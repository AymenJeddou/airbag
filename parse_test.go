package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestAnalyze(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	for _, p := range []string{".git/HEAD", "a.txt", "b.txt", ".env", "src/main.go", "build/out", "clean.py"} {
		mk(t, filepath.Join(root, p))
	}
	mk(t, filepath.Join(home, "x/notes"))
	env := []string{"HOME=" + home, "OUT=build"}

	cases := []struct {
		cmd   string
		paths []string // relative to root, "~/..." relative to home
		root  bool     // expect the project-root fallback
	}{
		{cmd: "ls -la"},
		{cmd: "rm -rf src", paths: []string{"src"}},
		{cmd: "rm -rf src src/main.go", paths: []string{"src"}},
		{cmd: "rm *.txt", paths: []string{"a.txt", "b.txt"}},
		{cmd: "rm a.txt nope *.log", paths: []string{"a.txt"}},
		{cmd: "rm -f .env", paths: []string{".env"}},
		{cmd: "cd src && rm -rf ../build", paths: []string{"build"}},
		{cmd: "rm -rf ~/x", paths: []string{"~/x"}},
		{cmd: "rm -rf $HOME/x", paths: []string{"~/x"}},
		{cmd: `rm -rf "$OUT"`, paths: []string{"build"}},
		{cmd: "D=src; rm -rf $D", paths: []string{"src"}},
		{cmd: "sudo rm -rf build", paths: []string{"build"}},
		{cmd: "bash -c 'rm -rf src'", paths: []string{"src"}},
		{cmd: "sed 's/a/b/' a.txt"},
		{cmd: "sed -i 's/a/b/' a.txt", paths: []string{"a.txt"}},
		{cmd: "echo hi > a.txt", paths: []string{"a.txt"}},
		{cmd: "echo hi >> a.txt"},
		{cmd: "ls 2>/dev/null"},
		{cmd: "mv a.txt b.txt", paths: []string{"a.txt", "b.txt"}},
		{cmd: "cp a.txt b.txt", paths: []string{"b.txt"}},
		{cmd: "find . -name '*.go' -delete", paths: []string{"."}},
		{cmd: "git status"},
		{cmd: "git restore --staged a.txt"},
		{cmd: "git clean -fdx", paths: []string{"."}},
		{cmd: "git reset --hard HEAD~1", paths: []string{"."}},
		{cmd: "git checkout -- a.txt", paths: []string{"."}},
		{cmd: "cd src && git checkout .", paths: []string{"."}},
		{cmd: "rm -rf $(cat list)", root: true},
		{cmd: "rm -rf $UNSET_THING/", root: true},
		{cmd: "python clean.py", root: true},
		{cmd: "make clean", root: true},
		{cmd: "npm run clean", root: true},
		{cmd: "npm install"},
		{cmd: "echo $(rm -rf src)", paths: []string{"src"}, root: true},
		{cmd: "if [ -d build ; then", root: true}, // unparseable
	}
	for _, c := range cases {
		t.Run(c.cmd, func(t *testing.T) {
			got := Analyze(c.cmd, root, env)
			var want []string
			for _, p := range c.paths {
				if rel, ok := cutHome(p); ok {
					want = append(want, filepath.Join(home, rel))
				} else {
					want = append(want, filepath.Join(root, p))
				}
			}
			slices.Sort(want)
			if !slices.Equal(got.Paths, want) {
				t.Errorf("paths = %q, want %q", got.Paths, want)
			}
			if (got.Root != "") != c.root {
				t.Errorf("root = %q, want fallback=%v", got.Root, c.root)
			}
		})
	}
}

func TestOpaqueNeverSnapshotsHome(t *testing.T) {
	home := t.TempDir()
	if got := Analyze("make", home, []string{"HOME=" + home}); got.Root != "" {
		t.Errorf("root = %q, want none", got.Root)
	}
}

func cutHome(p string) (string, bool) {
	if len(p) > 1 && p[:2] == "~/" {
		return p[2:], true
	}
	return "", false
}

func mk(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(p), 0o644); err != nil {
		t.Fatal(err)
	}
}
