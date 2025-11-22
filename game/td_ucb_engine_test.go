package game

import (
	"compress/gzip"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestTDUCBEngineSaveLoadPlainText(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "td_ucb_data")

	engine := newTDUCBEngine(1, path)
	engine.values["state"] = 0.25
	engine.dirty = true

	if err := engine.SaveIfNeeded(); err != nil {
		t.Fatalf("SaveIfNeeded failed: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read written data: %v", err)
	}
	if len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b {
		t.Fatalf("expected plain text data, found gzip header")
	}

	reloaded := newTDUCBEngine(1, path)
	if err := reloaded.loadKnowledge(); err != nil {
		t.Fatalf("loadKnowledge failed: %v", err)
	}
	got := reloaded.values["state"]
	if math.Abs(got-0.25) > 1e-9 {
		t.Fatalf("state value = %v, want 0.25", got)
	}
	if len(reloaded.moveStats) != 0 {
		t.Fatalf("move stats should not persist, found: %+v", reloaded.moveStats)
	}
}

func TestTDUCBEngineLoadLegacyGzip(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.gz")

	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	gz := gzip.NewWriter(file)
	if _, err := fmt.Fprintln(gz, "S\tlegacy\t0.12500000"); err != nil {
		t.Fatalf("failed to write gzip data: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("failed to close gzip writer: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("failed to close file: %v", err)
	}

	engine := newTDUCBEngine(1, path)
	if err := engine.loadKnowledge(); err != nil {
		t.Fatalf("loadKnowledge failed: %v", err)
	}
	got := engine.values["legacy"]
	if math.Abs(got-0.125) > 1e-9 {
		t.Fatalf("legacy value = %v, want 0.125", got)
	}
	if len(engine.moveStats) != 0 {
		t.Fatalf("move stats should not persist, found: %+v", engine.moveStats)
	}
}
