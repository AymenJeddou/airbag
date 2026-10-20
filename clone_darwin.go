package main

import (
	"io/fs"

	"golang.org/x/sys/unix"
)

// APFS clones are copy-on-write, so snapshots cost no extra space up front.
const cow = true

// fastClone clones a whole file or directory tree in one call.
func fastClone(src, dst string) error { return unix.Clonefile(src, dst, unix.CLONE_NOFOLLOW) }

func cloneFile(src, dst string, perm fs.FileMode) error {
	if unix.Clonefile(src, dst, unix.CLONE_NOFOLLOW) == nil {
		return nil
	}
	return copyFile(src, dst, perm)
}
