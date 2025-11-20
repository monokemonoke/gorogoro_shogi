package main

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"math/rand"
	"net/http"
	"strings"
	"sync"
)

const (
	boardRows = 6
	boardCols = 5
)

type Player int

const (
	Bottom Player = iota
	Top
)

func (p Player) opponent() Player {
	if p == Bottom {
		return Top
	}
	return Bottom
}

func (p Player) label() string {
	if p == Bottom {
		return "Bottom (moves up)"
	}
	return "Top (moves down)"
}

type PieceType int

const (
	King PieceType = iota
	Gold
	Silver
	Pawn
)

type Piece struct {
	Kind     PieceType
	Owner    Player
	Promoted bool
	Present  bool
}

type Coord struct {
	X int
	Y int
}

type Move struct {
	From    *Coord // nil for drops
	To      Coord
	Drop    *PieceType
	Promote bool
}

// Engine defines the contract for thinking components so they can be swapped easily.
// It expects state.Turn to point to the player that should move and returns the chosen move.
type Engine interface {
	NextMove(state GameState) (Move, error)
}

type GameState struct {
	Board [boardRows][boardCols]Piece
	Hands [2]map[PieceType]int
	Turn  Player
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

var (
	stateMu     sync.Mutex
	currentGame GameState
)

//go:embed web/*
var webFS embed.FS

func main() {
	currentGame = newGame()

	webRoot, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatalf("failed to load web assets: %v", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(webRoot)))
	mux.HandleFunc("/api/state", handleState)
	mux.HandleFunc("/api/legal", handleLegal)
	mux.HandleFunc("/api/move", handleMove)
	mux.HandleFunc("/api/reset", handleReset)

	addr := ":8080"
	log.Printf("Serving Gorogoro Shogi UI at http://localhost%s\n", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

func handleState(w http.ResponseWriter, r *http.Request) {
	stateMu.Lock()
	payload := serializeState(currentGame)
	stateMu.Unlock()
	writeJSON(w, http.StatusOK, payload)
}

func handleLegal(w http.ResponseWriter, r *http.Request) {
	stateMu.Lock()
	defer stateMu.Unlock()

	from := strings.TrimSpace(r.URL.Query().Get("from"))
	dropCode := strings.TrimSpace(r.URL.Query().Get("drop"))
	if from == "" && dropCode == "" {
		http.Error(w, "query 'from' or 'drop' is required", http.StatusBadRequest)
		return
	}

	candidates := generateLegalMoves(currentGame, currentGame.Turn)
	var filtered []Move

	if from != "" {
		coord, err := parseCoord(strings.ToLower(from))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		for _, mv := range candidates {
			if mv.From != nil && *mv.From == coord {
				filtered = append(filtered, mv)
			}
		}
	} else {
		pt, ok := parsePieceChar(strings.ToUpper(dropCode))
		if !ok {
			http.Error(w, "unknown piece type for drop", http.StatusBadRequest)
			return
		}
		if currentGame.Hands[currentGame.Turn][pt] == 0 {
			writeJSON(w, http.StatusOK, legalResponse{Moves: []legalMovePayload{}})
			return
		}
		for _, mv := range candidates {
			if mv.Drop != nil && *mv.Drop == pt {
				filtered = append(filtered, mv)
			}
		}
	}

	resp := legalResponse{Moves: make([]legalMovePayload, 0, len(filtered))}
	for _, mv := range filtered {
		resp.Moves = append(resp.Moves, legalMovePayload{
			To:      coordToString(mv.To),
			Promote: mv.Promote,
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

func handleMove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var req moveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	stateMu.Lock()
	defer stateMu.Unlock()

	mv, err := moveFromRequest(currentGame, req)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, moveResponse{
			Success: false,
			Error:   err.Error(),
			State:   serializeState(currentGame),
		})
		return
	}

	legal, applied := tryApplyMove(currentGame, mv)
	if !legal {
		writeJSON(w, http.StatusBadRequest, moveResponse{
			Success: false,
			Error:   "illegal move",
			State:   serializeState(currentGame),
		})
		return
	}

	currentGame = applied
	currentGame.Turn = currentGame.Turn.opponent()

	payload := serializeState(currentGame)
	resp := moveResponse{
		Success: true,
		State:   payload,
	}
	if payload.Checkmate {
		resp.Message = "Checkmate"
		resp.Winner = payload.Winner
	} else if payload.Check {
		resp.Message = "Check"
	}

	writeJSON(w, http.StatusOK, resp)
}

func handleReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	stateMu.Lock()
	currentGame = newGame()
	payload := serializeState(currentGame)
	stateMu.Unlock()

	writeJSON(w, http.StatusOK, payload)
}

func moveFromRequest(state GameState, req moveRequest) (Move, error) {
	if req.Drop != "" && req.From != "" {
		return Move{}, errors.New("specify either 'from' or 'drop', not both")
	}
	if req.Drop == "" && req.From == "" {
		return Move{}, errors.New("'from' or 'drop' is required")
	}
	if req.To == "" {
		return Move{}, errors.New("'to' is required")
	}

	to, err := parseCoord(strings.ToLower(req.To))
	if err != nil {
		return Move{}, err
	}

	if req.Drop != "" {
		pt, ok := parsePieceChar(strings.ToUpper(req.Drop))
		if !ok {
			return Move{}, errors.New("unknown piece type for drop")
		}
		if state.Hands[state.Turn][pt] == 0 {
			return Move{}, errors.New("specified drop piece is not in hand")
		}
		return Move{Drop: &pt, To: to}, nil
	}

	from, err := parseCoord(strings.ToLower(req.From))
	if err != nil {
		return Move{}, err
	}

	return Move{From: &from, To: to, Promote: req.Promote}, nil
}

func serializeState(state GameState) statePayload {
	payload := statePayload{
		Board: make([][]piecePayload, boardRows),
		Hands: map[string]map[string]int{
			"bottom": {},
			"top":    {},
		},
		Turn: playerKey(state.Turn),
	}

	for y := 0; y < boardRows; y++ {
		payload.Board[y] = make([]piecePayload, boardCols)
		for x := 0; x < boardCols; x++ {
			p := state.Board[y][x]
			cell := piecePayload{
				Promoted: p.Promoted,
				Present:  p.Present,
			}
			if p.Present {
				cell.Kind = pieceTypeCode(p.Kind)
				cell.Owner = playerKey(p.Owner)
			}
			payload.Board[y][x] = cell
		}
	}

	for pType, count := range state.Hands[Bottom] {
		if count > 0 {
			payload.Hands["bottom"][pieceTypeCode(pType)] = count
		}
	}
	for pType, count := range state.Hands[Top] {
		if count > 0 {
			payload.Hands["top"][pieceTypeCode(pType)] = count
		}
	}

	payload.Check = inCheck(state, state.Turn)
	if isCheckmate(state, state.Turn) {
		payload.Checkmate = true
		payload.Winner = playerKey(state.Turn.opponent())
	}
	return payload
}

func playerKey(p Player) string {
	if p == Bottom {
		return "bottom"
	}
	return "top"
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("failed to write JSON response: %v", err)
	}
}

func newGame() GameState {
	state := GameState{
		Hands: [2]map[PieceType]int{
			Bottom: make(map[PieceType]int),
			Top:    make(map[PieceType]int),
		},
		Turn: Bottom,
	}

	placeMajor := func(y int, owner Player) {
		state.Board[y][0] = Piece{Kind: Silver, Owner: owner, Present: true}
		state.Board[y][1] = Piece{Kind: Gold, Owner: owner, Present: true}
		state.Board[y][2] = Piece{Kind: King, Owner: owner, Present: true}
		state.Board[y][3] = Piece{Kind: Gold, Owner: owner, Present: true}
		state.Board[y][4] = Piece{Kind: Silver, Owner: owner, Present: true}
	}

	placePawns := func(y int, owner Player) {
		state.Board[y][1] = Piece{Kind: Pawn, Owner: owner, Present: true}
		state.Board[y][2] = Piece{Kind: Pawn, Owner: owner, Present: true}
		state.Board[y][3] = Piece{Kind: Pawn, Owner: owner, Present: true}
	}

	placeMajor(0, Bottom)
	placePawns(2, Bottom)
	placePawns(3, Top)
	placeMajor(5, Top)

	return state
}

type RandomEngine struct {
	rng *rand.Rand
}

func NewRandomEngine(seed int64) *RandomEngine {
	return &RandomEngine{rng: rand.New(rand.NewSource(seed))}
}

func (e *RandomEngine) NextMove(state GameState) (Move, error) {
	moves := generateLegalMoves(state, state.Turn)
	if len(moves) == 0 {
		return Move{}, errors.New("no legal moves to play")
	}
	return moves[e.rng.Intn(len(moves))], nil
}

type AlphaBetaEngine struct {
	Depth int
}

func NewAlphaBetaEngine(depth int) *AlphaBetaEngine {
	return &AlphaBetaEngine{Depth: depth}
}

const (
	checkmateScore = 100000
	infiniteScore  = 1_000_000_000
)

var pieceScores = map[PieceType]int{
	King:   1000,
	Gold:   70,
	Silver: 50,
	Pawn:   10,
}

func (e *AlphaBetaEngine) NextMove(state GameState) (Move, error) {
	moves := generateLegalMoves(state, state.Turn)
	if len(moves) == 0 {
		return Move{}, errors.New("no legal moves to play")
	}
	_, best := e.search(state, e.Depth, -infiniteScore, infiniteScore, state.Turn)
	if best == nil {
		return Move{}, errors.New("failed to find a move")
	}
	return *best, nil
}

func (e *AlphaBetaEngine) search(state GameState, depth int, alpha, beta int, maximizer Player) (int, *Move) {
	if depth == 0 {
		return e.evaluate(state, maximizer, depth), nil
	}

	legal := generateLegalMoves(state, state.Turn)
	if len(legal) == 0 {
		return e.evaluate(state, maximizer, depth), nil
	}

	var chosen *Move
	if state.Turn == maximizer {
		bestScore := -infiniteScore
		for _, mv := range legal {
			next := cloneState(state)
			applyMove(&next, mv)
			next.Turn = next.Turn.opponent()

			score, _ := e.search(next, depth-1, alpha, beta, maximizer)
			if score > bestScore {
				bestScore = score
				mvCopy := mv
				chosen = &mvCopy
			}
			if bestScore > alpha {
				alpha = bestScore
			}
			if beta <= alpha {
				break
			}
		}
		return bestScore, chosen
	}

	bestScore := infiniteScore
	for _, mv := range legal {
		next := cloneState(state)
		applyMove(&next, mv)
		next.Turn = next.Turn.opponent()

		score, _ := e.search(next, depth-1, alpha, beta, maximizer)
		if score < bestScore {
			bestScore = score
			mvCopy := mv
			chosen = &mvCopy
		}
		if bestScore < beta {
			beta = bestScore
		}
		if beta <= alpha {
			break
		}
	}
	return bestScore, chosen
}

func (e *AlphaBetaEngine) evaluate(state GameState, maximizer Player, depth int) int {
	if isCheckmate(state, maximizer) {
		return -checkmateScore - depth
	}
	if isCheckmate(state, maximizer.opponent()) {
		return checkmateScore + depth
	}

	score := 0
	for y := 0; y < boardRows; y++ {
		for x := 0; x < boardCols; x++ {
			p := state.Board[y][x]
			if !p.Present {
				continue
			}
			value := pieceValue(p)
			if p.Owner == maximizer {
				score += value
			} else {
				score -= value
			}
		}
	}

	for pType, count := range state.Hands[maximizer] {
		score += pieceScores[pType] * count
	}
	for pType, count := range state.Hands[maximizer.opponent()] {
		score -= pieceScores[pType] * count
	}

	if inCheck(state, maximizer) {
		score -= 5
	}
	if inCheck(state, maximizer.opponent()) {
		score += 5
	}
	return score
}

func pieceValue(p Piece) int {
	// Promoted silver/pawn move like gold, so borrow its value.
	if (p.Kind == Silver || p.Kind == Pawn) && p.Promoted {
		return pieceScores[Gold]
	}
	return pieceScores[p.Kind]
}

func pieceTypeCode(pt PieceType) string {
	switch pt {
	case King:
		return "K"
	case Gold:
		return "G"
	case Silver:
		return "S"
	case Pawn:
		return "P"
	default:
		return "?"
	}
}

func formatMove(m Move) string {
	if m.Drop != nil {
		return fmt.Sprintf("%s@%s", pieceTypeCode(*m.Drop), coordToString(m.To))
	}
	from := coordToString(*m.From)
	to := coordToString(m.To)
	suffix := ""
	if m.Promote {
		suffix = "+"
	}
	return from + to + suffix
}

func coordToString(c Coord) string {
	return fmt.Sprintf("%c%d", 'a'+c.X, c.Y+1)
}

func parseMove(input string) (Move, error) {
	s := strings.TrimSpace(strings.ToLower(input))
	s = strings.ReplaceAll(s, " ", "")
	if s == "" {
		return Move{}, errors.New("empty input")
	}

	if strings.Contains(s, "@") {
		parts := strings.Split(s, "@")
		if len(parts) != 2 || len(parts[0]) != 1 || len(parts[1]) != 2 {
			return Move{}, errors.New("drop format P@a3")
		}
		pieceChar := strings.ToUpper(parts[0])
		pKind, ok := parsePieceChar(pieceChar)
		if !ok {
			return Move{}, errors.New("unknown piece for drop")
		}
		to, err := parseCoord(parts[1])
		if err != nil {
			return Move{}, err
		}
		return Move{Drop: &pKind, To: to}, nil
	}

	promote := false
	if strings.HasSuffix(s, "+") {
		promote = true
		s = strings.TrimSuffix(s, "+")
	}
	if len(s) != 4 {
		return Move{}, errors.New("move format a1a2 or a1a2+")
	}
	from, err := parseCoord(s[:2])
	if err != nil {
		return Move{}, err
	}
	to, err := parseCoord(s[2:])
	if err != nil {
		return Move{}, err
	}
	return Move{From: &from, To: to, Promote: promote}, nil
}

func parsePieceChar(ch string) (PieceType, bool) {
	switch strings.ToUpper(ch) {
	case "K":
		return King, true
	case "G":
		return Gold, true
	case "S":
		return Silver, true
	case "P":
		return Pawn, true
	default:
		return King, false
	}
}

func parseCoord(token string) (Coord, error) {
	if len(token) != 2 {
		return Coord{}, errors.New("coord format a1")
	}
	colRune := token[0]
	rowRune := token[1]

	if colRune < 'a' || colRune > 'e' {
		return Coord{}, errors.New("column must be a-e")
	}
	if rowRune < '1' || rowRune > '6' {
		return Coord{}, errors.New("row must be 1-6")
	}
	x := int(colRune - 'a')
	y := int(rowRune - '1') // rows are bottom (1) to top (6)
	return Coord{X: x, Y: y}, nil
}

func tryApplyMove(state GameState, move Move) (bool, GameState) {
	legalMoves := generateLegalMoves(state, state.Turn)
	for _, m := range legalMoves {
		if movesEqual(m, move) {
			next := cloneState(state)
			applyMove(&next, m)
			return true, next
		}
	}
	return false, state
}

func movesEqual(a, b Move) bool {
	if (a.Drop == nil) != (b.Drop == nil) {
		return false
	}
	if a.Drop != nil && b.Drop != nil {
		return *a.Drop == *b.Drop && a.To == b.To
	}
	if (a.From == nil) != (b.From == nil) {
		return false
	}
	if a.From == nil || b.From == nil {
		return false
	}
	return *a.From == *b.From && a.To == b.To && a.Promote == b.Promote
}

func cloneState(state GameState) GameState {
	next := state
	next.Hands = [2]map[PieceType]int{
		Bottom: make(map[PieceType]int),
		Top:    make(map[PieceType]int),
	}
	for player := 0; player < 2; player++ {
		for k, v := range state.Hands[player] {
			next.Hands[player][k] = v
		}
	}
	return next
}

func applyMove(state *GameState, move Move) {
	player := state.Turn
	if move.Drop != nil {
		state.Hands[player][*move.Drop]--
		state.Board[move.To.Y][move.To.X] = Piece{Kind: *move.Drop, Owner: player, Present: true}
		return
	}

	fromPiece := state.Board[move.From.Y][move.From.X]
	if target := state.Board[move.To.Y][move.To.X]; target.Present {
		state.Hands[player][target.Kind]++
	}

	fromPiece.Present = false
	state.Board[move.From.Y][move.From.X] = Piece{}

	if move.Promote {
		fromPiece.Promoted = true
	}
	fromPiece.Present = true
	state.Board[move.To.Y][move.To.X] = fromPiece
}

func generateLegalMoves(state GameState, player Player) []Move {
	var moves []Move
	for y := 0; y < boardRows; y++ {
		for x := 0; x < boardCols; x++ {
			piece := state.Board[y][x]
			if !piece.Present || piece.Owner != player {
				continue
			}
			from := Coord{X: x, Y: y}
			for _, delta := range movementOffsets(piece) {
				to := Coord{X: x + delta.X, Y: y + delta.Y}
				if !insideBoard(to) {
					continue
				}
				dest := state.Board[to.Y][to.X]
				if dest.Present && dest.Owner == player {
					continue
				}

				canPromote := canPromote(piece, to.Y)
				promoteOptions := []bool{false}
				if canPromote {
					promoteOptions = append(promoteOptions, true)
				}

				for _, promote := range promoteOptions {
					testMove := Move{From: &from, To: to, Promote: promote}
					testState := cloneState(state)
					applyMove(&testState, testMove)
					if !inCheck(testState, player) {
						moves = append(moves, testMove)
					}
				}
			}
		}
	}

	for dropType, count := range state.Hands[player] {
		if count == 0 {
			continue
		}
		pieceKind := dropType
		for y := 0; y < boardRows; y++ {
			for x := 0; x < boardCols; x++ {
				if state.Board[y][x].Present {
					continue
				}
				testMove := Move{Drop: &pieceKind, To: Coord{X: x, Y: y}}
				testState := cloneState(state)
				applyMove(&testState, testMove)
				if !inCheck(testState, player) {
					moves = append(moves, testMove)
				}
			}
		}
	}
	return moves
}

func insideBoard(c Coord) bool {
	return c.X >= 0 && c.X < boardCols && c.Y >= 0 && c.Y < boardRows
}

func movementOffsets(p Piece) []Coord {
	// Gold movement is used for promoted pawns and silvers.
	forward := 1
	if p.Owner == Top {
		forward = -1
	}

	switch {
	case p.Kind == King:
		return []Coord{
			{X: -1, Y: -1}, {X: 0, Y: -1}, {X: 1, Y: -1},
			{X: -1, Y: 0}, {X: 1, Y: 0},
			{X: -1, Y: 1}, {X: 0, Y: 1}, {X: 1, Y: 1},
		}
	case p.Kind == Gold || p.Promoted:
		return []Coord{
			{X: -1, Y: forward}, {X: 0, Y: forward}, {X: 1, Y: forward},
			{X: -1, Y: 0}, {X: 1, Y: 0},
			{X: 0, Y: -forward},
		}
	case p.Kind == Silver:
		return []Coord{
			{X: -1, Y: forward}, {X: 0, Y: forward}, {X: 1, Y: forward},
			{X: -1, Y: -forward}, {X: 1, Y: -forward},
		}
	case p.Kind == Pawn:
		return []Coord{{X: 0, Y: forward}}
	default:
		return nil
	}
}

func canPromote(p Piece, destY int) bool {
	if p.Promoted {
		return false
	}
	if p.Kind != Silver && p.Kind != Pawn {
		return false
	}
	if p.Owner == Bottom {
		return destY >= boardRows-2
	}
	return destY <= 1
}

func inCheck(state GameState, player Player) bool {
	kingPos, found := findKing(state, player)
	if !found {
		return false
	}
	opponent := player.opponent()

	for y := 0; y < boardRows; y++ {
		for x := 0; x < boardCols; x++ {
			p := state.Board[y][x]
			if !p.Present || p.Owner != opponent {
				continue
			}
			from := Coord{X: x, Y: y}
			for _, delta := range movementOffsets(p) {
				to := Coord{X: from.X + delta.X, Y: from.Y + delta.Y}
				if !insideBoard(to) {
					continue
				}
				if to == kingPos {
					return true
				}
			}
		}
	}
	return false
}

func findKing(state GameState, player Player) (Coord, bool) {
	for y := 0; y < boardRows; y++ {
		for x := 0; x < boardCols; x++ {
			p := state.Board[y][x]
			if p.Present && p.Owner == player && p.Kind == King {
				return Coord{X: x, Y: y}, true
			}
		}
	}
	return Coord{}, false
}

func isCheckmate(state GameState, player Player) bool {
	if !inCheck(state, player) {
		return false
	}
	return len(generateLegalMoves(state, player)) == 0
}
