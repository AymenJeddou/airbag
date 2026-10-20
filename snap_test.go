package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The whole point: agent runs rm -rf, `airbag undo` brings it back byte for byte.
func TestHookThenUndo(t *testing.T) {
	t.Setenv("AIRBAG_HOME", t.TempDir())
	proj := t.TempDir()
	mk(t, filepath.Join(proj, "src/main.go"))
	mk(t, filepath.Join(proj, "src/deep/x.txt"))
	mk(t, filepath.Join(proj, ".env"))

	in, _ := json.Marshal(map[string]any{"cwd": proj, "tool_name": "Bash", "tool_input": map[string]string{"command": "rm -rf src .env"}})
	var out bytes.Buffer
	if err := hook("claude", bytes.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatalf("hook should stay silent, wrote %s", out.String())
	}

	// the agent's command runs, then the agent "fixes" things
	os.RemoveAll(filepath.Join(proj, "src"))
	os.Remove(filepath.Join(proj, ".env"))
	mk(t, filepath.Join(proj, "src/new.go"))

	if err := undo("", &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"src/main.go", "src/deep/x.txt", ".env"} {
		full := filepath.Join(proj, p)
		b, err := os.ReadFile(full)
		if err != nil || string(b) != full {
			t.Errorf("%s not restored: %q %v", p, b, err)
		}
	}
	if _, err := os.Stat(filepath.Join(proj, "src/new.go")); err == nil {
		t.Error("src/new.go should be gone after undo")
	}

	// undo is undoable: the newest snapshot is now the pre-undo state
	if err := undo("", &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(proj, "src/new.go")); err != nil {
		t.Error("second undo should bring back src/new.go")
	}
}

func TestHookIgnoresSafeCommands(t *testing.T) {
	t.Setenv("AIRBAG_HOME", t.TempDir())
	in := `{"cwd":".","tool_name":"Bash","tool_input":{"command":"ls -la"}}`
	if err := hook("claude", bytes.NewReader([]byte(in)), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if all, _ := snaps(); len(all) != 0 {
		t.Errorf("got %d snapshots, want 0", len(all))
	}
}

// When a target is too big to snapshot, Claude asks the user and Codex (no "ask" support) denies.
func TestHookTooBig(t *testing.T) {
	t.Setenv("AIRBAG_HOME", t.TempDir())
	defer func(n int) { maxFiles = n }(maxFiles)
	maxFiles = 1
	proj := t.TempDir()
	mk(t, filepath.Join(proj, "data/a"))
	mk(t, filepath.Join(proj, "data/b"))
	in, _ := json.Marshal(map[string]any{"cwd": proj, "tool_name": "Bash", "tool_input": map[string]string{"command": "rm -rf data"}})
	for agent, want := range map[string]string{"claude": `"ask"`, "codex": `"deny"`} {
		var out bytes.Buffer
		if err := hook(agent, bytes.NewReader(in), &out); err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(out.Bytes(), []byte(want)) {
			t.Errorf("%s: got %s, want decision %s", agent, out.String(), want)
		}
	}
}
