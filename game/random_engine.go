package game

import (
	"errors"
	"math/rand"
)

// RandomEngine picks a legal move uniformly at random.
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
