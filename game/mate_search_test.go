package game

import "testing"

func TestMateSearchDropMate(t *testing.T) {
	state := newEmptyState(Bottom)
	state.Turn = Bottom
	state.Board[5][0] = Piece{Kind: King, Owner: Top, Present: true}
	state.Board[3][1] = Piece{Kind: Silver, Owner: Bottom, Present: true}
	state.Board[4][2] = Piece{Kind: Gold, Owner: Bottom, Present: true}
	state.Hands[Bottom][Pawn] = 1

	mate, line := MateSearch(state, Bottom, 1)
	if !mate {
		t.Fatalf("expected mate to be found")
	}
	if len(line) == 0 {
		t.Fatalf("expected at least one move in sequence")
	}
}

func TestMateSearchRequiresPositiveDepth(t *testing.T) {
	state := newEmptyState(Bottom)
	state.Turn = Bottom
	state.Board[5][0] = Piece{Kind: King, Owner: Top, Present: true}
	state.Board[3][1] = Piece{Kind: Silver, Owner: Bottom, Present: true}
	state.Board[4][2] = Piece{Kind: Gold, Owner: Bottom, Present: true}
	state.Hands[Bottom][Pawn] = 1

	mate, _ := MateSearch(state, Bottom, 0)
	if mate {
		t.Fatalf("did not expect mate with zero ply limit")
	}
}

func TestMateSearchNoMateAvailable(t *testing.T) {
	state := newEmptyState(Bottom)
	state.Board[0][0] = Piece{Kind: King, Owner: Bottom, Present: true}
	state.Board[5][4] = Piece{Kind: King, Owner: Top, Present: true}

	mate, _ := MateSearch(state, Bottom, 2)
	if mate {
		t.Fatalf("expected no mate in empty king vs king scenario")
	}
}
