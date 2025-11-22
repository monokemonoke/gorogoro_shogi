package game

import (
	"errors"
	"fmt"
	"strings"
)

const (
	BoardRows = 6
	BoardCols = 5
)

type Player int

const (
	Bottom Player = iota
	Top
)

func (p Player) Opponent() Player {
	if p == Bottom {
		return Top
	}
	return Bottom
}

func (p Player) Label() string {
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
	Board [BoardRows][BoardCols]Piece
	Hands [2]map[PieceType]int
	Turn  Player
}

func NewGame() GameState {
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

func PieceTypeCode(pt PieceType) string {
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

func FormatMove(m Move) string {
	if m.Drop != nil {
		return fmt.Sprintf("%s@%s", PieceTypeCode(*m.Drop), CoordToString(m.To))
	}
	from := CoordToString(*m.From)
	to := CoordToString(m.To)
	suffix := ""
	if m.Promote {
		suffix = "+"
	}
	return from + to + suffix
}

func CoordToString(c Coord) string {
	return fmt.Sprintf("%c%d", 'a'+c.X, c.Y+1)
}

func ParseMove(input string) (Move, error) {
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
		pKind, ok := ParsePieceChar(pieceChar)
		if !ok {
			return Move{}, errors.New("unknown piece for drop")
		}
		to, err := ParseCoord(parts[1])
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
	from, err := ParseCoord(s[:2])
	if err != nil {
		return Move{}, err
	}
	to, err := ParseCoord(s[2:])
	if err != nil {
		return Move{}, err
	}
	return Move{From: &from, To: to, Promote: promote}, nil
}

func ParsePieceChar(ch string) (PieceType, bool) {
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

func ParseCoord(token string) (Coord, error) {
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

func TryApplyMove(state GameState, move Move) (bool, GameState) {
	legalMoves := GenerateLegalMoves(state, state.Turn)
	for _, m := range legalMoves {
		if movesEqual(m, move) {
			next := CloneState(state)
			ApplyMove(&next, m)
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

func CloneState(state GameState) GameState {
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

func ApplyMove(state *GameState, move Move) {
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

type handDelta struct {
	player Player
	piece  PieceType
	delta  int
}

type moveDiff struct {
	hasFrom    bool
	from       Coord
	fromBefore Piece
	to         Coord
	toBefore   Piece
	handChange handDelta
	hasHand    bool
}

// applyMoveInPlace mutates the board and hands directly and returns the diff for undoMove.
func applyMoveInPlace(state *GameState, move Move, player Player) moveDiff {
	diff := moveDiff{
		to:       move.To,
		toBefore: state.Board[move.To.Y][move.To.X],
	}

	if move.Drop != nil {
		diff.handChange = handDelta{player: player, piece: *move.Drop, delta: -1}
		diff.hasHand = true
		state.Hands[player][*move.Drop]--
		state.Board[move.To.Y][move.To.X] = Piece{Kind: *move.Drop, Owner: player, Present: true}
		return diff
	}

	if move.From == nil {
		return diff
	}

	from := *move.From
	diff.hasFrom = true
	diff.from = from
	diff.fromBefore = state.Board[from.Y][from.X]

	movingPiece := diff.fromBefore
	state.Board[from.Y][from.X] = Piece{}

	if diff.toBefore.Present {
		diff.handChange = handDelta{player: player, piece: diff.toBefore.Kind, delta: 1}
		diff.hasHand = true
		state.Hands[player][diff.toBefore.Kind]++
	}

	if move.Promote {
		movingPiece.Promoted = true
	}
	movingPiece.Present = true
	state.Board[move.To.Y][move.To.X] = movingPiece
	return diff
}

func undoMove(state *GameState, diff moveDiff) {
	state.Board[diff.to.Y][diff.to.X] = diff.toBefore
	if diff.hasFrom {
		state.Board[diff.from.Y][diff.from.X] = diff.fromBefore
	}
	if diff.hasHand {
		change := diff.handChange
		state.Hands[change.player][change.piece] -= change.delta
	}
}

func GenerateLegalMoves(state GameState, player Player) []Move {
	statePtr := &state
	kingPos, kingFound := findKing(state, player)
	moves := make([]Move, 0, 48)
	for y := 0; y < BoardRows; y++ {
		for x := 0; x < BoardCols; x++ {
			piece := state.Board[y][x]
			if !piece.Present || piece.Owner != player {
				continue
			}
			from := Coord{X: x, Y: y}
			moves = appendLegalMovesForPiece(statePtr, from, piece, kingPos, kingFound, moves)
		}
	}

	for dropType := range state.Hands[player] {
		moves = appendLegalDrops(statePtr, player, dropType, kingPos, kingFound, moves)
	}
	return moves
}

func GenerateLegalMovesFrom(state GameState, player Player, from Coord) []Move {
	if !insideBoard(from) {
		return nil
	}
	piece := state.Board[from.Y][from.X]
	if !piece.Present || piece.Owner != player {
		return nil
	}
	kingPos, kingFound := findKing(state, player)
	return appendLegalMovesForPiece(&state, from, piece, kingPos, kingFound, nil)
}

func GenerateLegalDrops(state GameState, player Player, pieceKind PieceType) []Move {
	if state.Hands[player][pieceKind] == 0 {
		return nil
	}
	kingPos, kingFound := findKing(state, player)
	return appendLegalDrops(&state, player, pieceKind, kingPos, kingFound, nil)
}

func appendLegalMovesForPiece(state *GameState, from Coord, piece Piece, kingPos Coord, kingFound bool, moves []Move) []Move {
	player := piece.Owner
	for _, delta := range movementOffsets(piece) {
		to := Coord{X: from.X + delta.X, Y: from.Y + delta.Y}
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
			diff := applyMoveInPlace(state, testMove, player)
			inCheck := playerInCheckAfterAppliedMove(state, player, diff, kingPos, kingFound)
			if !inCheck && pieceHasBoardReach(state.Board[to.Y][to.X], to) {
				moves = append(moves, testMove)
			}
			undoMove(state, diff)
		}
	}
	return moves
}

func appendLegalDrops(state *GameState, player Player, pieceKind PieceType, kingPos Coord, kingFound bool, moves []Move) []Move {
	// Pawn drops are blocked on files that already contain an unpromoted pawn of the same player (nifu).
	var blockedColumns [BoardCols]bool
	if pieceKind == Pawn {
		for x := 0; x < BoardCols; x++ {
			blockedColumns[x] = columnHasUnpromotedPawn(state, player, x)
		}
	}
	for y := 0; y < BoardRows; y++ {
		for x := 0; x < BoardCols; x++ {
			if state.Board[y][x].Present || blockedColumns[x] {
				continue
			}
			testMove := Move{Drop: &pieceKind, To: Coord{X: x, Y: y}}
			diff := applyMoveInPlace(state, testMove, player)
			inCheck := playerInCheckAfterAppliedMove(state, player, diff, kingPos, kingFound)
			if !inCheck && pieceHasBoardReach(state.Board[y][x], testMove.To) {
				moves = append(moves, testMove)
			}
			undoMove(state, diff)
		}
	}
	return moves
}

// pieceHasBoardReach ensures the piece still has at least one theoretical destination on the board.
func pieceHasBoardReach(piece Piece, at Coord) bool {
	if !piece.Present {
		return false
	}
	for _, delta := range movementOffsets(piece) {
		target := Coord{X: at.X + delta.X, Y: at.Y + delta.Y}
		if insideBoard(target) {
			return true
		}
	}
	return false
}

func columnHasUnpromotedPawn(state *GameState, player Player, column int) bool {
	for y := 0; y < BoardRows; y++ {
		p := state.Board[y][column]
		if !p.Present {
			continue
		}
		if p.Owner == player && p.Kind == Pawn && !p.Promoted {
			return true
		}
	}
	return false
}

func insideBoard(c Coord) bool {
	return c.X >= 0 && c.X < BoardCols && c.Y >= 0 && c.Y < BoardRows
}

var (
	kingOffsets = []Coord{
		{X: -1, Y: -1}, {X: 0, Y: -1}, {X: 1, Y: -1},
		{X: -1, Y: 0}, {X: 1, Y: 0},
		{X: -1, Y: 1}, {X: 0, Y: 1}, {X: 1, Y: 1},
	}
	goldOffsetsBottom = []Coord{
		{X: -1, Y: 1}, {X: 0, Y: 1}, {X: 1, Y: 1},
		{X: -1, Y: 0}, {X: 1, Y: 0},
		{X: 0, Y: -1},
	}
	goldOffsetsTop = []Coord{
		{X: -1, Y: -1}, {X: 0, Y: -1}, {X: 1, Y: -1},
		{X: -1, Y: 0}, {X: 1, Y: 0},
		{X: 0, Y: 1},
	}
	silverOffsetsBottom = []Coord{
		{X: -1, Y: 1}, {X: 0, Y: 1}, {X: 1, Y: 1},
		{X: -1, Y: -1}, {X: 1, Y: -1},
	}
	silverOffsetsTop = []Coord{
		{X: -1, Y: -1}, {X: 0, Y: -1}, {X: 1, Y: -1},
		{X: -1, Y: 1}, {X: 1, Y: 1},
	}
	pawnOffsetsBottom = []Coord{{X: 0, Y: 1}}
	pawnOffsetsTop    = []Coord{{X: 0, Y: -1}}
)

func movementOffsets(p Piece) []Coord {
	if p.Kind == King {
		return kingOffsets
	}
	if p.Kind == Gold || p.Promoted {
		if p.Owner == Bottom {
			return goldOffsetsBottom
		}
		return goldOffsetsTop
	}
	if p.Kind == Silver {
		if p.Owner == Bottom {
			return silverOffsetsBottom
		}
		return silverOffsetsTop
	}
	if p.Kind == Pawn {
		if p.Owner == Bottom {
			return pawnOffsetsBottom
		}
		return pawnOffsetsTop
	}
	return nil
}

func canPromote(p Piece, destY int) bool {
	if p.Promoted {
		return false
	}
	if p.Kind != Silver && p.Kind != Pawn {
		return false
	}
	zoneMin, zoneMax := promotionZoneFor(p.Owner)
	return destY >= zoneMin && destY <= zoneMax
}

// promotionZoneFor returns inclusive Y bounds for the opponent's first two ranks.
func promotionZoneFor(player Player) (minY, maxY int) {
	if player == Bottom {
		return BoardRows - 2, BoardRows - 1
	}
	return 0, 1
}

func playerInCheckAfterAppliedMove(state *GameState, player Player, diff moveDiff, kingPos Coord, kingFound bool) bool {
	nextKingPos, nextKingFound := kingPositionAfterDiff(player, diff, kingPos, kingFound)
	if !nextKingFound {
		return false
	}
	return isKingThreatened(&state.Board, player, nextKingPos)
}

func kingPositionAfterDiff(player Player, diff moveDiff, kingPos Coord, kingFound bool) (Coord, bool) {
	if diff.hasFrom && diff.fromBefore.Owner == player && diff.fromBefore.Kind == King {
		return diff.to, true
	}
	return kingPos, kingFound
}

func InCheck(state GameState, player Player) bool {
	kingPos, found := findKing(state, player)
	if !found {
		return false
	}
	return isKingThreatened(&state.Board, player, kingPos)
}

func isKingThreatened(board *[BoardRows][BoardCols]Piece, player Player, kingPos Coord) bool {
	opponent := player.Opponent()

	for y := 0; y < BoardRows; y++ {
		for x := 0; x < BoardCols; x++ {
			p := board[y][x]
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
	for y := 0; y < BoardRows; y++ {
		for x := 0; x < BoardCols; x++ {
			p := state.Board[y][x]
			if p.Present && p.Owner == player && p.Kind == King {
				return Coord{X: x, Y: y}, true
			}
		}
	}
	return Coord{}, false
}

// CheckmateStatus reports whether the side to move is checkmated and returns the winner if so.
// The winner value is undefined when the first return value is false.
func CheckmateStatus(state GameState) (bool, Player) {
	if !IsCheckmate(state, state.Turn) {
		return false, Bottom
	}
	return true, state.Turn.Opponent()
}

func IsCheckmate(state GameState, player Player) bool {
	if !InCheck(state, player) {
		return false
	}
	return len(GenerateLegalMoves(state, player)) == 0
}

// MateSearch performs a minimax search limited by depth (in plies) to detect a forced mate.
// It returns the winning line starting from the current state if the attacker can force mate.
func MateSearch(state GameState, attacker Player, depth int) (bool, []Move) {
	if depth <= 0 {
		return false, nil
	}
	defender := attacker.Opponent()
	return mateSearch(state, attacker, defender, depth)
}

func mateSearch(state GameState, attacker, defender Player, depth int) (bool, []Move) {
	if depth == 0 {
		return false, nil
	}

	player := state.Turn
	moves := GenerateLegalMoves(state, player)

	if len(moves) == 0 {
		if player == defender && InCheck(state, defender) {
			return true, nil
		}
		return false, nil
	}

	if player == attacker {
		for _, mv := range moves {
			next := CloneState(state)
			ApplyMove(&next, mv)
			next.Turn = player.Opponent()

			if IsCheckmate(next, defender) {
				return true, []Move{mv}
			}
			found, line := mateSearch(next, attacker, defender, depth-1)
			if found {
				return true, append([]Move{mv}, line...)
			}
		}
		return false, nil
	}

	var forcedLine []Move
	for _, mv := range moves {
		next := CloneState(state)
		ApplyMove(&next, mv)
		next.Turn = player.Opponent()

		found, line := mateSearch(next, attacker, defender, depth-1)
		if !found {
			return false, nil
		}
		if forcedLine == nil {
			forcedLine = append([]Move{mv}, line...)
		}
	}
	if forcedLine == nil {
		forcedLine = []Move{}
	}
	return true, forcedLine
}
