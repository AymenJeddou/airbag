package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallUninstall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows
	settings := filepath.Join(home, ".claude", "settings.json")
	mk(t, settings)
	os.WriteFile(settings, []byte(`{"model":"opus","hooks":{"PreToolUse":[{"matcher":"Edit","hooks":[{"type":"command","command":"fmt.sh"}]}]}}`), 0o644)

	for range 2 { // second run must be a no-op
		if err := install(nil, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
	}
	var cfg struct {
		Model string
		Hooks map[string][]json.RawMessage
	}
	b, _ := os.ReadFile(settings)
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Model != "opus" || len(cfg.Hooks["PreToolUse"]) != 2 {
		t.Fatalf("want user settings kept and one airbag entry added, got %s", b)
	}
	if !strings.Contains(string(cfg.Hooks["PreToolUse"][1]), "hook claude") {
		t.Errorf("airbag entry missing: %s", b)
	}
	if _, err := os.Stat(filepath.Join(home, ".cursor")); err == nil {
		t.Error("install touched an agent that isn't installed")
	}

	if err := install([]string{"cursor"}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	cur, _ := os.ReadFile(filepath.Join(home, ".cursor", "hooks.json"))
	if !strings.Contains(string(cur), `"version": 1`) || !strings.Contains(string(cur), "hook cursor") {
		t.Errorf("bad cursor config: %s", cur)
	}

	if err := uninstall([]string{"claude"}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(settings)
	if strings.Contains(string(b), "airbag") || !strings.Contains(string(b), "fmt.sh") {
		t.Errorf("uninstall should remove only airbag: %s", b)
	}
}

func TestInstallRefusesBadJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	settings := filepath.Join(home, ".claude", "settings.json")
	mk(t, settings)
	os.WriteFile(settings, []byte(`{"model": "opus",}`), 0o644)
	if err := install([]string{"claude"}, &bytes.Buffer{}); err == nil {
		t.Error("want an error for invalid JSON")
	}
	if b, _ := os.ReadFile(settings); string(b) != `{"model": "opus",}` {
		t.Errorf("file was modified: %s", b)
	}
}
