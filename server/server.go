package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gorogoro/game"
)

type Server struct {
	mu      sync.Mutex
	game    game.GameState
	history []historyEntry
	initial boardPayload
	static  http.Handler
	engines map[game.Player]game.Engine
	modes   map[game.Player]string
	dataDir string
	auto    struct {
		active   bool
		stopCh   chan struct{}
		interval time.Duration
	}
	training *trainingManager
}

const (
	engineRandom            = "random"
	engineAlphaBeta         = "alpha-beta"
	engineAlphaBetaMobility = "alpha-beta-mobility"
	engineMCTS              = "mcts"
	engineHuman             = "human"
	defaultAutoInterval     = 1500 * time.Millisecond
	defaultTrainingMaxMoves = 300
	defaultDataDir          = "data"
)

type Config struct {
	DataDir string
}

func New(staticFS http.FileSystem, cfg Config) *Server {
	dataDir := strings.TrimSpace(cfg.DataDir)
	if dataDir == "" {
		dataDir = defaultDataDir
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		log.Printf("failed to create data directory %q: %v", dataDir, err)
	}
	s := &Server{
		game:   game.NewGame(),
		static: http.FileServer(staticFS),
		engines: map[game.Player]game.Engine{
			game.Bottom: nil,
			game.Top:    nil,
		},
		modes: map[game.Player]string{
			game.Bottom: engineHuman,
			game.Top:    engineRandom,
		},
		dataDir:  dataDir,
		training: newTrainingManager(),
	}
	s.initial = s.makeBoardPayload(s.game)
	if err := s.setEngine(game.Top, engineRandom); err != nil {
		log.Printf("failed to initialize engine: %v", err)
	}
	return s
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/", s.static)
	mux.HandleFunc("/api/state", s.handleState)
	mux.HandleFunc("/api/legal", s.handleLegal)
	mux.HandleFunc("/api/move", s.handleMove)
	mux.HandleFunc("/api/reset", s.handleReset)
	mux.HandleFunc("/api/engine", s.handleEngine)
	mux.HandleFunc("/api/auto", s.handleAuto)
	mux.HandleFunc("/api/training", s.handleTraining)
	return mux
}

type piecePayload struct {
	Kind     string `json:"kind"`
	Owner    string `json:"owner,omitempty"`
	Promoted bool   `json:"promoted"`
	Present  bool   `json:"present"`
}

type boardPayload struct {
	Board     [][]piecePayload          `json:"board"`
	Hands     map[string]map[string]int `json:"hands"`
	Turn      string                    `json:"turn"`
	Check     bool                      `json:"check"`
	Checkmate bool                      `json:"checkmate"`
	Winner    string                    `json:"winner,omitempty"`
}

type statePayload struct {
	boardPayload
	Engine      string            `json:"engine"`
	Engines     map[string]string `json:"engines"`
	AutoPlaying bool              `json:"autoPlaying"`
	History     []historyEntry    `json:"history"`
	Initial     boardPayload      `json:"initial"`
}

type historyEntry struct {
	Player   string       `json:"player"`
	Move     string       `json:"move"`
	Snapshot boardPayload `json:"snapshot"`
}

type moveRequest struct {
	From    string `json:"from,omitempty"`
	To      string `json:"to"`
	Drop    string `json:"drop,omitempty"`
	Promote bool   `json:"promote"`
}

type moveResponse struct {
	Success bool         `json:"success"`
	Error   string       `json:"error,omitempty"`
	State   statePayload `json:"state"`
	Message string       `json:"message,omitempty"`
	Winner  string       `json:"winner,omitempty"`
}

type legalMovePayload struct {
	To      string `json:"to"`
	Promote bool   `json:"promote"`
}

type legalResponse struct {
	Moves []legalMovePayload `json:"moves"`
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	payload := s.serializeState(s.game)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) handleLegal(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	from := strings.TrimSpace(r.URL.Query().Get("from"))
	dropCode := strings.TrimSpace(r.URL.Query().Get("drop"))
	if from == "" && dropCode == "" {
		http.Error(w, "query 'from' or 'drop' is required", http.StatusBadRequest)
		return
	}

	var filtered []game.Move

	if from != "" {
		coord, err := game.ParseCoord(strings.ToLower(from))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		filtered = game.GenerateLegalMovesFrom(s.game, s.game.Turn, coord)
	} else {
		pt, ok := game.ParsePieceChar(strings.ToUpper(dropCode))
		if !ok {
			http.Error(w, "unknown piece type for drop", http.StatusBadRequest)
			return
		}
		if s.game.Hands[s.game.Turn][pt] == 0 {
			writeJSON(w, http.StatusOK, legalResponse{Moves: []legalMovePayload{}})
			return
		}
		filtered = game.GenerateLegalDrops(s.game, s.game.Turn, pt)
	}

	resp := legalResponse{Moves: make([]legalMovePayload, 0, len(filtered))}
	for _, mv := range filtered {
		resp.Moves = append(resp.Moves, legalMovePayload{
			To:      game.CoordToString(mv.To),
			Promote: mv.Promote,
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleMove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var req moveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	if s.auto.active {
		payload := s.serializeState(s.game)
		s.mu.Unlock()
		writeJSON(w, http.StatusConflict, moveResponse{
			Success: false,
			Error:   "auto play is running",
			State:   payload,
		})
		return
	}

	mv, err := s.moveFromRequest(s.game, req)
	if err != nil {
		payload := s.serializeState(s.game)
		s.mu.Unlock()
		writeJSON(w, http.StatusBadRequest, moveResponse{
			Success: false,
			Error:   err.Error(),
			State:   payload,
		})
		return
	}

	legal, applied := game.TryApplyMove(s.game, mv)
	if !legal {
		payload := s.serializeState(s.game)
		s.mu.Unlock()
		writeJSON(w, http.StatusBadRequest, moveResponse{
			Success: false,
			Error:   "illegal move",
			State:   payload,
		})
		return
	}

	movingPlayer := s.game.Turn
	s.game = applied
	s.game.Turn = s.game.Turn.Opponent()
	s.recordMove(movingPlayer, mv, s.makeBoardPayload(s.game))
	s.mu.Unlock()

	responses, err := s.respondWithEngines()
	if err != nil {
		s.mu.Lock()
		payload := s.serializeState(s.game)
		s.mu.Unlock()
		writeJSON(w, http.StatusInternalServerError, moveResponse{
			Success: false,
			Error:   err.Error(),
			State:   payload,
		})
		return
	}

	s.mu.Lock()
	payload := s.serializeState(s.game)
	checkmate := payload.Checkmate
	check := payload.Check
	if checkmate {
		s.flushEngineDataLocked()
	}
	s.mu.Unlock()
	resp := moveResponse{
		Success: true,
		State:   payload,
	}
	var notes []string
	if len(responses) > 0 {
		notes = append(notes, responses...)
	}
	if checkmate {
		notes = append(notes, "Checkmate")
		resp.Winner = payload.Winner
	} else if check {
		notes = append(notes, "Check")
	}
	if len(notes) > 0 {
		resp.Message = strings.Join(notes, " / ")
	}

	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	s.mu.Lock()
	s.stopAutoPlayLocked()
	s.flushEngineDataLocked()
	s.game = game.NewGame()
	s.history = nil
	s.initial = s.makeBoardPayload(s.game)
	payload := s.serializeState(s.game)
	s.mu.Unlock()

	writeJSON(w, http.StatusOK, payload)
}

type engineResponse struct {
	Engine  string            `json:"engine"`
	Engines map[string]string `json:"engines"`
}

type engineRequest struct {
	Player string `json:"player"`
	Engine string `json:"engine"`
}

type autoRequest struct {
	Running    bool `json:"running"`
	IntervalMS int  `json:"interval_ms"`
}

type autoResponse struct {
	Running bool `json:"running"`
}

type trainingRequest struct {
	Action       string `json:"action"`
	Games        int    `json:"games"`
	Parallel     int    `json:"parallel"`
	EngineBottom string `json:"engine_bottom"`
	EngineTop    string `json:"engine_top"`
	IntervalMS   int    `json:"interval_ms"`
	MaxMoves     int    `json:"max_moves"`
}

type trainingStatePayload struct {
	Running bool                  `json:"running"`
	Config  trainingConfigPayload `json:"config"`
	Summary trainingSummary       `json:"summary"`
	Games   []trainingGameStatus  `json:"games"`
}

type trainingConfigPayload struct {
	Total        int    `json:"total"`
	Parallel     int    `json:"parallel"`
	BottomEngine string `json:"bottomEngine"`
	TopEngine    string `json:"topEngine"`
	IntervalMS   int    `json:"intervalMs"`
	MaxMoves     int    `json:"maxMoves"`
}

type trainingSummary struct {
	Total      int  `json:"total"`
	Completed  int  `json:"completed"`
	BottomWins int  `json:"bottomWins"`
	TopWins    int  `json:"topWins"`
	Draws      int  `json:"draws"`
	Errors     int  `json:"errors"`
	Aborted    bool `json:"aborted"`
}

type trainingGameStatus struct {
	ID       int    `json:"id"`
	Moves    int    `json:"moves"`
	Winner   string `json:"winner,omitempty"`
	Result   string `json:"result,omitempty"`
	State    string `json:"state"`
	LastMove string `json:"lastMove,omitempty"`
	Turn     string `json:"turn,omitempty"`
	Error    string `json:"error,omitempty"`
}

func (s *Server) handleEngine(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.engineStatus())
		return
	case http.MethodPost:
		var payload engineRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		player := game.Top
		if payload.Player != "" {
			mapped, ok := parsePlayer(payload.Player)
			if !ok {
				http.Error(w, "unknown player for engine", http.StatusBadRequest)
				return
			}
			player = mapped
		}
		if err := s.setEngine(player, payload.Engine); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, s.engineStatus())
		return
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
}

func (s *Server) handleAuto(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var payload autoRequest
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if payload.Running {
		interval := time.Duration(payload.IntervalMS) * time.Millisecond
		if err := s.startAutoPlayLocked(interval); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	} else {
		s.stopAutoPlayLocked()
	}

	writeJSON(w, http.StatusOK, autoResponse{Running: s.auto.active})
}

func (s *Server) handleTraining(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.training.Snapshot())
		return
	case http.MethodPost:
		var payload trainingRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		action := strings.ToLower(strings.TrimSpace(payload.Action))
		switch action {
		case "start":
			cfg, err := s.buildTrainingConfig(payload)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if err := s.training.Start(cfg); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			writeJSON(w, http.StatusOK, s.training.Snapshot())
			return
		case "stop":
			if err := s.training.Stop(); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			writeJSON(w, http.StatusOK, s.training.Snapshot())
			return
		default:
			http.Error(w, "unknown action for training", http.StatusBadRequest)
			return
		}
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
}

func (s *Server) buildTrainingConfig(req trainingRequest) (trainingConfig, error) {
	cfg := trainingConfig{
		Total:        req.Games,
		Parallel:     req.Parallel,
		BottomEngine: strings.TrimSpace(req.EngineBottom),
		TopEngine:    strings.TrimSpace(req.EngineTop),
		IntervalMS:   req.IntervalMS,
		MaxMoves:     req.MaxMoves,
	}
	if cfg.Total <= 0 {
		return trainingConfig{}, errors.New("games must be greater than zero")
	}
	if cfg.Parallel <= 0 {
		cfg.Parallel = 1
	}
	if cfg.Parallel > cfg.Total {
		cfg.Parallel = cfg.Total
	}
	if cfg.IntervalMS > 0 {
		cfg.Interval = time.Duration(cfg.IntervalMS) * time.Millisecond
	}
	if cfg.MaxMoves <= 0 {
		cfg.MaxMoves = defaultTrainingMaxMoves
	}
	if cfg.BottomEngine == "" || cfg.BottomEngine == engineHuman {
		return trainingConfig{}, errors.New("bottom engine must be an AI engine")
	}
	if cfg.TopEngine == "" || cfg.TopEngine == engineHuman {
		return trainingConfig{}, errors.New("top engine must be an AI engine")
	}
	return cfg, nil
}

func (s *Server) moveFromRequest(state game.GameState, req moveRequest) (game.Move, error) {
	if req.Drop != "" && req.From != "" {
		return game.Move{}, errors.New("specify either 'from' or 'drop', not both")
	}
	if req.Drop == "" && req.From == "" {
		return game.Move{}, errors.New("'from' or 'drop' is required")
	}
	if req.To == "" {
		return game.Move{}, errors.New("'to' is required")
	}

	to, err := game.ParseCoord(strings.ToLower(req.To))
	if err != nil {
		return game.Move{}, err
	}

	if req.Drop != "" {
		pt, ok := game.ParsePieceChar(strings.ToUpper(req.Drop))
		if !ok {
			return game.Move{}, errors.New("unknown piece type for drop")
		}
		if state.Hands[state.Turn][pt] == 0 {
			return game.Move{}, errors.New("specified drop piece is not in hand")
		}
		return game.Move{Drop: &pt, To: to}, nil
	}

	from, err := game.ParseCoord(strings.ToLower(req.From))
	if err != nil {
		return game.Move{}, err
	}

	return game.Move{From: &from, To: to, Promote: req.Promote}, nil
}

func (s *Server) serializeState(state game.GameState) statePayload {
	return statePayload{
		boardPayload: s.makeBoardPayload(state),
		Engine:       s.modes[game.Top],
		Engines:      map[string]string{"bottom": s.modes[game.Bottom], "top": s.modes[game.Top]},
		AutoPlaying:  s.auto.active,
		History:      append([]historyEntry(nil), s.history...),
		Initial:      s.initial,
	}
}

func (s *Server) makeBoardPayload(state game.GameState) boardPayload {
	payload := boardPayload{
		Board: make([][]piecePayload, game.BoardRows),
		Hands: map[string]map[string]int{
			"bottom": {},
			"top":    {},
		},
		Turn: playerKey(state.Turn),
	}

	for y := 0; y < game.BoardRows; y++ {
		payload.Board[y] = make([]piecePayload, game.BoardCols)
		for x := 0; x < game.BoardCols; x++ {
			p := state.Board[y][x]
			cell := piecePayload{
				Promoted: p.Promoted,
				Present:  p.Present,
			}
			if p.Present {
				cell.Kind = game.PieceTypeCode(p.Kind)
				cell.Owner = playerKey(p.Owner)
			}
			payload.Board[y][x] = cell
		}
	}

	for pType, count := range state.Hands[game.Bottom] {
		if count > 0 {
			payload.Hands["bottom"][game.PieceTypeCode(pType)] = count
		}
	}
	for pType, count := range state.Hands[game.Top] {
		if count > 0 {
			payload.Hands["top"][game.PieceTypeCode(pType)] = count
		}
	}

	payload.Check = game.InCheck(state, state.Turn)
	if mate, winner := game.CheckmateStatus(state); mate {
		payload.Checkmate = true
		payload.Winner = playerKey(winner)
	}
	return payload
}

func playerKey(p game.Player) string {
	if p == game.Bottom {
		return "bottom"
	}
	return "top"
}

func playerLabel(p game.Player) string {
	if p == game.Bottom {
		return "先手"
	}
	return "後手"
}

func parsePlayer(value string) (game.Player, bool) {
	switch strings.ToLower(value) {
	case "", "top":
		return game.Top, true
	case "bottom":
		return game.Bottom, true
	default:
		return game.Bottom, false
	}
}

func (s *Server) respondWithEngines() ([]string, error) {
	var responses []string
	for {
		s.mu.Lock()
		if s.auto.active {
			s.mu.Unlock()
			break
		}
		if mate, _ := game.CheckmateStatus(s.game); mate {
			s.mu.Unlock()
			break
		}
		engine := s.engines[s.game.Turn]
		if engine == nil {
			s.mu.Unlock()
			break
		}
		currentPlayer := s.game.Turn
		stateCopy := cloneGameState(s.game)
		s.mu.Unlock()

		mv, err := engine.NextMove(stateCopy)
		if err != nil {
			return responses, errors.New("failed to generate move for " + playerLabel(currentPlayer))
		}

		s.mu.Lock()
		if s.auto.active || s.game.Turn != currentPlayer || s.engines[currentPlayer] != engine {
			s.mu.Unlock()
			continue
		}
		game.ApplyMove(&s.game, mv)
		s.game.Turn = s.game.Turn.Opponent()
		s.recordMove(currentPlayer, mv, s.makeBoardPayload(s.game))
		s.mu.Unlock()

		responses = append(responses, playerLabel(currentPlayer)+": "+game.FormatMove(mv))
	}
	return responses, nil
}

func cloneGameState(state game.GameState) game.GameState {
	clone := state
	for idx, hand := range state.Hands {
		if hand == nil {
			clone.Hands[idx] = nil
			continue
		}
		copyHand := make(map[game.PieceType]int, len(hand))
		for pt, count := range hand {
			copyHand[pt] = count
		}
		clone.Hands[idx] = copyHand
	}
	return clone
}

func (s *Server) recordMove(player game.Player, mv game.Move, snapshot boardPayload) {
	s.history = append(s.history, historyEntry{
		Player:   playerKey(player),
		Move:     game.FormatMove(mv),
		Snapshot: snapshot,
	})
}

type savableEngine interface {
	SaveIfNeeded() error
}

func saveEngineData(eng game.Engine) {
	if eng == nil {
		return
	}
	if saver, ok := eng.(savableEngine); ok {
		if err := saver.SaveIfNeeded(); err != nil {
			log.Printf("failed to save engine data: %v", err)
		}
	}
}

func (s *Server) flushEngineDataLocked() {
	for _, eng := range s.engines {
		saveEngineData(eng)
	}
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("failed to write JSON response: %v", err)
	}
}

func (s *Server) engineStatus() engineResponse {
	return engineResponse{
		Engine: s.modes[game.Top],
		Engines: map[string]string{
			"bottom": s.modes[game.Bottom],
			"top":    s.modes[game.Top],
		},
	}
}

func (s *Server) setEngine(player game.Player, kind string) error {
	mode := strings.TrimSpace(kind)
	if mode == "" || mode == engineHuman {
		saveEngineData(s.engines[player])
		s.engines[player] = nil
		s.modes[player] = engineHuman
		return nil
	}
	saveEngineData(s.engines[player])
	eng, err := s.buildEngine(mode, player)
	if err != nil {
		return err
	}
	s.engines[player] = eng
	s.modes[player] = mode
	return nil
}

func (s *Server) buildEngine(mode string, player game.Player) (game.Engine, error) {
	if mode == engineMCTS {
		path := filepath.Join(s.dataDir, fmt.Sprintf("mcts_%s.json", playerKey(player)))
		return game.NewPersistentMCTSEngine(800, time.Now().UnixNano(), path), nil
	}
	return newEngineForMode(mode)
}

func newEngineForMode(mode string) (game.Engine, error) {
	switch mode {
	case engineRandom:
		return game.NewRandomEngine(time.Now().UnixNano()), nil
	case engineAlphaBeta:
		return game.NewAlphaBetaEngine(3), nil
	case engineAlphaBetaMobility:
		return game.NewMobilityAlphaBetaEngine(3), nil
	case engineMCTS:
		return game.NewMCTSEngine(800, time.Now().UnixNano()), nil
	default:
		return nil, errors.New("unknown engine requested: " + mode)
	}
}

func (s *Server) startAutoPlayLocked(interval time.Duration) error {
	if s.auto.active {
		return errors.New("auto play already running")
	}
	if s.engines[game.Bottom] == nil || s.engines[game.Top] == nil {
		return errors.New("both players must be engines to start auto play")
	}
	if interval <= 0 {
		interval = defaultAutoInterval
	}
	stop := make(chan struct{})
	s.auto.active = true
	s.auto.stopCh = stop
	s.auto.interval = interval
	go s.runAutoPlay(stop, interval)
	return nil
}

func (s *Server) stopAutoPlayLocked() {
	if !s.auto.active {
		return
	}
	if s.auto.stopCh != nil {
		close(s.auto.stopCh)
	}
	s.auto.active = false
	s.auto.stopCh = nil
}

func (s *Server) runAutoPlay(stop <-chan struct{}, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			s.mu.Lock()
			if !s.auto.active {
				s.mu.Unlock()
				return
			}
			if mate, _ := game.CheckmateStatus(s.game); mate {
				s.flushEngineDataLocked()
				s.auto.active = false
				s.auto.stopCh = nil
				s.mu.Unlock()
				return
			}
			engine := s.engines[s.game.Turn]
			if engine == nil {
				s.auto.active = false
				s.auto.stopCh = nil
				s.mu.Unlock()
				return
			}
			mv, err := engine.NextMove(s.game)
			if err != nil {
				log.Printf("auto play failed: %v", err)
				s.auto.active = false
				s.auto.stopCh = nil
				s.mu.Unlock()
				return
			}
			movingPlayer := s.game.Turn
			game.ApplyMove(&s.game, mv)
			s.game.Turn = s.game.Turn.Opponent()
			s.recordMove(movingPlayer, mv, s.makeBoardPayload(s.game))
			if mate, _ := game.CheckmateStatus(s.game); mate {
				s.flushEngineDataLocked()
			}
			s.mu.Unlock()
		}
	}
}

type trainingConfig struct {
	Total        int
	Parallel     int
	BottomEngine string
	TopEngine    string
	Interval     time.Duration
	IntervalMS   int
	MaxMoves     int
}

type trainingManager struct {
	mu      sync.Mutex
	running bool
	config  trainingConfig
	summary trainingSummary
	games   map[int]*trainingGameStatus
	stopCh  chan struct{}
}

func newTrainingManager() *trainingManager {
	return &trainingManager{
		games: make(map[int]*trainingGameStatus),
	}
}

func (tm *trainingManager) Start(cfg trainingConfig) error {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if tm.running || tm.stopCh != nil {
		return errors.New("training already running")
	}
	tm.running = true
	tm.config = cfg
	tm.summary = trainingSummary{Total: cfg.Total}
	tm.games = make(map[int]*trainingGameStatus)
	stop := make(chan struct{})
	tm.stopCh = stop
	go tm.run(cfg, stop)
	return nil
}

func (tm *trainingManager) Stop() error {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if tm.stopCh == nil {
		return errors.New("training not running")
	}
	select {
	case <-tm.stopCh:
	default:
		close(tm.stopCh)
	}
	tm.summary.Aborted = true
	tm.running = false
	return nil
}

func (tm *trainingManager) Snapshot() trainingStatePayload {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	ids := make([]int, 0, len(tm.games))
	for id := range tm.games {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	games := make([]trainingGameStatus, 0, len(ids))
	for _, id := range ids {
		status := tm.games[id]
		if status == nil {
			continue
		}
		copyStatus := *status
		games = append(games, copyStatus)
	}
	payload := trainingStatePayload{
		Running: tm.running,
		Summary: tm.summary,
		Games:   games,
	}
	if tm.config.Total > 0 {
		payload.Config = trainingConfigPayload{
			Total:        tm.config.Total,
			Parallel:     tm.config.Parallel,
			BottomEngine: tm.config.BottomEngine,
			TopEngine:    tm.config.TopEngine,
			IntervalMS:   tm.config.IntervalMS,
			MaxMoves:     tm.config.MaxMoves,
		}
	}
	return payload
}

func (tm *trainingManager) run(cfg trainingConfig, stop <-chan struct{}) {
	sem := make(chan struct{}, cfg.Parallel)
	var wg sync.WaitGroup
	aborted := false
launch:
	for id := 1; id <= cfg.Total; id++ {
		select {
		case <-stop:
			aborted = true
			break launch
		default:
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(gameID int) {
			defer func() {
				<-sem
				wg.Done()
			}()
			tm.playSingleGame(gameID, cfg, stop)
		}(id)
	}
	wg.Wait()
	tm.mu.Lock()
	if aborted {
		tm.summary.Aborted = true
	}
	tm.stopCh = nil
	tm.running = false
	tm.mu.Unlock()
}

func (tm *trainingManager) playSingleGame(id int, cfg trainingConfig, stop <-chan struct{}) {
	tm.registerGame(id)
	bottomEngine, err := newEngineForMode(cfg.BottomEngine)
	if err != nil {
		tm.recordGameError(id, err)
		return
	}
	topEngine, err := newEngineForMode(cfg.TopEngine)
	if err != nil {
		tm.recordGameError(id, err)
		return
	}
	state := game.NewGame()
	moves := 0
	lastMove := ""
	for {
		select {
		case <-stop:
			tm.markGameAborted(id, moves, lastMove)
			return
		default:
		}
		if mate, winner := game.CheckmateStatus(state); mate {
			tm.finishGameWin(id, winner, moves, lastMove)
			return
		}
		if cfg.MaxMoves > 0 && moves >= cfg.MaxMoves {
			tm.finishGameDraw(id, moves, lastMove)
			return
		}
		var eng game.Engine
		if state.Turn == game.Bottom {
			eng = bottomEngine
		} else {
			eng = topEngine
		}
		mv, err := eng.NextMove(state)
		if err != nil {
			tm.recordGameError(id, err)
			return
		}
		game.ApplyMove(&state, mv)
		state.Turn = state.Turn.Opponent()
		moves++
		lastMove = game.FormatMove(mv)
		tm.updateGameProgress(id, moves, lastMove, state.Turn)
		if cfg.Interval > 0 {
			select {
			case <-stop:
				tm.markGameAborted(id, moves, lastMove)
				return
			case <-time.After(cfg.Interval):
			}
		}
	}
}

func (tm *trainingManager) registerGame(id int) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.games[id] = &trainingGameStatus{
		ID:    id,
		State: "running",
		Turn:  playerKey(game.Bottom),
	}
}

func (tm *trainingManager) updateGameProgress(id, moves int, lastMove string, next game.Player) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if status, ok := tm.games[id]; ok {
		status.Moves = moves
		status.LastMove = lastMove
		status.Turn = playerKey(next)
	}
}

func (tm *trainingManager) finishGameWin(id int, winner game.Player, moves int, lastMove string) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	status := tm.ensureStatus(id)
	status.Moves = moves
	status.LastMove = lastMove
	status.Winner = playerKey(winner)
	status.Result = "win"
	status.State = "completed"
	status.Turn = ""
	tm.summary.Completed++
	if winner == game.Bottom {
		tm.summary.BottomWins++
	} else {
		tm.summary.TopWins++
	}
}

func (tm *trainingManager) finishGameDraw(id, moves int, lastMove string) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	status := tm.ensureStatus(id)
	status.Moves = moves
	status.LastMove = lastMove
	status.Result = "draw"
	status.State = "completed"
	status.Turn = ""
	tm.summary.Completed++
	tm.summary.Draws++
}

func (tm *trainingManager) recordGameError(id int, err error) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	status := tm.ensureStatus(id)
	status.Result = "error"
	status.State = "error"
	status.Error = err.Error()
	status.Turn = ""
	tm.summary.Completed++
	tm.summary.Errors++
}

func (tm *trainingManager) markGameAborted(id, moves int, lastMove string) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	status := tm.ensureStatus(id)
	status.Moves = moves
	status.LastMove = lastMove
	status.Result = "aborted"
	status.State = "aborted"
	status.Turn = ""
}

func (tm *trainingManager) ensureStatus(id int) *trainingGameStatus {
	status, ok := tm.games[id]
	if !ok {
		status = &trainingGameStatus{ID: id}
		tm.games[id] = status
	}
	return status
}
