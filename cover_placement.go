package wrtag

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type coverFileOps struct {
	copy   func(io.Writer, io.Reader) (int64, error)
	chmod  func(*os.File, os.FileMode) error
	close  func(*os.File) error
	rename func(string, string) error
	remove func(string) error
}

func downloadedCoverFileOps() coverFileOps {
	return coverFileOps{
		copy:   io.Copy,
		chmod:  (*os.File).Chmod,
		close:  (*os.File).Close,
		rename: os.Rename,
		remove: os.Remove,
	}
}

func removeDownloadedCover(path string, remove func(string) error) error {
	if err := remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove downloaded cover %q: %w", path, err)
	}
	return nil
}

func placeDownloadedCover(src, dest string, mode os.FileMode, ops coverFileOps) (retErr error) {
	defer func() {
		retErr = errors.Join(retErr, removeDownloadedCover(src, ops.remove))
	}()

	if err := mkdirAllFileMode(filepath.Dir(dest), mode); err != nil {
		return fmt.Errorf("create cover destination: %w", err)
	}
	source, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open downloaded cover: %w", err)
	}
	defer func() {
		if err := source.Close(); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("close downloaded cover: %w", err))
		}
	}()

	stage, err := os.CreateTemp(filepath.Dir(dest), ".wrtag-cover-stage-*")
	if err != nil {
		return fmt.Errorf("create cover stage: %w", err)
	}
	closed := false
	defer func() {
		if !closed {
			if err := ops.close(stage); err != nil {
				retErr = errors.Join(retErr, fmt.Errorf("close cover stage: %w", err))
			}
		}
		if err := ops.remove(stage.Name()); err != nil && !errors.Is(err, os.ErrNotExist) {
			retErr = errors.Join(retErr, fmt.Errorf("remove cover stage: %w", err))
		}
	}()

	if _, err := ops.copy(stage, source); err != nil {
		return fmt.Errorf("copy cover stage: %w", err)
	}
	if err := ops.chmod(stage, mode); err != nil {
		return fmt.Errorf("chmod cover stage: %w", err)
	}
	closed = true
	if err := ops.close(stage); err != nil {
		return fmt.Errorf("close cover stage: %w", err)
	}
	if err := ops.rename(stage.Name(), dest); err != nil {
		return fmt.Errorf("publish cover: %w", err)
	}
	return nil
}
