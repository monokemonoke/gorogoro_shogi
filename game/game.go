package game

import (
	"errors"
	"fmt"
	"math"
	"math/rand"
	"strconv"
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

type RandomEngine struct {
	rng *rand.Rand
}

func NewRandomEngine(seed int64) *RandomEngine {
	return &RandomEngine{rng: rand.New(rand.NewSource(seed))}
}

func (e *RandomEngine) NextMove(state GameState) (Move, error) {
	moves := GenerateLegalMoves(state, state.Turn)
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

type boundType int

const (
	boundExact boundType = iota
	boundLower
	boundUpper
)

type ttEntry struct {
	depth   int
	score   int
	move    Move
	hasMove bool
	bound   boundType
}

type stateKey struct {
	boardKey  string
	turn      Player
	maximizer Player
}

const (
	checkmateScore         = 100000
	infiniteScore          = 1_000_000_000
	defaultMCTSIterations  = 800
	defaultMCTSExploration = 1.2
	mctsRolloutDepth       = 60
)

var pieceScores = map[PieceType]int{
	King:   1000,
	Gold:   70,
	Silver: 50,
	Pawn:   10,
}

var orderedPieceTypes = []PieceType{King, Gold, Silver, Pawn}

func (e *AlphaBetaEngine) NextMove(state GameState) (Move, error) {
	moves := GenerateLegalMoves(state, state.Turn)
	if len(moves) == 0 {
		return Move{}, errors.New("no legal moves to play")
	}
	table := make(map[stateKey]ttEntry)
	_, best := e.search(state, e.Depth, -infiniteScore, infiniteScore, state.Turn, table)
	if best == nil {
		return Move{}, errors.New("failed to find a move")
	}
	return *best, nil
}

func (e *AlphaBetaEngine) search(state GameState, depth int, alpha, beta int, maximizer Player, table map[stateKey]ttEntry) (int, *Move) {
	alphaOrig, betaOrig := alpha, beta
	key := makeStateKey(state, maximizer)
	if entry, ok := table[key]; ok && entry.depth >= depth {
		switch entry.bound {
		case boundExact:
			return entry.score, duplicateEntryMove(entry)
		case boundLower:
			if entry.score > alpha {
				alpha = entry.score
			}
		case boundUpper:
			if entry.score < beta {
				beta = entry.score
			}
		}
		if alpha >= beta {
			return entry.score, duplicateEntryMove(entry)
		}
	}

	if depth == 0 {
		score := e.evaluate(state, maximizer, depth)
		table[key] = ttEntry{depth: depth, score: score, bound: boundExact}
		return score, nil
	}

	legal := GenerateLegalMoves(state, state.Turn)
	if len(legal) == 0 {
		score := e.evaluate(state, maximizer, depth)
		table[key] = ttEntry{depth: depth, score: score, bound: boundExact}
		return score, nil
	}

	var chosen *Move
	if state.Turn == maximizer {
		bestScore := -infiniteScore
		for _, mv := range legal {
			next := CloneState(state)
			ApplyMove(&next, mv)
			next.Turn = next.Turn.Opponent()

			score, _ := e.search(next, depth-1, alpha, beta, maximizer, table)
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
		bound := determineBound(bestScore, alphaOrig, betaOrig)
		table[key] = makeEntry(bestScore, depth, bound, chosen)
		return bestScore, chosen
	}

	bestScore := infiniteScore
	for _, mv := range legal {
		next := CloneState(state)
		ApplyMove(&next, mv)
		next.Turn = next.Turn.Opponent()

		score, _ := e.search(next, depth-1, alpha, beta, maximizer, table)
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
	bound := determineBound(bestScore, alphaOrig, betaOrig)
	table[key] = makeEntry(bestScore, depth, bound, chosen)
	return bestScore, chosen
}

func determineBound(score, alphaOrig, betaOrig int) boundType {
	switch {
	case score <= alphaOrig:
		return boundUpper
	case score >= betaOrig:
		return boundLower
	default:
		return boundExact
	}
}

func makeEntry(score, depth int, bound boundType, chosen *Move) ttEntry {
	entry := ttEntry{depth: depth, score: score, bound: bound}
	if chosen != nil {
		entry.hasMove = true
		entry.move = *chosen
	}
	return entry
}

func duplicateEntryMove(entry ttEntry) *Move {
	if !entry.hasMove {
		return nil
	}
	mv := entry.move
	return &mv
}

func makeStateKey(state GameState, maximizer Player) stateKey {
	return stateKey{
		boardKey:  encodeState(state),
		turn:      state.Turn,
		maximizer: maximizer,
	}
}

func encodeState(state GameState) string {
	var b strings.Builder
	for y := 0; y < BoardRows; y++ {
		for x := 0; x < BoardCols; x++ {
			p := state.Board[y][x]
			if !p.Present {
				b.WriteByte('.')
				continue
			}
			b.WriteByte(byte('0' + p.Kind))
			b.WriteByte(byte('0' + p.Owner))
			if p.Promoted {
				b.WriteByte('1')
			} else {
				b.WriteByte('0')
			}
		}
		b.WriteByte('/')
	}
	b.WriteByte('|')
	for _, player := range []Player{Bottom, Top} {
		for _, pt := range orderedPieceTypes {
			count := state.Hands[player][pt]
			b.WriteByte(byte('0' + int(player)))
			b.WriteByte(byte('0' + int(pt)))
			b.WriteString(strconv.Itoa(count))
			b.WriteByte(',')
		}
	}
	return b.String()
}

func (e *AlphaBetaEngine) evaluate(state GameState, maximizer Player, depth int) int {
	if IsCheckmate(state, maximizer) {
		return -checkmateScore - depth
	}
	if IsCheckmate(state, maximizer.Opponent()) {
		return checkmateScore + depth
	}

	score := 0
	for y := 0; y < BoardRows; y++ {
		for x := 0; x < BoardCols; x++ {
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
	for pType, count := range state.Hands[maximizer.Opponent()] {
		score -= pieceScores[pType] * count
	}

	if InCheck(state, maximizer) {
		score -= 5
	}
	if InCheck(state, maximizer.Opponent()) {
		score += 5
	}
	return score
}

type MCTSEngine struct {
	iterations  int
	exploration float64
	rng         *rand.Rand
}

func NewMCTSEngine(iterations int, seed int64) *MCTSEngine {
	if iterations <= 0 {
		iterations = defaultMCTSIterations
	}
	return &MCTSEngine{
		iterations:  iterations,
		exploration: defaultMCTSExploration,
		rng:         rand.New(rand.NewSource(seed)),
	}
}

func (e *MCTSEngine) NextMove(state GameState) (Move, error) {
	legal := GenerateLegalMoves(state, state.Turn)
	if len(legal) == 0 {
		return Move{}, errors.New("no legal moves to play")
	}
	root := newMCTSNode(CloneState(state), nil, nil)
	rootPlayer := state.Turn
	for i := 0; i < e.iterations; i++ {
		node := root
		for len(node.untried) == 0 && len(node.children) > 0 {
			node = node.selectChild(e.exploration)
		}
		if len(node.untried) > 0 {
			node = node.expand(e.rng)
		}
		winner, decided := e.rollout(node.state, rootPlayer)
		node.backpropagate(winner, rootPlayer, decided)
	}
	best := root.bestChildByVisits()
	if best == nil || best.move == nil {
		return Move{}, errors.New("failed to choose move")
	}
	return *best.move, nil
}

type mctsNode struct {
	state    GameState
	move     *Move
	parent   *mctsNode
	children []*mctsNode
	untried  []Move
	visits   int
	wins     float64
}

func newMCTSNode(state GameState, move *Move, parent *mctsNode) *mctsNode {
	available := GenerateLegalMoves(state, state.Turn)
	untried := make([]Move, len(available))
	copy(untried, available)
	return &mctsNode{
		state:   state,
		move:    move,
		parent:  parent,
		untried: untried,
	}
}

func (n *mctsNode) selectChild(exploration float64) *mctsNode {
	parentVisits := math.Max(1, float64(n.visits))
	bestScore := math.Inf(-1)
	var chosen *mctsNode
	for _, child := range n.children {
		if child.visits == 0 {
			return child
		}
		exploit := child.winsRatio()
		explore := exploration * math.Sqrt(math.Log(parentVisits)/float64(child.visits))
		score := exploit + explore
		if score > bestScore {
			bestScore = score
			chosen = child
		}
	}
	return chosen
}

func (n *mctsNode) expand(rng *rand.Rand) *mctsNode {
	if len(n.untried) == 0 {
		return n
	}
	idx := rng.Intn(len(n.untried))
	mv := n.untried[idx]
	n.untried[idx] = n.untried[len(n.untried)-1]
	n.untried = n.untried[:len(n.untried)-1]
	childState := CloneState(n.state)
	ApplyMove(&childState, mv)
	childState.Turn = childState.Turn.Opponent()
	mvCopy := mv
	child := newMCTSNode(childState, &mvCopy, n)
	n.children = append(n.children, child)
	return child
}

func (n *mctsNode) winsRatio() float64 {
	if n.visits == 0 {
		return 0
	}
	return n.wins / float64(n.visits)
}

func (n *mctsNode) bestChildByVisits() *mctsNode {
	var best *mctsNode
	bestVisits := -1
	for _, child := range n.children {
		if child.visits > bestVisits {
			best = child
			bestVisits = child.visits
		}
	}
	return best
}

func (n *mctsNode) backpropagate(winner Player, root Player, decided bool) {
	reward := 0.5
	if decided {
		if winner == root {
			reward = 1
		} else if winner == root.Opponent() {
			reward = 0
		}
	}
	for node := n; node != nil; node = node.parent {
		node.visits++
		node.wins += reward
	}
}

func (e *MCTSEngine) rollout(state GameState, root Player) (Player, bool) {
	sim := CloneState(state)
	for depth := 0; depth < mctsRolloutDepth; depth++ {
		moves := GenerateLegalMoves(sim, sim.Turn)
		if len(moves) == 0 {
			if InCheck(sim, sim.Turn) {
				return sim.Turn.Opponent(), true
			}
			return root, false
		}
		mv := moves[e.rng.Intn(len(moves))]
		ApplyMove(&sim, mv)
		sim.Turn = sim.Turn.Opponent()
	}
	score := materialBalance(sim, root)
	switch {
	case score > 0:
		return root, true
	case score < 0:
		return root.Opponent(), true
	default:
		return root, false
	}
}

func materialBalance(state GameState, player Player) int {
	score := 0
	for y := 0; y < BoardRows; y++ {
		for x := 0; x < BoardCols; x++ {
			p := state.Board[y][x]
			if !p.Present {
				continue
			}
			value := pieceValue(p)
			if p.Owner == player {
				score += value
			} else {
				score -= value
			}
		}
	}
	for pieceType, count := range state.Hands[player] {
		score += pieceScores[pieceType] * count
	}
	for pieceType, count := range state.Hands[player.Opponent()] {
		score -= pieceScores[pieceType] * count
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

func GenerateLegalMoves(state GameState, player Player) []Move {
	var moves []Move
	for y := 0; y < BoardRows; y++ {
		for x := 0; x < BoardCols; x++ {
			from := Coord{X: x, Y: y}
			moves = append(moves, GenerateLegalMovesFrom(state, player, from)...)
		}
	}

	for dropType := range state.Hands[player] {
		moves = append(moves, GenerateLegalDrops(state, player, dropType)...)
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
	return legalMovesForPiece(state, from, piece)
}

func GenerateLegalDrops(state GameState, player Player, pieceKind PieceType) []Move {
	if state.Hands[player][pieceKind] == 0 {
		return nil
	}
	return legalDropsForPiece(state, player, pieceKind)
}

func legalMovesForPiece(state GameState, from Coord, piece Piece) []Move {
	player := piece.Owner
	var moves []Move
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
			testState := CloneState(state)
			ApplyMove(&testState, testMove)
			if !InCheck(testState, player) && pieceHasBoardReach(testState.Board[to.Y][to.X], to) {
				moves = append(moves, testMove)
			}
		}
	}
	return moves
}

func legalDropsForPiece(state GameState, player Player, pieceKind PieceType) []Move {
	// Pawn drops are blocked on files that already contain an unpromoted pawn of the same player (nifu).
	blockedColumns := map[int]bool{}
	if pieceKind == Pawn {
		for x := 0; x < BoardCols; x++ {
			blockedColumns[x] = columnHasUnpromotedPawn(state, player, x)
		}
	}
	var moves []Move
	for y := 0; y < BoardRows; y++ {
		for x := 0; x < BoardCols; x++ {
			if state.Board[y][x].Present {
				continue
			}
			if blockedColumns[x] {
				continue
			}
			testMove := Move{Drop: &pieceKind, To: Coord{X: x, Y: y}}
			testState := CloneState(state)
			ApplyMove(&testState, testMove)
			if !InCheck(testState, player) && pieceHasBoardReach(testState.Board[y][x], testMove.To) {
				moves = append(moves, testMove)
			}
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

func columnHasUnpromotedPawn(state GameState, player Player, column int) bool {
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

func InCheck(state GameState, player Player) bool {
	kingPos, found := findKing(state, player)
	if !found {
		return false
	}
	opponent := player.Opponent()

	for y := 0; y < BoardRows; y++ {
		for x := 0; x < BoardCols; x++ {
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
