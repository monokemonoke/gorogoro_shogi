package game

import (
	"bufio"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultTDSimulations = 300
	defaultTDRollout     = 40
	defaultTDAlpha       = 0.4
	defaultTDGamma       = 0.95
	defaultTDExploration = 0.9
	tdRecordState        = "S"
	tdRecordMove         = "M"
)

// TDUCBEngine learns a simple value function with TD(0) updates and uses UCB to
// balance exploration and exploitation while sampling rollouts.
type TDUCBEngine struct {
	values      map[string]float64
	moveStats   map[string]map[string]*tdMoveStat
	alpha       float64
	gamma       float64
	exploration float64
	simulations int
	depth       int
	rng         *rand.Rand
	storagePath string
	dirty       bool
	mu          sync.Mutex
}

type tdMoveStat struct {
	visits int
	total  float64
}

func (s *tdMoveStat) mean() float64 {
	if s.visits == 0 {
		return 0
	}
	return s.total / float64(s.visits)
}

func NewTDUCBEngine(seed int64) *TDUCBEngine {
	return newTDUCBEngine(seed, "")
}

func NewPersistentTDUCBEngine(seed int64, storagePath string) *TDUCBEngine {
	engine := newTDUCBEngine(seed, storagePath)
	if err := engine.loadKnowledge(); err != nil {
		fmt.Printf("td-ucb: failed to load knowledge: %v\n", err)
	}
	return engine
}

func newTDUCBEngine(seed int64, storagePath string) *TDUCBEngine {
	if seed == 0 {
		seed = time.Now().UnixNano()
	}
	return &TDUCBEngine{
		values:      make(map[string]float64),
		moveStats:   make(map[string]map[string]*tdMoveStat),
		alpha:       defaultTDAlpha,
		gamma:       defaultTDGamma,
		exploration: defaultTDExploration,
		simulations: defaultTDSimulations,
		depth:       defaultTDRollout,
		rng:         rand.New(rand.NewSource(seed)),
		storagePath: storagePath,
	}
}

func (e *TDUCBEngine) NextMove(state GameState) (Move, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	legal := GenerateLegalMoves(state, state.Turn)
	if len(legal) == 0 {
		return Move{}, errors.New("no legal moves to play")
	}

	root := CloneState(state)
	for i := 0; i < e.simulations; i++ {
		e.runSimulation(root)
	}

	e.rng.Shuffle(len(legal), func(i, j int) {
		legal[i], legal[j] = legal[j], legal[i]
	})
	key := e.stateKey(root)
	stats := e.moveStats[key]
	best := legal[0]
	bestScore := math.Inf(-1)
	for _, mv := range legal {
		score := e.moveMean(stats, mv)
		if state.Turn == Top {
			score = -score
		}
		if score > bestScore {
			bestScore = score
			best = mv
		}
	}
	return best, nil
}

func (e *TDUCBEngine) runSimulation(root GameState) {
	e.markDirty()
	state := CloneState(root)
	for depth := 0; depth < e.depth; depth++ {
		key := e.stateKey(state)
		legal := GenerateLegalMoves(state, state.Turn)
		if len(legal) == 0 {
			if InCheck(state, state.Turn) {
				e.values[key] = e.outcomeForBottom(state.Turn.Opponent())
			} else {
				e.values[key] = 0
			}
			return
		}

		move := e.selectSimulationMove(state, key, legal)
		next := CloneState(state)
		ApplyMove(&next, move)
		mover := state.Turn
		next.Turn = mover.Opponent()

		reward, terminal := e.evaluateOutcome(next, mover)
		target := reward
		if !terminal {
			target += e.gamma * e.stateValue(next)
		}

		current := e.stateValue(state)
		e.values[key] = current + e.alpha*(target-current)
		e.updateMoveStats(key, move, target)

		state = next
		if terminal {
			doneKey := e.stateKey(state)
			if _, ok := e.values[doneKey]; !ok {
				e.values[doneKey] = reward
			}
			return
		}
	}
}

func (e *TDUCBEngine) selectSimulationMove(state GameState, key string, legal []Move) Move {
	stats := e.moveStats[key]
	if stats == nil {
		stats = make(map[string]*tdMoveStat)
		e.moveStats[key] = stats
	}
	e.rng.Shuffle(len(legal), func(i, j int) {
		legal[i], legal[j] = legal[j], legal[i]
	})
	total := 0
	for _, st := range stats {
		total += st.visits
	}
	if total == 0 {
		total = 1
	}

	best := legal[0]
	bestScore := math.Inf(-1)
	for _, mv := range legal {
		mKey := FormatMove(mv)
		entry := stats[mKey]
		if entry == nil || entry.visits == 0 {
			return mv
		}

		mean := entry.mean()
		if state.Turn == Top {
			mean = -mean
		}
		score := mean + e.exploration*math.Sqrt(math.Log(float64(total)+1)/float64(entry.visits))
		if score > bestScore {
			bestScore = score
			best = mv
		}
	}
	return best
}

func (e *TDUCBEngine) evaluateOutcome(state GameState, mover Player) (float64, bool) {
	legal := GenerateLegalMoves(state, state.Turn)
	if len(legal) > 0 {
		return 0, false
	}
	if InCheck(state, state.Turn) {
		return e.outcomeForBottom(mover), true
	}
	return 0, true
}

func (e *TDUCBEngine) outcomeForBottom(winner Player) float64 {
	if winner == Bottom {
		return 1
	}
	if winner == Top {
		return -1
	}
	return 0
}

func (e *TDUCBEngine) stateKey(state GameState) string {
	return encodeState(state) + "#" + string(rune('0'+int(state.Turn)))
}

func (e *TDUCBEngine) stateValue(state GameState) float64 {
	if v, ok := e.values[e.stateKey(state)]; ok {
		return v
	}
	return 0
}

func (e *TDUCBEngine) moveMean(stats map[string]*tdMoveStat, mv Move) float64 {
	if stats == nil {
		return 0
	}
	entry := stats[FormatMove(mv)]
	if entry == nil {
		return 0
	}
	return entry.mean()
}

func (e *TDUCBEngine) updateMoveStats(key string, mv Move, value float64) {
	stats := e.moveStats[key]
	if stats == nil {
		stats = make(map[string]*tdMoveStat)
		e.moveStats[key] = stats
	}
	mKey := FormatMove(mv)
	entry := stats[mKey]
	if entry == nil {
		entry = &tdMoveStat{}
		stats[mKey] = entry
	}
	entry.visits++
	entry.total += value
}

func (e *TDUCBEngine) markDirty() {
	if e.storagePath != "" {
		e.dirty = true
	}
}

func (e *TDUCBEngine) SaveIfNeeded() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.dirty || e.storagePath == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(e.storagePath), 0o755); err != nil {
		return err
	}
	tmp := e.storagePath + ".tmp"
	if err := e.writeKnowledge(tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, e.storagePath); err != nil {
		return err
	}
	e.dirty = false
	return nil
}

func (e *TDUCBEngine) writeKnowledge(path string) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	gz := gzip.NewWriter(file)
	defer gz.Close()

	writer := bufio.NewWriter(gz)
	for key, value := range e.values {
		if _, err := fmt.Fprintf(writer, "%s\t%s\t%.8f\n", tdRecordState, key, value); err != nil {
			return err
		}
	}
	for stateKey, moves := range e.moveStats {
		for moveKey, stat := range moves {
			if stat.visits == 0 {
				continue
			}
			if _, err := fmt.Fprintf(writer, "%s\t%s\t%s\t%d\t%.8f\n",
				tdRecordMove, stateKey, moveKey, stat.visits, stat.total); err != nil {
				return err
			}
		}
	}
	if err := writer.Flush(); err != nil {
		return err
	}
	return nil
}

func (e *TDUCBEngine) loadKnowledge() error {
	if e.storagePath == "" {
		return nil
	}
	file, err := os.Open(e.storagePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer file.Close()

	gz, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gz.Close()

	scanner := bufio.NewScanner(gz)
	for scanner.Scan() {
		line := scanner.Text()
		if err := e.parseRecord(line); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func (e *TDUCBEngine) parseRecord(line string) error {
	if strings.TrimSpace(line) == "" {
		return nil
	}
	fields := strings.Split(line, "\t")
	switch fields[0] {
	case tdRecordState:
		if len(fields) != 3 {
			return fmt.Errorf("td-ucb: malformed state record")
		}
		value, err := strconv.ParseFloat(fields[2], 64)
		if err != nil {
			return err
		}
		e.values[fields[1]] = value
	case tdRecordMove:
		if len(fields) != 5 {
			return fmt.Errorf("td-ucb: malformed move record")
		}
		visits, err := strconv.Atoi(fields[3])
		if err != nil {
			return err
		}
		total, err := strconv.ParseFloat(fields[4], 64)
		if err != nil {
			return err
		}
		stats := e.moveStats[fields[1]]
		if stats == nil {
			stats = make(map[string]*tdMoveStat)
			e.moveStats[fields[1]] = stats
		}
		stats[fields[2]] = &tdMoveStat{visits: visits, total: total}
	default:
		return fmt.Errorf("td-ucb: unknown record kind %q", fields[0])
	}
	return nil
}
