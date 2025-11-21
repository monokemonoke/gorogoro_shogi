package game

import (
	"errors"
	"strconv"
	"strings"
)

// AlphaBetaEngine performs a depth-limited minimax search with alpha-beta pruning.
type AlphaBetaEngine struct {
	Depth int
	table map[stateKey]ttEntry
}

func NewAlphaBetaEngine(depth int) *AlphaBetaEngine {
	return &AlphaBetaEngine{
		Depth: depth,
		table: make(map[stateKey]ttEntry),
	}
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
	checkmateScore = 100000
	infiniteScore  = 1_000_000_000
)

func (e *AlphaBetaEngine) NextMove(state GameState) (Move, error) {
	moves := GenerateLegalMoves(state, state.Turn)
	if len(moves) == 0 {
		return Move{}, errors.New("no legal moves to play")
	}
	if e.table == nil {
		e.table = make(map[stateKey]ttEntry)
	}
	_, best := e.search(state, e.Depth, -infiniteScore, infiniteScore, state.Turn)
	if best == nil {
		return Move{}, errors.New("failed to find a move")
	}
	return *best, nil
}

func (e *AlphaBetaEngine) search(state GameState, depth int, alpha, beta int, maximizer Player) (int, *Move) {
	alphaOrig, betaOrig := alpha, beta
	key := makeStateKey(state, maximizer)
	if entry, ok := e.table[key]; ok && entry.depth >= depth {
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
		e.table[key] = ttEntry{depth: depth, score: score, bound: boundExact}
		return score, nil
	}

	legal := GenerateLegalMoves(state, state.Turn)
	if len(legal) == 0 {
		score := e.evaluate(state, maximizer, depth)
		e.table[key] = ttEntry{depth: depth, score: score, bound: boundExact}
		return score, nil
	}

	var chosen *Move
	if state.Turn == maximizer {
		bestScore := -infiniteScore
		for _, mv := range legal {
			next := CloneState(state)
			ApplyMove(&next, mv)
			next.Turn = next.Turn.Opponent()

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
		bound := determineBound(bestScore, alphaOrig, betaOrig)
		e.table[key] = makeEntry(bestScore, depth, bound, chosen)
		return bestScore, chosen
	}

	bestScore := infiniteScore
	for _, mv := range legal {
		next := CloneState(state)
		ApplyMove(&next, mv)
		next.Turn = next.Turn.Opponent()

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
	bound := determineBound(bestScore, alphaOrig, betaOrig)
	e.table[key] = makeEntry(bestScore, depth, bound, chosen)
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
