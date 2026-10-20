package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// ponytail: fixed limits, make them config when someone hits them
var (
	maxFiles       = 200_000
	maxBytes int64 = 1 << 30 // only enforced where copies aren't copy-on-write
)

var errTooBig = errors.New("too big to snapshot")

type Item struct {
	Path  string `json:"path"`
	Saved string `json:"saved"` // name inside the snapshot dir
}

type Snap struct {
	ID    string    `json:"id"`
	Time  time.Time `json:"time"`
	Cmd   string    `json:"cmd"`
	Cwd   string    `json:"cwd"`
	Agent string    `json:"agent"`
	Items []Item    `json:"items"`
}

func storeDir() string {
	if d := os.Getenv("AIRBAG_HOME"); d != "" {
		return d
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".airbag")
}

func snapDir(id string) string { return filepath.Join(storeDir(), "snaps", id) }

const idLayout = "20060102-150405.000000"

// keepFor is how long snapshots live. ponytail: age only, add a size cap if stores grow too big
const keepFor = 7 * 24 * time.Hour

// prune deletes expired snapshots. IDs are UTC timestamps, so no manifest reads are needed.
func prune(now time.Time) {
	entries, _ := os.ReadDir(filepath.Join(storeDir(), "snaps"))
	for _, e := range entries {
		if t, err := time.Parse(idLayout, e.Name()); err == nil && now.Sub(t) > keepFor {
			os.RemoveAll(snapDir(e.Name()))
		}
	}
}

// takeSnap copies paths into a new snapshot described by meta.
func takeSnap(meta Snap, paths []string) (Snap, error) {
	if err := checkBudget(paths); err != nil {
		return meta, err
	}
	meta.Time = time.Now()
	meta.ID = meta.Time.UTC().Format(idLayout)
	dir := snapDir(meta.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return meta, err
	}
	for i, p := range paths {
		it := Item{Path: p, Saved: strconv.Itoa(i)}
		if err := cloneTree(p, filepath.Join(dir, it.Saved)); err != nil {
			os.RemoveAll(dir)
			return meta, fmt.Errorf("snapshot %s: %w", p, err)
		}
		meta.Items = append(meta.Items, it)
	}
	b, _ := json.MarshalIndent(meta, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), b, 0o600); err != nil {
		os.RemoveAll(dir)
		return meta, err
	}
	return meta, nil
}

func checkBudget(paths []string) error {
	files, size := 0, int64(0)
	for _, p := range paths {
		err := filepath.WalkDir(p, func(_ string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			files++
			if !cow && d.Type().IsRegular() {
				if fi, err := d.Info(); err == nil {
					size += fi.Size()
				}
			}
			if files > maxFiles || size > maxBytes {
				return errTooBig
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func cloneTree(src, dst string) error {
	if fastClone(src, dst) == nil {
		return nil
	}
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		t := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return os.MkdirAll(t, info.Mode().Perm()|0o700)
		case d.Type()&fs.ModeSymlink != 0:
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(link, t)
		case d.Type().IsRegular():
			if err := cloneFile(p, t, info.Mode().Perm()); err != nil {
				return err
			}
			return os.Chtimes(t, info.ModTime(), info.ModTime())
		}
		return nil // sockets, devices: nothing to restore
	})
}

func copyFile(src, dst string, perm fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// snaps returns all snapshots, newest first.
func snaps() ([]Snap, error) {
	entries, err := os.ReadDir(filepath.Join(storeDir(), "snaps"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Snap
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(snapDir(e.Name()), "manifest.json"))
		if err != nil {
			continue // half-written snapshot
		}
		var s Snap
		if json.Unmarshal(b, &s) == nil {
			out = append(out, s)
		}
	}
	slices.SortFunc(out, func(a, b Snap) int { return strings.Compare(b.ID, a.ID) })
	return out, nil
}

// find resolves "", "N" (Nth newest, 1-based) or an ID prefix among the snapshots for dir ("" = all).
func find(ref, dir string) (Snap, error) {
	all, err := snaps()
	if err != nil {
		return Snap{}, err
	}
	if ref == "" {
		ref = "1"
	}
	if n, err := strconv.Atoi(ref); err == nil && len(ref) < 4 {
		if mine := forDir(all, dir); n >= 1 && n <= len(mine) {
			return mine[n-1], nil
		}
	}
	for _, s := range all { // an explicit ID works from anywhere
		if strings.HasPrefix(s.ID, ref) {
			return s, nil
		}
	}
	if dir != "" {
		return Snap{}, fmt.Errorf("no snapshot %q for %s (see `airbag list --all`)", ref, dir)
	}
	return Snap{}, fmt.Errorf("no snapshot %q (see `airbag list`)", ref)
}

// forDir keeps snapshots that touch dir: something inside it, or a folder it lives in. "" keeps all.
func forDir(all []Snap, dir string) []Snap {
	if dir == "" {
		return all
	}
	var out []Snap
	for _, s := range all {
		if slices.ContainsFunc(s.Items, func(it Item) bool { return within(it.Path, dir) || within(dir, it.Path) }) {
			out = append(out, s)
		}
	}
	return out
}

// restore puts every item of s back, after snapshotting the current state so undo is undoable.
func restore(s Snap) (backup Snap, err error) {
	var current []string
	for _, it := range s.Items {
		if _, err := os.Lstat(it.Path); err == nil {
			current = append(current, it.Path)
		}
	}
	if len(current) > 0 {
		backup, err = takeSnap(Snap{Cmd: "airbag undo " + s.ID, Cwd: s.Cwd, Agent: "airbag"}, current)
		if errors.Is(err, errTooBig) {
			fmt.Fprintln(os.Stderr, "airbag: warning: current state too big to back up, restoring anyway")
			backup = Snap{}
		} else if err != nil {
			return backup, fmt.Errorf("backing up current state: %w", err)
		}
	}
	for _, it := range s.Items {
		if err := os.RemoveAll(it.Path); err != nil {
			return backup, err
		}
		if err := os.MkdirAll(filepath.Dir(it.Path), 0o755); err != nil {
			return backup, err
		}
		if err := cloneTree(filepath.Join(snapDir(s.ID), it.Saved), it.Path); err != nil {
			return backup, fmt.Errorf("restore %s: %w", it.Path, err)
		}
	}
	return backup, nil
}

// changes lists what differs between snapshot s and the disk now, as "deleted|modified|added PATH".
func changes(s Snap) ([]string, error) {
	var out []string
	for _, it := range s.Items {
		saved := filepath.Join(snapDir(s.ID), it.Saved)
		was, err := files(saved)
		if err != nil {
			return nil, err
		}
		now, err := files(it.Path)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		for rel := range was {
			p := filepath.Join(it.Path, rel)
			if _, ok := now[rel]; !ok {
				out = append(out, "deleted  "+p)
			} else if !sameFile(filepath.Join(saved, rel), p) {
				out = append(out, "modified "+p)
			}
		}
		for rel := range now {
			if _, ok := was[rel]; !ok {
				out = append(out, "added    "+filepath.Join(it.Path, rel))
			}
		}
	}
	slices.SortFunc(out, func(a, b string) int { return strings.Compare(a[9:], b[9:]) })
	return out, nil
}

// files maps the relative path of every non-directory under root ("." when root is a file).
func files(root string) (map[string]bool, error) {
	m := map[string]bool{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(root, p)
			m[rel] = true
		}
		return nil
	})
	return m, err
}

func sameFile(a, b string) bool {
	x, err1 := os.ReadFile(a)
	y, err2 := os.ReadFile(b)
	return err1 == nil && err2 == nil && bytes.Equal(x, y)
}
