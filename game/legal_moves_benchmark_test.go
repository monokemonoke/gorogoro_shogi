package game

import (
	"runtime"
	"testing"
)

func BenchmarkGenerateLegalMoves(b *testing.B) {
	scenarios := []struct {
		name  string
		setup func() GameState
	}{
		{
			name: "initial_setup",
			setup: func() GameState {
				return NewGame()
			},
		},
		{
			name: "midgame_mixed_pieces",
			setup: func() GameState {
				state := newEmptyState(Bottom)
				state.Board[0][2] = Piece{Kind: King, Owner: Bottom, Present: true}
				state.Board[5][2] = Piece{Kind: King, Owner: Top, Present: true}
				state.Board[2][1] = Piece{Kind: Gold, Owner: Bottom, Present: true}
				state.Board[3][3] = Piece{Kind: Silver, Owner: Bottom, Promoted: true, Present: true}
				state.Board[2][4] = Piece{Kind: Pawn, Owner: Bottom, Promoted: true, Present: true}
				state.Board[4][1] = Piece{Kind: Gold, Owner: Top, Present: true}
				state.Board[3][2] = Piece{Kind: Silver, Owner: Top, Present: true}
				state.Board[1][4] = Piece{Kind: Pawn, Owner: Top, Present: true}
				state.Hands[Bottom][Pawn] = 1
				state.Hands[Bottom][Silver] = 1
				state.Hands[Top][Pawn] = 2
				return state
			},
		},
		{
			name: "drop_heavy_position",
			setup: func() GameState {
				state := newEmptyState(Bottom)
				state.Board[0][2] = Piece{Kind: King, Owner: Bottom, Present: true}
				state.Board[5][2] = Piece{Kind: King, Owner: Top, Present: true}
				state.Hands[Bottom][Pawn] = 3
				state.Hands[Bottom][Silver] = 1
				state.Hands[Bottom][Gold] = 1
				state.Hands[Top][Pawn] = 1
				return state
			},
		},
		{
			name: "king_in_check",
			setup: func() GameState {
				state := newEmptyState(Bottom)
				state.Board[0][0] = Piece{Kind: King, Owner: Bottom, Present: true}
				state.Board[5][4] = Piece{Kind: King, Owner: Top, Present: true}
				state.Board[1][0] = Piece{Kind: Gold, Owner: Top, Present: true}
				state.Board[3][1] = Piece{Kind: Silver, Owner: Bottom, Present: true}
				state.Board[2][2] = Piece{Kind: Gold, Owner: Bottom, Present: true}
				state.Hands[Bottom][Pawn] = 1
				return state
			},
		},
	}

	for _, sc := range scenarios {
		b.Run(sc.name, func(b *testing.B) {
			state := sc.setup()
			player := state.Turn
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				moves := GenerateLegalMoves(state, player)
				if len(moves) == 0 {
					b.Fatalf("expected legal moves in scenario %s", sc.name)
				}
				runtime.KeepAlive(moves)
			}
		})
	}
}

func BenchmarkGenerateLegalMovesFrom(b *testing.B) {
	scenarios := []struct {
		name  string
		setup func() (GameState, Coord)
	}{
		{
			name: "king_center_control",
			setup: func() (GameState, Coord) {
				state := newEmptyState(Bottom)
				center := Coord{X: 2, Y: 2}
				state.Board[center.Y][center.X] = Piece{Kind: King, Owner: Bottom, Present: true}
				state.Board[1][1] = Piece{Kind: Gold, Owner: Top, Present: true}
				state.Board[3][3] = Piece{Kind: Silver, Owner: Top, Present: true}
				return state, center
			},
		},
		{
			name: "promoted_pawn_edge",
			setup: func() (GameState, Coord) {
				state := newEmptyState(Bottom)
				at := Coord{X: 4, Y: 1}
				state.Board[0][2] = Piece{Kind: King, Owner: Bottom, Present: true}
				state.Board[5][2] = Piece{Kind: King, Owner: Top, Present: true}
				state.Board[at.Y][at.X] = Piece{Kind: Pawn, Owner: Bottom, Promoted: true, Present: true}
				state.Board[2][4] = Piece{Kind: Silver, Owner: Top, Present: true}
				return state, at
			},
		},
		{
			name: "silver_defense_top",
			setup: func() (GameState, Coord) {
				state := newEmptyState(Top)
				from := Coord{X: 2, Y: 4}
				state.Board[0][2] = Piece{Kind: King, Owner: Bottom, Present: true}
				state.Board[5][2] = Piece{Kind: King, Owner: Top, Present: true}
				state.Board[from.Y][from.X] = Piece{Kind: Silver, Owner: Top, Present: true}
				state.Board[3][1] = Piece{Kind: Gold, Owner: Bottom, Present: true}
				state.Board[3][2] = Piece{Kind: Pawn, Owner: Bottom, Present: true}
				return state, from
			},
		},
	}

	for _, sc := range scenarios {
		b.Run(sc.name, func(b *testing.B) {
			state, from := sc.setup()
			player := state.Turn
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				moves := GenerateLegalMovesFrom(state, player, from)
				if len(moves) == 0 {
					b.Fatalf("expected moves for %s", sc.name)
				}
				runtime.KeepAlive(moves)
			}
		})
	}
}
