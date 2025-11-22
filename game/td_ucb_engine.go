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
	profiler    tdProfiler
}

type tdMoveStat struct {
	visits int
	total  float64
}

// TDUCBProfile summarizes time spent in key sections of the TD rollouts.
type TDUCBProfile struct {
	NextMove        TDProfileMetric `json:"nextMove"`
	Simulation      TDProfileMetric `json:"simulation"`
	MoveSelection   TDProfileMetric `json:"moveSelection"`
	LegalGeneration TDProfileMetric `json:"legalGeneration"`
	MoveApply       TDProfileMetric `json:"moveApply"`
}

// TDProfileMetric exposes aggregate timing data in milliseconds.
type TDProfileMetric struct {
	Count   int64   `json:"count"`
	TotalMS float64 `json:"totalMs"`
	AvgMS   float64 `json:"avgMs"`
	MaxMS   float64 `json:"maxMs"`
}

type tdMetric struct {
	count int64
	total time.Duration
	max   time.Duration
}

func (m *tdMetric) add(duration time.Duration) {
	m.count++
	m.total += duration
	if duration > m.max {
		m.max = duration
	}
}

func (m tdMetric) snapshot() TDProfileMetric {
	if m.count == 0 {
		return TDProfileMetric{}
	}
	total := durationToMillis(m.total)
	return TDProfileMetric{
		Count:   m.count,
		TotalMS: total,
		AvgMS:   total / float64(m.count),
		MaxMS:   durationToMillis(m.max),
	}
}

type tdProfiler struct {
	nextMove        tdMetric
	simulation      tdMetric
	moveSelection   tdMetric
	legalGeneration tdMetric
	moveApply       tdMetric
}

func (p *tdProfiler) observeNextMove(duration time.Duration) {
	p.nextMove.add(duration)
}

func (p *tdProfiler) observeSimulation(duration time.Duration) {
	p.simulation.add(duration)
}

func (p *tdProfiler) observeMoveSelection(duration time.Duration) {
	p.moveSelection.add(duration)
}

func (p *tdProfiler) observeLegalGeneration(duration time.Duration) {
	p.legalGeneration.add(duration)
}

func (p *tdProfiler) observeMoveApply(duration time.Duration) {
	p.moveApply.add(duration)
}

func (p *tdProfiler) snapshot() TDUCBProfile {
	return TDUCBProfile{
		NextMove:        p.nextMove.snapshot(),
		Simulation:      p.simulation.snapshot(),
		MoveSelection:   p.moveSelection.snapshot(),
		LegalGeneration: p.legalGeneration.snapshot(),
		MoveApply:       p.moveApply.snapshot(),
	}
}

func (p *tdProfiler) reset() {
	*p = tdProfiler{}
}

func durationToMillis(d time.Duration) float64 {
	return float64(d) / float64(time.Millisecond)
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

// ProfileSnapshot returns a copy of the current profiling totals.
func (e *TDUCBEngine) ProfileSnapshot() TDUCBProfile {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.profiler.snapshot()
}

// ResetProfile clears the accumulated profiling metrics.
func (e *TDUCBEngine) ResetProfile() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.profiler.reset()
}

func (e *TDUCBEngine) NextMove(state GameState) (Move, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	start := time.Now()
	defer e.profiler.observeNextMove(time.Since(start))

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
	simStart := time.Now()
	defer e.profiler.observeSimulation(time.Since(simStart))
	state := CloneState(root)
	for depth := 0; depth < e.depth; depth++ {
		key := e.stateKey(state)
		currentValue := e.stateValue(state)
		legalStart := time.Now()
		legal := GenerateLegalMoves(state, state.Turn)
		e.profiler.observeLegalGeneration(time.Since(legalStart))
		if len(legal) == 0 {
			if InCheck(state, state.Turn) {
				e.values[key] = e.outcomeForBottom(state.Turn.Opponent())
			} else {
				e.values[key] = 0
			}
			return
		}

		move := e.selectSimulationMove(state, key, legal)
		mover := state.Turn
		applyStart := time.Now()
		ApplyMove(&state, move)
		e.profiler.observeMoveApply(time.Since(applyStart))
		state.Turn = mover.Opponent()

		reward, terminal := e.evaluateOutcome(state, mover)
		target := reward
		if !terminal {
			target += e.gamma * e.stateValue(state)
		}

		e.values[key] = currentValue + e.alpha*(target-currentValue)
		e.updateMoveStats(key, move, target)

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
	start := time.Now()
	defer e.profiler.observeMoveSelection(time.Since(start))
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
	start := time.Now()
	legal := GenerateLegalMoves(state, state.Turn)
	e.profiler.observeLegalGeneration(time.Since(start))
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
