package game

var pieceScores = map[PieceType]int{
	King:   1000,
	Gold:   70,
	Silver: 50,
	Pawn:   10,
}

var orderedPieceTypes = []PieceType{King, Gold, Silver, Pawn}

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
	if (p.Kind == Silver || p.Kind == Pawn) && p.Promoted {
		return pieceScores[Gold]
	}
	return pieceScores[p.Kind]
}
