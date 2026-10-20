package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
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

	if err := undo("", proj, &bytes.Buffer{}); err != nil {
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
	if err := undo("", proj, &bytes.Buffer{}); err != nil {
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

// When a target is too big to snapshot, Claude and Cursor ask the user; Codex (no "ask" support) denies.
func TestHookTooBig(t *testing.T) {
	t.Setenv("AIRBAG_HOME", t.TempDir())
	defer func(n int) { maxFiles = n }(maxFiles)
	maxFiles = 1
	proj := t.TempDir()
	mk(t, filepath.Join(proj, "data/a"))
	mk(t, filepath.Join(proj, "data/b"))
	in, _ := json.Marshal(map[string]any{"cwd": proj, "command": "rm -rf data", // Cursor's shape
		"tool_name": "Bash", "tool_input": map[string]string{"command": "rm -rf data"}}) // Claude/Codex shape
	for agent, want := range map[string]string{"claude": `"ask"`, "codex": `"deny"`, "cursor": `"ask"`} {
		var out bytes.Buffer
		if err := hook(agent, bytes.NewReader(in), &out); err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(out.Bytes(), []byte(want)) {
			t.Errorf("%s: got %s, want decision %s", agent, out.String(), want)
		}
	}
}

func TestCursorHook(t *testing.T) {
	t.Setenv("AIRBAG_HOME", t.TempDir())
	proj := t.TempDir()
	mk(t, filepath.Join(proj, "notes.md"))
	in, _ := json.Marshal(map[string]string{"cwd": proj, "command": "rm notes.md"})
	var out bytes.Buffer
	if err := hook("cursor", bytes.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte(`"allow"`)) {
		t.Errorf("cursor needs an explicit allow, got %q", out.String())
	}
	if all, _ := snaps(); len(all) != 1 || all[0].Agent != "cursor" {
		t.Errorf("want one cursor snapshot, got %+v", all)
	}
}

func TestPrune(t *testing.T) {
	t.Setenv("AIRBAG_HOME", t.TempDir())
	f := filepath.Join(t.TempDir(), "f")
	mk(t, f)
	s, err := takeSnap(Snap{Cmd: "test"}, []string{f})
	if err != nil {
		t.Fatal(err)
	}
	prune(s.Time.Add(keepFor - time.Minute))
	if all, _ := snaps(); len(all) != 1 {
		t.Fatalf("fresh snapshot pruned")
	}
	prune(s.Time.Add(keepFor + time.Minute))
	if all, _ := snaps(); len(all) != 0 {
		t.Fatalf("expired snapshot kept")
	}
}

func TestCursorFailsOpen(t *testing.T) {
	var out bytes.Buffer
	if err := hook("cursor", bytes.NewReader([]byte("not json")), &out); err == nil {
		t.Error("want the decode error reported")
	}
	if !bytes.Contains(out.Bytes(), []byte(`"allow"`)) {
		t.Errorf("cursor must still get allow, got %q", out.String())
	}
}

func TestChanges(t *testing.T) {
	t.Setenv("AIRBAG_HOME", t.TempDir())
	dir := t.TempDir()
	mk(t, filepath.Join(dir, "gone"))
	mk(t, filepath.Join(dir, "edited"))
	mk(t, filepath.Join(dir, "same"))
	s, err := takeSnap(Snap{Cmd: "test"}, []string{dir})
	if err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(dir, "gone"))
	os.WriteFile(filepath.Join(dir, "edited"), []byte("new"), 0o644)
	mk(t, filepath.Join(dir, "new"))
	got, err := changes(s)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"modified " + filepath.Join(dir, "edited"), "deleted  " + filepath.Join(dir, "gone"), "added    " + filepath.Join(dir, "new")}
	if !slices.Equal(got, want) {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

// undo in one project must never restore another project's snapshot, even a newer one.
func TestUndoScopedToFolder(t *testing.T) {
	t.Setenv("AIRBAG_HOME", t.TempDir())
	a, b := t.TempDir(), t.TempDir()
	mk(t, filepath.Join(a, "keep"))
	mk(t, filepath.Join(b, "other"))
	sa, _ := takeSnap(Snap{Cmd: "rm keep"}, []string{filepath.Join(a, "keep")})
	takeSnap(Snap{Cmd: "rm other"}, []string{filepath.Join(b, "other")}) // newer
	for dir, want := range map[string]string{a: sa.ID, "": ""} {
		s, err := find("1", dir)
		if err != nil {
			t.Fatal(err)
		}
		if want != "" && s.ID != want {
			t.Errorf("find in %s picked %s (%s), want %s", dir, s.ID, s.Cmd, want)
		}
		if want == "" && s.Cmd != "rm other" {
			t.Errorf("--all should pick the newest overall, got %s", s.Cmd)
		}
	}
}
