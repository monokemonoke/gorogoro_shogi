package game

import "testing"

func newEmptyState(turn Player) GameState {
	return GameState{
		Hands: [2]map[PieceType]int{
			Bottom: make(map[PieceType]int),
			Top:    make(map[PieceType]int),
		},
		Turn: turn,
	}
}

func TestCheckmateStatusMate(t *testing.T) {
	state := newEmptyState(Bottom)
	state.Board[0][0] = Piece{Kind: King, Owner: Bottom, Present: true}
	state.Board[1][0] = Piece{Kind: Gold, Owner: Top, Present: true}
	state.Board[0][1] = Piece{Kind: Gold, Owner: Top, Present: true}
	state.Board[1][1] = Piece{Kind: King, Owner: Top, Present: true}

	mate, winner := CheckmateStatus(state)
	if !mate {
		t.Fatalf("expected checkmate, got false")
	}
	if winner != Top {
		t.Fatalf("expected top to be winner, got %v", winner)
	}
}

func TestCheckmateStatusEscapeAvailable(t *testing.T) {
	state := newEmptyState(Bottom)
	state.Board[0][0] = Piece{Kind: King, Owner: Bottom, Present: true}
	state.Board[1][0] = Piece{Kind: Gold, Owner: Top, Present: true}

	if mate, _ := CheckmateStatus(state); mate {
		t.Fatalf("expected escape to exist, but checkmate reported")
	}
}

func TestCheckmateStatusSafePosition(t *testing.T) {
	state := newEmptyState(Bottom)
	state.Board[0][0] = Piece{Kind: King, Owner: Bottom, Present: true}
	state.Board[5][4] = Piece{Kind: King, Owner: Top, Present: true}

	if mate, _ := CheckmateStatus(state); mate {
		t.Fatalf("expected no checkmate in safe position")
	}
}
