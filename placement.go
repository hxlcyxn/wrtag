package wrtag

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/KarpelesLab/reflink"
)

type filePlacement struct {
	transfer func(dest, src *os.File) error
	chmod    func(*os.File, os.FileMode) error
	close    func(*os.File) error
	rename   func(string, string) error
	remove   func(string) error
}

func newFilePlacement(transfer func(dest, src *os.File) error) filePlacement {
	return filePlacement{
		transfer: transfer,
		chmod:    (*os.File).Chmod,
		close:    (*os.File).Close,
		rename:   os.Rename,
		remove:   os.Remove,
	}
}

func copyContents(dest, src *os.File) error {
	_, err := io.Copy(dest, src)
	return err
}

func reflinkContents(dest, src *os.File) error {
	return reflink.Reflink(dest, src, false)
}

func (p filePlacement) move(src, dest string, mode os.FileMode) error {
	info, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("stat source: %w", err)
	}

	destInfo, err := os.Stat(dest)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat destination: %w", err)
	}
	if err == nil && os.SameFile(info, destInfo) {
		return fmt.Errorf("source and destination refer to the same file: %s", dest)
	}

	if info.Mode().Perm() == mode.Perm() {
		if err := p.rename(src, dest); err == nil {
			return nil
		} else if !errors.Is(err, syscall.EXDEV) {
			return fmt.Errorf("rename: %w", err)
		}
	}

	if err := p.stage(src, dest, mode); err != nil {
		return err
	}
	if err := p.remove(src); err != nil {
		return fmt.Errorf("remove source after publication: %w", err)
	}
	return nil
}

func (p filePlacement) stage(src, dest string, mode os.FileMode) (err error) {
	source, err := os.Open(filepath.Clean(src))
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer source.Close()

	tmp, err := os.CreateTemp(filepath.Dir(dest), ".wrtag-*")
	if err != nil {
		return fmt.Errorf("create staging file: %w", err)
	}
	path := tmp.Name()
	defer func() {
		if tmp != nil {
			err = errors.Join(err, tmp.Close())
		}
		if err != nil {
			if removeErr := p.remove(path); removeErr != nil {
				err = errors.Join(err, fmt.Errorf("remove staging file: %w", removeErr))
			}
		}
	}()

	if err := p.transfer(tmp, source); err != nil {
		return fmt.Errorf("stage contents: %w", err)
	}
	if err := p.chmod(tmp, mode); err != nil {
		return fmt.Errorf("chmod staging file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync staging file: %w", err)
	}
	closeErr := p.close(tmp)
	tmp = nil
	if closeErr != nil {
		return fmt.Errorf("close staging file: %w", closeErr)
	}
	if err := p.rename(path, dest); err != nil {
		return fmt.Errorf("publish staging file: %w", err)
	}
	return nil
}
