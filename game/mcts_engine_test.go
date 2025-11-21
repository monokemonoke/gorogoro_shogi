package game

import (
	"path/filepath"
	"testing"
)

func TestMCTSEnginePersistsKnowledge(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	storage := filepath.Join(tempDir, "mcts.json")
	engine := NewPersistentMCTSEngine(32, 1, storage)
	state := NewGame()

	if _, err := engine.NextMove(state); err != nil {
		t.Fatalf("NextMove failed: %v", err)
	}
	if err := engine.SaveIfNeeded(); err != nil {
		t.Fatalf("SaveIfNeeded failed: %v", err)
	}

	reloaded := NewPersistentMCTSEngine(32, 1, storage)
	key := encodeStateKey(state)
	entries := reloaded.knowledge[key]
	if len(entries) == 0 {
		t.Fatalf("expected knowledge for key %q to be restored", key)
	}
}
