package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// agentConfig says where each agent keeps its hooks and what airbag's entry looks like there.
type agentConfig struct {
	dir   string // under $HOME; install picks agents whose dir exists
	file  string
	event string
	entry func(cmd string) any
	note  string
}

var agents = map[string]agentConfig{
	"claude": {dir: ".claude", file: "settings.json", event: "PreToolUse", entry: matcherGroup},
	"codex": {dir: ".codex", file: "hooks.json", event: "PreToolUse", entry: matcherGroup,
		note: "run /hooks in Codex to review and trust it"},
	"cursor": {dir: ".cursor", file: "hooks.json", event: "beforeShellExecution",
		entry: func(cmd string) any { return map[string]any{"command": cmd} }},
}

var agentNames = []string{"claude", "codex", "cursor"}

func matcherGroup(cmd string) any {
	return map[string]any{"matcher": "Bash", "hooks": []any{map[string]any{"type": "command", "command": cmd}}}
}

func hookCommand(agent string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	// Forward slashes and quotes survive both bash and cmd on Windows.
	return fmt.Sprintf("%q hook %s", filepath.ToSlash(exe), agent), nil
}

// pick returns the named agents, or every agent installed on this machine.
func pick(names []string) ([]string, error) {
	if len(names) > 0 {
		for _, n := range names {
			if _, ok := agents[n]; !ok {
				return nil, fmt.Errorf("unknown agent %q (supported: %s)", n, strings.Join(agentNames, ", "))
			}
		}
		return names, nil
	}
	home, _ := os.UserHomeDir()
	var found []string
	for _, n := range agentNames {
		if _, err := os.Stat(filepath.Join(home, agents[n].dir)); err == nil {
			found = append(found, n)
		}
	}
	if len(found) == 0 {
		return nil, errors.New("no supported agent found; name one: airbag install claude|codex|cursor")
	}
	return found, nil
}

func install(names []string, out io.Writer) error {
	names, err := pick(names)
	if err != nil {
		return err
	}
	for _, n := range names {
		a := agents[n]
		cmd, err := hookCommand(n)
		if err != nil {
			return err
		}
		path, changed, err := editHooks(a, func(list []any) ([]any, bool) {
			if ours(list, n) != nil {
				return list, false
			}
			return append(list, a.entry(cmd)), true
		})
		if err != nil {
			return fmt.Errorf("%s: %w", n, err)
		}
		if !changed {
			fmt.Fprintf(out, "%-7s already installed in %s\n", n, path)
			continue
		}
		fmt.Fprintf(out, "%-7s installed in %s\n", n, path)
		if a.note != "" {
			fmt.Fprintf(out, "        %s\n", a.note)
		}
	}
	return nil
}

func uninstall(names []string, out io.Writer) error {
	names, err := pick(names)
	if err != nil {
		return err
	}
	for _, n := range names {
		path, changed, err := editHooks(agents[n], func(list []any) ([]any, bool) {
			drop := ours(list, n)
			var kept []any
			for i, e := range list {
				if !drop[i] {
					kept = append(kept, e)
				}
			}
			return kept, len(drop) > 0
		})
		if err != nil {
			return fmt.Errorf("%s: %w", n, err)
		}
		if changed {
			fmt.Fprintf(out, "%-7s removed from %s\n", n, path)
		}
	}
	return nil
}

// ours marks the entries in an event's hook list that run airbag for this agent.
func ours(list []any, agent string) map[int]bool {
	marked := map[int]bool{}
	for i, e := range list {
		b, _ := json.Marshal(e)
		if s := string(b); strings.Contains(s, "airbag") && strings.Contains(s, " hook "+agent) {
			marked[i] = true
		}
	}
	if len(marked) == 0 {
		return nil
	}
	return marked
}

// editHooks applies fn to hooks[event] in the agent's config file, keeping every other setting.
// The previous file is kept next to it as .airbag.bak.
func editHooks(a agentConfig, fn func([]any) ([]any, bool)) (path string, changed bool, err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false, err
	}
	path = filepath.Join(home, a.dir, a.file)
	cfg := map[string]any{}
	old, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return path, false, err
	}
	if len(old) > 0 {
		if err := json.Unmarshal(old, &cfg); err != nil {
			return path, false, fmt.Errorf("%s isn't valid JSON, not touching it: %w", path, err)
		}
	}
	hooks, _ := cfg["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	list, _ := hooks[a.event].([]any)
	list, changed = fn(list)
	if !changed {
		return path, false, nil
	}
	if len(list) == 0 {
		delete(hooks, a.event)
	} else {
		hooks[a.event] = list
	}
	cfg["hooks"] = hooks
	if a.event == "beforeShellExecution" && cfg["version"] == nil {
		cfg["version"] = 1 // Cursor requires it
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return path, false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return path, false, err
	}
	if len(old) > 0 {
		if err := os.WriteFile(path+".airbag.bak", old, 0o600); err != nil {
			return path, false, err
		}
	}
	return path, true, os.WriteFile(path, append(b, '\n'), 0o644)
}
