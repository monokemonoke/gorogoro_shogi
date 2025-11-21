package game

import (
	"errors"
	"math"
	"math/rand"
)

const (
	defaultMCTSIterations  = 800
	defaultMCTSExploration = 1.2
	mctsRolloutDepth       = 60
)

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
