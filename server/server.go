package server

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
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
	auto    struct {
		active   bool
		stopCh   chan struct{}
		interval time.Duration
	}
}

const (
	engineRandom            = "random"
	engineAlphaBeta         = "alpha-beta"
	engineAlphaBetaMobility = "alpha-beta-mobility"
	engineMCTS              = "mcts"
	engineHuman             = "human"
	defaultAutoInterval     = 1500 * time.Millisecond
)

func New(staticFS http.FileSystem) *Server {
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
	defer s.mu.Unlock()

	mv, err := s.moveFromRequest(s.game, req)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, moveResponse{
			Success: false,
			Error:   err.Error(),
			State:   s.serializeState(s.game),
		})
		return
	}

	legal, applied := game.TryApplyMove(s.game, mv)
	if !legal {
		writeJSON(w, http.StatusBadRequest, moveResponse{
			Success: false,
			Error:   "illegal move",
			State:   s.serializeState(s.game),
		})
		return
	}

	movingPlayer := s.game.Turn
	s.game = applied
	s.game.Turn = s.game.Turn.Opponent()
	s.recordMove(movingPlayer, mv, s.makeBoardPayload(s.game))
	responses, err := s.respondWithEnginesLocked()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, moveResponse{
			Success: false,
			Error:   err.Error(),
			State:   s.serializeState(s.game),
		})
		return
	}

	payload := s.serializeState(s.game)
	resp := moveResponse{
		Success: true,
		State:   payload,
	}
	var notes []string
	if len(responses) > 0 {
		notes = append(notes, responses...)
	}
	if payload.Checkmate {
		notes = append(notes, "Checkmate")
		resp.Winner = payload.Winner
	} else if payload.Check {
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

func (s *Server) respondWithEnginesLocked() ([]string, error) {
	var responses []string
	for {
		if s.auto.active {
			break
		}
		if mate, _ := game.CheckmateStatus(s.game); mate {
			break
		}
		engine := s.engines[s.game.Turn]
		if engine == nil {
			break
		}
		currentPlayer := s.game.Turn
		mv, err := engine.NextMove(s.game)
		if err != nil {
			return responses, errors.New("failed to generate move for " + playerLabel(currentPlayer))
		}
		game.ApplyMove(&s.game, mv)
		s.game.Turn = s.game.Turn.Opponent()
		s.recordMove(currentPlayer, mv, s.makeBoardPayload(s.game))
		responses = append(responses, playerLabel(currentPlayer)+": "+game.FormatMove(mv))
	}
	return responses, nil
}

func (s *Server) recordMove(player game.Player, mv game.Move, snapshot boardPayload) {
	s.history = append(s.history, historyEntry{
		Player:   playerKey(player),
		Move:     game.FormatMove(mv),
		Snapshot: snapshot,
	})
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
		s.engines[player] = nil
		s.modes[player] = engineHuman
		return nil
	}
	var eng game.Engine
	switch mode {
	case engineRandom:
		eng = game.NewRandomEngine(time.Now().UnixNano())
	case engineAlphaBeta:
		eng = game.NewAlphaBetaEngine(3)
	case engineAlphaBetaMobility:
		eng = game.NewMobilityAlphaBetaEngine(3)
	case engineMCTS:
		eng = game.NewMCTSEngine(800, time.Now().UnixNano())
	default:
		return errors.New("unknown engine requested")
	}
	s.engines[player] = eng
	s.modes[player] = mode
	return nil
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
			s.mu.Unlock()
		}
	}
}
