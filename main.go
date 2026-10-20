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

// version is set by the release build.
var version = "dev"

const usage = `airbag: undo for AI coding agents

  airbag install [AGENT...]    add the hook to claude, codex, cursor (default: those installed)
  airbag uninstall [AGENT...]  remove it again
  airbag list [--all]          recent snapshots for this folder (--all: every folder)
  airbag diff [--all] [N|ID]   what changed since snapshot N (1 = newest, the default) or ID
  airbag undo [--all] [N|ID]   restore snapshot N or ID
  airbag snap PATH...          snapshot paths by hand
  airbag hook AGENT            the hook itself (reads the agent's JSON on stdin)
  airbag version
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
		_, dir, out := scope(os.Args[2:])
		err = list(dir, out)
	case "undo":
		err = undo(scope(os.Args[2:]))
	case "diff":
		err = diff(scope(os.Args[2:]))
	case "install":
		err = install(os.Args[2:], os.Stdout)
	case "uninstall":
		err = uninstall(os.Args[2:], os.Stdout)
	case "version", "--version", "-v":
		fmt.Println("airbag", version)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "airbag:", err)
		os.Exit(1)
	}
}

// hookInput is the subset of the hook payloads we use. Claude Code and Codex send
// tool_name + tool_input; Cursor's beforeShellExecution sends command at the top level.
type hookInput struct {
	Cwd       string `json:"cwd"`
	Command   string `json:"command"`
	ToolName  string `json:"tool_name"`
	ToolInput struct {
		Command string `json:"command"`
	} `json:"tool_input"`
}

func hook(agent string, in io.Reader, out io.Writer) error {
	if agent != "claude" && agent != "codex" && agent != "cursor" {
		return fmt.Errorf("unknown agent %q (supported: claude, codex, cursor)", agent)
	}
	var h hookInput
	if err := json.NewDecoder(in).Decode(&h); err != nil {
		if agent == "cursor" { // fail open: Cursor blocks on missing output
			fmt.Fprintln(out, `{"permission":"allow"}`)
		}
		return err
	}
	if agent == "cursor" {
		return cursorHook(h, out)
	}
	if h.ToolName != "Bash" || h.ToolInput.Command == "" {
		return nil
	}
	tooBig, err := protect(agent, h.ToolInput.Command, h.Cwd)
	if tooBig == "" {
		return err
	}
	// We stay out of the permission flow unless we can't protect an explicit target.
	// Codex parses "ask" but doesn't support it yet, so it gets a deny the agent can explain.
	decision := "ask"
	if agent == "codex" {
		decision = "deny"
	}
	return json.NewEncoder(out).Encode(map[string]any{"hookSpecificOutput": map[string]string{
		"hookEventName":            "PreToolUse",
		"permissionDecision":       decision,
		"permissionDecisionReason": tooBig,
	}})
}

// cursorHook always answers, because Cursor treats empty or invalid output as a block.
func cursorHook(h hookInput, out io.Writer) error {
	resp := map[string]string{"permission": "allow"}
	tooBig, err := protect("cursor", h.Command, h.Cwd)
	if tooBig != "" {
		resp = map[string]string{"permission": "ask", "user_message": tooBig, "agent_message": tooBig}
	}
	if encErr := json.NewEncoder(out).Encode(resp); encErr != nil {
		return encErr
	}
	return err
}

// protect snapshots what cmd puts at risk. It returns a reason when an explicit target is too big to snapshot.
func protect(agent, cmd, cwd string) (tooBig string, err error) {
	plan := Analyze(cmd, cwd, os.Environ())
	paths := plan.all()
	if len(paths) == 0 {
		return "", nil
	}
	_, err = takeSnap(Snap{Cmd: cmd, Cwd: cwd, Agent: agent}, paths)
	prune(time.Now())
	if errors.Is(err, errTooBig) && len(plan.Paths) > 0 {
		return "airbag can't snapshot " + strings.Join(plan.Paths, ", ") + " (too big), so this can't be undone", nil
	}
	return "", err
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

// scope reads [--all] [N|ID]. Without --all, commands only see snapshots for the current folder,
// so undo in one project never restores another's files.
func scope(args []string) (ref, dir string, out io.Writer) {
	dir, _ = os.Getwd()
	for _, a := range args {
		if a == "--all" || a == "-a" {
			dir = ""
		} else if ref == "" {
			ref = a
		}
	}
	return ref, dir, os.Stdout
}

func list(dir string, out io.Writer) error {
	all, err := snaps()
	if err != nil {
		return err
	}
	all = forDir(all, dir)
	if len(all) == 0 && dir != "" {
		fmt.Fprintf(out, "no snapshots for %s (airbag list --all shows every folder)\n", dir)
	} else if len(all) == 0 {
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

func undo(ref, dir string, out io.Writer) error {
	s, err := find(ref, dir)
	if err != nil {
		return err
	}
	backup, err := restore(s)
	if err != nil {
		return err
	}
	for _, it := range s.Items {
		fmt.Fprintln(out, "restored", display(it.Path))
	}
	if backup.ID != "" {
		fmt.Fprintf(out, "previous state saved as %s (airbag undo %s to go back)\n", backup.ID, backup.ID)
	}
	return nil
}

func diff(ref, dir string, out io.Writer) error {
	s, err := find(ref, dir)
	if err != nil {
		return err
	}
	lines, err := changes(s)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "%s  %s\n", s.ID, s.Cmd)
	if len(lines) == 0 {
		fmt.Fprintln(out, "no changes")
	}
	for _, l := range lines {
		fmt.Fprintln(out, l[:9]+display(l[9:])) // "modified " + path
	}
	return nil
}

// display shortens paths under the current folder to relative ones.
func display(p string) string {
	if cwd, err := os.Getwd(); err == nil && within(p, cwd) {
		if rel, err := filepath.Rel(cwd, p); err == nil && rel != "." {
			return rel
		}
	}
	return p
}
