// Command airbag snapshots what an AI agent's shell command is about to destroy, so `airbag undo` can bring it back.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const usage = `airbag: undo for AI coding agents

  airbag hook claude      PreToolUse hook for Claude Code (reads hook JSON on stdin)
  airbag snap PATH...     snapshot paths by hand
  airbag list             recent snapshots
  airbag undo [N|ID]      restore snapshot N (1 = newest, the default) or ID
`

func main() {
	arg := func(i int) string {
		if len(os.Args) > i {
			return os.Args[i]
		}
		return ""
	}
	var err error
	switch arg(1) {
	case "hook":
		// Never block the agent because airbag broke: report and let the command run.
		if err := hook(arg(2), os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "airbag:", err)
		}
		return
	case "snap":
		err = snapCmd(os.Args[2:], os.Stdout)
	case "list":
		err = list(os.Stdout)
	case "undo":
		err = undo(arg(2), os.Stdout)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "airbag:", err)
		os.Exit(1)
	}
}

// claudeInput is the subset of Claude Code's PreToolUse payload we use.
type claudeInput struct {
	Cwd       string `json:"cwd"`
	ToolName  string `json:"tool_name"`
	ToolInput struct {
		Command string `json:"command"`
	} `json:"tool_input"`
}

func hook(agent string, in io.Reader, out io.Writer) error {
	if agent != "claude" {
		return fmt.Errorf("unknown agent %q (supported: claude)", agent)
	}
	var h claudeInput
	if err := json.NewDecoder(in).Decode(&h); err != nil {
		return err
	}
	if h.ToolName != "Bash" || h.ToolInput.Command == "" {
		return nil
	}
	plan := Analyze(h.ToolInput.Command, h.Cwd, os.Environ())
	paths := plan.all()
	if len(paths) == 0 {
		return nil
	}
	_, err := takeSnap(Snap{Cmd: h.ToolInput.Command, Cwd: h.Cwd, Agent: agent}, paths)
	if errors.Is(err, errTooBig) && len(plan.Paths) > 0 {
		// We stay out of the permission flow unless we can't protect an explicit target.
		return json.NewEncoder(out).Encode(map[string]any{"hookSpecificOutput": map[string]string{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       "ask",
			"permissionDecisionReason": "airbag can't snapshot " + strings.Join(plan.Paths, ", ") + " (too big), so this can't be undone",
		}})
	}
	return err
}

func snapCmd(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: airbag snap PATH...")
	}
	cwd, _ := os.Getwd()
	var paths []string
	for _, p := range args {
		p, err := filepath.Abs(p)
		if err != nil {
			return err
		}
		if _, err := os.Lstat(p); err != nil {
			return err
		}
		paths = append(paths, p)
	}
	s, err := takeSnap(Snap{Cmd: "airbag snap " + strings.Join(args, " "), Cwd: cwd, Agent: "manual"}, dedupe(paths))
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "snapshot", s.ID)
	return nil
}

func list(out io.Writer) error {
	all, err := snaps()
	if err != nil {
		return err
	}
	if len(all) == 0 {
		fmt.Fprintln(out, "no snapshots yet")
	}
	for i, s := range all {
		cmd := strings.ReplaceAll(s.Cmd, "\n", " ")
		if len(cmd) > 60 {
			cmd = cmd[:57] + "..."
		}
		fmt.Fprintf(out, "%3d  %s  %-8s %8s ago  %s\n", i+1, s.ID, s.Agent, time.Since(s.Time).Round(time.Second), cmd)
	}
	return nil
}

func undo(ref string, out io.Writer) error {
	s, err := find(ref)
	if err != nil {
		return err
	}
	backup, err := restore(s)
	if err != nil {
		return err
	}
	for _, it := range s.Items {
		fmt.Fprintln(out, "restored", it.Path)
	}
	if backup.ID != "" {
		fmt.Fprintf(out, "previous state saved as %s (airbag undo %s to go back)\n", backup.ID, backup.ID)
	}
	return nil
}
