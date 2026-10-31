//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

func syncTimestamp(path string) error {
	if err := unix.Utimes(path, nil); err != nil {
		return &os.PathError{Op: "chtimes", Path: path, Err: err}
	}
	return nil
}
