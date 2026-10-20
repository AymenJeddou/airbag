package main

import (
	"errors"
	"io/fs"
	"os"

	"golang.org/x/sys/unix"
)

// ponytail: assumes no reflink, so the byte budget applies; probe the filesystem if btrfs/XFS users hit it
const cow = false

func fastClone(src, dst string) error { return errors.ErrUnsupported }

// cloneFile reflinks on btrfs/XFS and falls back to a plain copy elsewhere.
func cloneFile(src, dst string, perm fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if unix.IoctlFileClone(int(out.Fd()), int(in.Fd())) == nil {
		return out.Close()
	}
	out.Close()
	os.Remove(dst)
	return copyFile(src, dst, perm)
}
