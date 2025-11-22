package game

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

const (
	defaultMCTSIterations  = 800
	defaultMCTSExploration = 1.2
	mctsRolloutDepth       = 60
)

type moveStats struct {
	Visits int     `json:"visits"`
	Wins   float64 `json:"wins"`
}

type storedKnowledge struct {
	States map[string]map[string]moveStats `json:"states"`
}

type MCTSEngine struct {
	iterations  int
	exploration float64
	rng         *rand.Rand
	storagePath string
	knowledge   map[string]map[string]moveStats
	dirty       bool
	mu          sync.Mutex
}

func NewMCTSEngine(iterations int, seed int64) *MCTSEngine {
	return NewPersistentMCTSEngine(iterations, seed, "")
}

func NewPersistentMCTSEngine(iterations int, seed int64, storagePath string) *MCTSEngine {
	if iterations <= 0 {
		iterations = defaultMCTSIterations
	}
	engine := &MCTSEngine{
		iterations:  iterations,
		exploration: defaultMCTSExploration,
		rng:         rand.New(rand.NewSource(seed)),
		storagePath: storagePath,
		knowledge:   make(map[string]map[string]moveStats),
	}
	if err := engine.loadKnowledge(); err != nil {
		log.Printf("mcts: failed to load knowledge: %v", err)
	}
	return engine
}

func (e *MCTSEngine) SaveIfNeeded() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.saveLocked()
}

func (e *MCTSEngine) NextMove(state GameState) (Move, error) {
	legal := GenerateLegalMoves(state, state.Turn)
	if len(legal) == 0 {
		return Move{}, errors.New("no legal moves to play")
	}
	rootState := CloneState(state)
	root := newMCTSNode(rootState, nil, nil)
	stateKey, prior := e.snapshotKnowledge(rootState)
	applyPriorKnowledge(root, prior)
	rootPlayer := state.Turn
	rng := e.newWorkerRNG()
	for i := 0; i < e.iterations; i++ {
		node := root
		for len(node.untried) == 0 && len(node.children) > 0 {
			node = node.selectChild(e.exploration)
		}
		if len(node.untried) > 0 {
			node = node.expand(rng)
		}
		winner, decided := e.rollout(node.state, rootPlayer, rng)
		node.backpropagate(winner, rootPlayer, decided)
	}
	best := root.bestChildByVisits()
	if best == nil || best.move == nil {
		return Move{}, errors.New("failed to choose move")
	}
	e.updateKnowledgeFromRoot(root, stateKey)
	if err := e.SaveIfNeeded(); err != nil {
		log.Printf("mcts: failed to persist knowledge: %v", err)
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

func (e *MCTSEngine) rollout(state GameState, root Player, rng *rand.Rand) (Player, bool) {
	sim := CloneState(state)
	for depth := 0; depth < mctsRolloutDepth; depth++ {
		moves := GenerateLegalMoves(sim, sim.Turn)
		if len(moves) == 0 {
			if InCheck(sim, sim.Turn) {
				return sim.Turn.Opponent(), true
			}
			return root, false
		}
		mv := moves[rng.Intn(len(moves))]
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

func (e *MCTSEngine) newWorkerRNG() *rand.Rand {
	e.mu.Lock()
	seed := e.rng.Int63()
	e.mu.Unlock()
	return rand.New(rand.NewSource(seed))
}

func (e *MCTSEngine) snapshotKnowledge(state GameState) (string, map[string]moveStats) {
	if e.storagePath == "" {
		return "", nil
	}
	key := encodeStateKey(state)
	e.mu.Lock()
	entries := e.knowledge[key]
	var clone map[string]moveStats
	if len(entries) > 0 {
		clone = make(map[string]moveStats, len(entries))
		for mv, stats := range entries {
			clone[mv] = stats
		}
	}
	e.mu.Unlock()
	return key, clone
}

func applyPriorKnowledge(root *mctsNode, entries map[string]moveStats) {
	if len(entries) == 0 {
		return
	}
	var remaining []Move
	for _, mv := range root.untried {
		stats, ok := entries[FormatMove(mv)]
		if !ok {
			remaining = append(remaining, mv)
			continue
		}
		childState := CloneState(root.state)
		ApplyMove(&childState, mv)
		childState.Turn = childState.Turn.Opponent()
		mvCopy := mv
		child := newMCTSNode(childState, &mvCopy, root)
		child.visits = stats.Visits
		child.wins = stats.Wins
		root.children = append(root.children, child)
	}
	root.untried = remaining
}

func (e *MCTSEngine) updateKnowledgeFromRoot(root *mctsNode, key string) {
	if e.storagePath == "" {
		return
	}
	if key == "" {
		key = encodeStateKey(root.state)
	}
	entries := make(map[string]moveStats)
	for _, child := range root.children {
		if child.move == nil {
			continue
		}
		entries[FormatMove(*child.move)] = moveStats{
			Visits: child.visits,
			Wins:   child.wins,
		}
	}
	e.mu.Lock()
	e.knowledge[key] = entries
	e.dirty = true
	e.mu.Unlock()
}

func (e *MCTSEngine) loadKnowledge() error {
	if e.storagePath == "" {
		return nil
	}
	data, err := os.ReadFile(e.storagePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b {
		return e.loadCompressedKnowledge(data)
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil
	}
	if trimmed[0] == '{' {
		return e.loadLegacyJSON(trimmed)
	}
	return e.loadPlainKnowledge(trimmed)
}

func (e *MCTSEngine) loadCompressedKnowledge(data []byte) error {
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer reader.Close()
	return e.loadFromReader(reader)
}

func (e *MCTSEngine) loadPlainKnowledge(data []byte) error {
	return e.loadFromReader(bytes.NewReader(data))
}

func (e *MCTSEngine) loadFromReader(r io.Reader) error {
	entries, err := decodeKnowledge(r)
	if err != nil {
		return err
	}
	e.knowledge = entries
	return nil
}

func (e *MCTSEngine) loadLegacyJSON(data []byte) error {
	var payload storedKnowledge
	if err := json.Unmarshal(data, &payload); err != nil {
		return err
	}
	if payload.States != nil {
		e.knowledge = payload.States
	} else {
		e.knowledge = make(map[string]map[string]moveStats)
	}
	return nil
}

func (e *MCTSEngine) saveLocked() error {
	if e.storagePath == "" || !e.dirty {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(e.storagePath), 0o755); err != nil {
		return err
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if err := encodeKnowledge(gz, e.knowledge); err != nil {
		gz.Close()
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	if err := os.WriteFile(e.storagePath, buf.Bytes(), 0o644); err != nil {
		return err
	}
	e.dirty = false
	return nil
}

func encodeStateKey(state GameState) string {
	var b strings.Builder
	b.Grow(BoardRows*BoardCols*3 + 32)
	b.WriteByte(byte('0' + byte(state.Turn)))
	for y := 0; y < BoardRows; y++ {
		for x := 0; x < BoardCols; x++ {
			p := state.Board[y][x]
			if !p.Present {
				b.WriteByte('.')
				continue
			}
			b.WriteByte(byte('0' + byte(p.Owner)))
			b.WriteByte(byte('0' + byte(p.Kind)))
			if p.Promoted {
				b.WriteByte('1')
			} else {
				b.WriteByte('0')
			}
		}
	}
	for _, player := range []Player{Bottom, Top} {
		b.WriteByte('|')
		for _, piece := range []PieceType{King, Gold, Silver, Pawn} {
			count := state.Hands[player][piece]
			b.WriteString(strconv.Itoa(count))
			b.WriteByte(',')
		}
	}
	return b.String()
}

func encodeKnowledge(w io.Writer, knowledge map[string]map[string]moveStats) error {
	bw := bufio.NewWriter(w)
	keys := make([]string, 0, len(knowledge))
	for key := range knowledge {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		line := key
		moves := knowledge[key]
		if len(moves) > 0 {
			moveKeys := make([]string, 0, len(moves))
			for mv := range moves {
				moveKeys = append(moveKeys, mv)
			}
			sort.Strings(moveKeys)
			parts := make([]string, 0, len(moveKeys))
			for _, mv := range moveKeys {
				stats := moves[mv]
				wins := strconv.FormatFloat(stats.Wins, 'g', -1, 64)
				parts = append(parts, mv+":"+strconv.Itoa(stats.Visits)+":"+wins)
			}
			line += "\t" + strings.Join(parts, ",")
		}
		if _, err := bw.WriteString(line + "\n"); err != nil {
			return err
		}
	}
	return bw.Flush()
}

func decodeKnowledge(r io.Reader) (map[string]map[string]moveStats, error) {
	scanner := bufio.NewScanner(r)
	entries := make(map[string]map[string]moveStats)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		stateKey, movePart, found := strings.Cut(line, "\t")
		if !found {
			movePart = ""
		}
		if stateKey == "" {
			return nil, errors.New("invalid knowledge line: missing state key")
		}
		moves := make(map[string]moveStats)
		if movePart != "" {
			segments := strings.Split(movePart, ",")
			for _, segment := range segments {
				if segment == "" {
					continue
				}
				move, rest, ok := strings.Cut(segment, ":")
				if !ok {
					return nil, fmt.Errorf("invalid move entry: %q", segment)
				}
				visitsStr, winsStr, ok := strings.Cut(rest, ":")
				if !ok {
					return nil, fmt.Errorf("invalid move stats: %q", segment)
				}
				visits, err := strconv.Atoi(visitsStr)
				if err != nil {
					return nil, fmt.Errorf("invalid visits value %q", visitsStr)
				}
				wins, err := strconv.ParseFloat(winsStr, 64)
				if err != nil {
					return nil, fmt.Errorf("invalid wins value %q", winsStr)
				}
				moves[move] = moveStats{Visits: visits, Wins: wins}
			}
		}
		entries[stateKey] = moves
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}
