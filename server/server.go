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
	mu     sync.Mutex
	game   game.GameState
	static http.Handler
	engine game.Engine
	mode   string
}

const (
	engineRandom    = "random"
	engineAlphaBeta = "alpha-beta"
)

func New(staticFS http.FileSystem) *Server {
	s := &Server{
		game:   game.NewGame(),
		static: http.FileServer(staticFS),
	}
	if err := s.setEngine(engineRandom); err != nil {
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
	return mux
}

type piecePayload struct {
	Kind     string `json:"kind"`
	Owner    string `json:"owner,omitempty"`
	Promoted bool   `json:"promoted"`
	Present  bool   `json:"present"`
}

type statePayload struct {
	Board     [][]piecePayload          `json:"board"`
	Hands     map[string]map[string]int `json:"hands"`
	Turn      string                    `json:"turn"`
	Check     bool                      `json:"check"`
	Checkmate bool                      `json:"checkmate"`
	Winner    string                    `json:"winner,omitempty"`
	Engine    string                    `json:"engine"`
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

	s.game = applied
	s.game.Turn = s.game.Turn.Opponent()
	aiMessage, err := s.respondAsGoteIfNeeded()
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
	if aiMessage != "" {
		notes = append(notes, aiMessage)
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
	s.game = game.NewGame()
	payload := s.serializeState(s.game)
	s.mu.Unlock()

	writeJSON(w, http.StatusOK, payload)
}

type engineResponse struct {
	Engine string `json:"engine"`
}

type engineRequest struct {
	Engine string `json:"engine"`
}

func (s *Server) handleEngine(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, engineResponse{Engine: s.mode})
		return
	case http.MethodPost:
		var payload engineRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		if err := s.setEngine(payload.Engine); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, engineResponse{Engine: s.mode})
		return
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
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
	payload := statePayload{
		Board: make([][]piecePayload, game.BoardRows),
		Hands: map[string]map[string]int{
			"bottom": {},
			"top":    {},
		},
		Turn:   playerKey(state.Turn),
		Engine: s.mode,
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
	if game.IsCheckmate(state, state.Turn) {
		payload.Checkmate = true
		payload.Winner = playerKey(state.Turn.Opponent())
	}
	return payload
}

func playerKey(p game.Player) string {
	if p == game.Bottom {
		return "bottom"
	}
	return "top"
}

func (s *Server) respondAsGoteIfNeeded() (string, error) {
	if s.engine == nil {
		return "", nil
	}
	if s.game.Turn != game.Top {
		return "", nil
	}
	if game.IsCheckmate(s.game, s.game.Turn) {
		return "", nil
	}
	mv, err := s.engine.NextMove(s.game)
	if err != nil {
		return "", errors.New("failed to generate move for gote")
	}
	game.ApplyMove(&s.game, mv)
	s.game.Turn = s.game.Turn.Opponent()
	return "後手: " + game.FormatMove(mv), nil
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("failed to write JSON response: %v", err)
	}
}

func (s *Server) setEngine(kind string) error {
	switch kind {
	case engineRandom:
		s.engine = game.NewRandomEngine(time.Now().UnixNano())
	case engineAlphaBeta:
		s.engine = game.NewAlphaBetaEngine(3)
	default:
		return errors.New("unknown engine requested")
	}
	s.mode = kind
	return nil
}
