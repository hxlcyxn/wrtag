//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris)

package main

import (
	"os"
	"time"
)

func syncTimestamp(path string) error {
	now := time.Now()
	return os.Chtimes(path, now, now)
}
