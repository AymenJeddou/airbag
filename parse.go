package main

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

// Plan is what a shell command puts at risk.
type Plan struct {
	Paths []string // existing paths the command may destroy, as bash would resolve them
	Root  string   // project root to snapshot when we can't tell what the command touches
}

func (p Plan) all() []string {
	if p.Root == "" {
		return p.Paths
	}
	return dedupe(append(slices.Clone(p.Paths), p.Root))
}

// Commands that run arbitrary code: we can't see what they delete.
var opaqueCmds = map[string]bool{
	"make": true, "just": true, "xargs": true, "eval": true, "source": true, ".": true,
	"python": true, "python3": true, "node": true, "ruby": true, "perl": true, "deno": true,
	"bash": true, "sh": true, "zsh": true, "fish": true, "pwsh": true,
}

// Package managers are opaque only when they run scripts.
var scriptRunners = map[string]bool{"npm": true, "pnpm": true, "yarn": true, "bun": true, "npx": true, "bunx": true}

// Prefixes that run the rest of the line as a command.
var wrappers = map[string]bool{"sudo": true, "doas": true, "env": true, "command": true, "nice": true, "nohup": true, "time": true, "exec": true, "timeout": true}

type analyzer struct {
	env  map[string]string
	dir  string
	plan Plan
}

// Analyze resolves which paths cmd would destroy if run in cwd.
func Analyze(cmd, cwd string, environ []string) Plan {
	a := &analyzer{env: map[string]string{}, dir: cwd}
	for _, kv := range environ {
		if k, v, ok := strings.Cut(kv, "="); ok {
			a.env[k] = v
		}
	}
	if a.env["HOME"] == "" {
		a.env["HOME"], _ = os.UserHomeDir()
	}
	a.run(cmd)
	a.plan.Paths = dedupe(a.plan.Paths)
	return a.plan
}

func (a *analyzer) run(cmd string) {
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(cmd), "")
	if err != nil {
		a.opaque()
		return
	}
	// ponytail: walks in source order and ignores subshell/if scoping for cd and assignments
	syntax.Walk(f, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.CallExpr:
			a.call(n)
		case *syntax.Redirect:
			if n.Op == syntax.RdrOut || n.Op == syntax.ClbOut || n.Op == syntax.RdrAll {
				if fs, ok := a.expand(n.Word); ok {
					a.add(fs...)
				}
			}
		}
		return true
	})
}

func (a *analyzer) cfg() *expand.Config {
	return &expand.Config{
		Env: expand.FuncEnviron(func(name string) string {
			if name == "PWD" {
				return a.dir
			}
			return a.env[name]
		}),
		ReadDir2: os.ReadDir,
	}
}

// expand returns ok=false when the words depend on something we can't know:
// $(...) output or a variable set only inside the agent's shell.
func (a *analyzer) expand(words ...*syntax.Word) ([]string, bool) {
	unknown := false
	for _, w := range words {
		syntax.Walk(w, func(n syntax.Node) bool {
			if p, ok := n.(*syntax.ParamExp); ok && p.Param != nil {
				name := p.Param.Value
				if _, set := a.env[name]; !set && name != "PWD" && !special(name) {
					unknown = true
				}
			}
			return true
		})
	}
	if unknown {
		return nil, false
	}
	fields, err := expand.Fields(a.cfg(), words...)
	return fields, err == nil
}

func special(name string) bool {
	return strings.Trim(name, "0123456789") == "" || strings.Contains("?#@*$!-", name)
}

func (a *analyzer) call(c *syntax.CallExpr) {
	if len(c.Args) == 0 { // D=build
		for _, as := range c.Assigns {
			if as.Value == nil {
				continue
			}
			if v, ok := a.expand(as.Value); ok {
				a.env[as.Name.Value] = strings.Join(v, " ")
			} else {
				a.opaque() // left unset, so later uses are unknown too
			}
		}
		return
	}
	args, ok := a.expand(c.Args...)
	if !ok {
		a.opaque()
		return
	}
	a.argv(args)
}

func (a *analyzer) argv(args []string) {
	for len(args) > 0 && wrappers[filepath.Base(args[0])] {
		w := filepath.Base(args[0])
		args = args[1:]
		for len(args) > 0 && (strings.HasPrefix(args[0], "-") || strings.Contains(args[0], "=")) {
			args = args[1:]
		}
		if w == "timeout" && len(args) > 0 {
			args = args[1:]
		}
	}
	if len(args) == 0 {
		return
	}
	name, rest := filepath.Base(args[0]), args[1:]
	switch name {
	case "cd":
		if len(rest) == 0 {
			a.dir = a.env["HOME"]
		} else {
			a.dir = a.abs(rest[0])
		}
	case "rm", "rmdir", "unlink", "shred", "truncate", "mv", "chmod", "chown", "chgrp", "tee":
		a.add(operands(rest)...)
	case "cp", "install", "ln", "rsync":
		if ops := operands(rest); len(ops) > 1 {
			a.add(ops[len(ops)-1])
		}
	case "dd":
		for _, s := range rest {
			if f, ok := strings.CutPrefix(s, "of="); ok {
				a.add(f)
			}
		}
	case "sed", "perl":
		if slices.ContainsFunc(rest, func(s string) bool { return strings.HasPrefix(s, "-i") || s == "--in-place" }) {
			a.add(operands(rest)...) // the script arg won't exist as a path, so it drops out
		} else if name == "perl" {
			a.opaque()
		}
	case "find":
		if slices.ContainsFunc(rest, func(s string) bool { return s == "-delete" || strings.HasPrefix(s, "-exec") || strings.HasPrefix(s, "-ok") }) {
			i := slices.IndexFunc(rest, func(s string) bool { return strings.HasPrefix(s, "-") || s == "(" || s == "!" })
			if i == -1 {
				i = len(rest)
			}
			if i == 0 {
				a.add(".")
			}
			a.add(rest[:i]...)
		}
	case "git":
		a.git(rest)
	case "bash", "sh", "zsh":
		if i := slices.Index(rest, "-c"); i >= 0 && i+1 < len(rest) {
			a.run(rest[i+1])
			return
		}
		a.opaque()
	default:
		if opaqueCmds[name] {
			a.opaque()
		} else if scriptRunners[name] && len(rest) > 0 && !slices.Contains([]string{"install", "i", "ci", "add", "ls", "list", "view", "info", "outdated", "--version", "-v"}, rest[0]) {
			a.opaque()
		}
	}
}

func (a *analyzer) git(args []string) {
	dir := a.dir
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		if (args[0] == "-C" || args[0] == "-c") && len(args) > 1 {
			if args[0] == "-C" {
				dir = a.abs(args[1])
			}
			args = args[1:]
		}
		args = args[1:]
	}
	if len(args) == 0 {
		return
	}
	sub, rest := args[0], args[1:]
	has := func(flags ...string) bool {
		return slices.ContainsFunc(rest, func(s string) bool { return slices.Contains(flags, s) })
	}
	destructive := false
	switch sub {
	case "clean", "rm":
		destructive = true
	case "reset":
		destructive = has("--hard", "--merge", "--keep")
	case "checkout":
		destructive = has("--", ".", "-f", "--force")
	case "restore":
		destructive = !has("--staged", "-S") || has("--worktree", "-W")
	case "switch":
		destructive = has("-f", "--force", "--discard-changes")
	}
	if destructive {
		a.plan.Paths = append(a.plan.Paths, projectRoot(dir))
	}
}

func (a *analyzer) opaque() {
	root := projectRoot(a.dir)
	// Snapshotting all of $HOME or / per command is not an option; dcg's job, not ours.
	if root == filepath.Clean(a.env["HOME"]) || root == filepath.Dir(root) {
		return
	}
	a.plan.Root = root
}

func (a *analyzer) add(paths ...string) {
	for _, p := range paths {
		p = a.abs(p)
		if fi, err := os.Lstat(p); err == nil && (fi.Mode().IsRegular() || fi.IsDir() || fi.Mode()&os.ModeSymlink != 0) {
			a.plan.Paths = append(a.plan.Paths, p)
		}
	}
}

var msysDrive = regexp.MustCompile(`^/([a-zA-Z])(/|$)`)

func (a *analyzer) abs(p string) string {
	if runtime.GOOS == "windows" { // Git Bash spells C:\x as /c/x
		p = msysDrive.ReplaceAllString(p, "$1:/")
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(a.dir, p)
	}
	return filepath.Clean(p)
}

// operands drops flags. ponytail: doesn't know which flags take a value (chmod -R 755 x keeps "755", which then fails to exist).
func operands(args []string) []string {
	var out []string
	dashdash := false
	for _, s := range args {
		if !dashdash && s == "--" {
			dashdash = true
			continue
		}
		if dashdash || !strings.HasPrefix(s, "-") || s == "-" {
			out = append(out, s)
		}
	}
	return out
}

// projectRoot is the enclosing git work tree, or dir itself.
func projectRoot(dir string) string {
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		if filepath.Dir(d) == d {
			return dir
		}
	}
}

// dedupe sorts and drops paths already covered by an ancestor in the list.
func dedupe(paths []string) []string {
	slices.Sort(paths) // ancestors sort before descendants
	var out []string
	for _, p := range paths {
		if !slices.ContainsFunc(out, func(d string) bool { return within(p, d) }) {
			out = append(out, p)
		}
	}
	return out
}

func within(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
