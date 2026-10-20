//go:build !darwin && !linux

package main

import (
	"errors"
	"io/fs"
)

// ponytail: plain copies; ReFS block cloning (Windows Dev Drive) is the upgrade path
const cow = false

func fastClone(src, dst string) error { return errors.ErrUnsupported }

func cloneFile(src, dst string, perm fs.FileMode) error { return copyFile(src, dst, perm) }
