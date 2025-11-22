package game

import (
	"fmt"
	"testing"
)

func BenchmarkTDUCBEngineStatesPerSecond(b *testing.B) {
	type scenario struct {
		name  string
		setup func() GameState
	}
	type config struct {
		name        string
		simulations int
		depth       int
	}

	scenarios := []scenario{
		{
			name: "opening",
			setup: func() GameState {
				return NewGame()
			},
		},
		{
			name: "tactical_scramble",
			setup: func() GameState {
				state := newEmptyState(Bottom)
				state.Board[0][2] = Piece{Kind: King, Owner: Bottom, Present: true}
				state.Board[5][2] = Piece{Kind: King, Owner: Top, Present: true}
				state.Board[1][1] = Piece{Kind: Gold, Owner: Bottom, Present: true}
				state.Board[1][3] = Piece{Kind: Silver, Owner: Bottom, Promoted: true, Present: true}
				state.Board[2][2] = Piece{Kind: Pawn, Owner: Bottom, Promoted: true, Present: true}
				state.Board[3][1] = Piece{Kind: Silver, Owner: Top, Present: true}
				state.Board[3][3] = Piece{Kind: Gold, Owner: Top, Present: true}
				state.Board[4][2] = Piece{Kind: Pawn, Owner: Top, Present: true}
				state.Hands[Bottom][Pawn] = 1
				state.Hands[Top][Pawn] = 2
				state.Hands[Top][Silver] = 1
				return state
			},
		},
	}

	configs := []config{
		{name: "fast", simulations: 150, depth: 24},
		{name: "default", simulations: defaultTDSimulations, depth: defaultTDRollout},
	}

	for _, sc := range scenarios {
		for _, cfg := range configs {
			b.Run(fmt.Sprintf("%s/%s", sc.name, cfg.name), func(b *testing.B) {
				state := sc.setup()
				engine := NewTDUCBEngine(1)
				engine.simulations = cfg.simulations
				engine.depth = cfg.depth

				estimatedStatesPerRun := cfg.simulations * cfg.depth
				if estimatedStatesPerRun == 0 {
					b.Fatalf("invalid configuration %s: zero budget", cfg.name)
				}

				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if _, err := engine.NextMove(state); err != nil {
						b.Fatalf("next move failed: %v", err)
					}
				}
				b.StopTimer()

				elapsedSeconds := b.Elapsed().Seconds()
				if elapsedSeconds > 0 {
					totalStates := float64(b.N * estimatedStatesPerRun)
					// The estimate assumes every simulation consumes the configured depth,
					// which captures the upper bound of positions evaluated.
					b.ReportMetric(totalStates/elapsedSeconds, "states/s")
				}
			})
		}
	}
}
