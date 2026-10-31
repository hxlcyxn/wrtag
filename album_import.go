package wrtag

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type albumFile struct {
	source    string
	name      string
	info      os.FileInfo
	copyOnly  bool
	protected bool
}

type albumIO struct {
	publish    func(string, string) error
	removeAll  func(string) error
	remove     func(string) error
	chmod      func(string, os.FileMode) error
	reportMode func(string)
	stage      func(string, string) (string, error)
	transfer   func(context.Context, FileSystemOperation, DirContext, string, string, os.FileMode) error
}

var albumGroupsMatch = func(os.FileInfo, os.FileInfo) bool { return true }

var errDirectoryPublicationUnsupported = errors.New("no-replace directory publication unsupported")
var publishAlbumDirectory = func(string, string) error { return errDirectoryPublicationUnsupported }

func defaultAlbumIO() albumIO {
	return albumIO{publish: publishAlbumDirectory, removeAll: os.RemoveAll, remove: os.Remove, chmod: os.Chmod, reportMode: func(string) {}, stage: os.MkdirTemp, transfer: func(ctx context.Context, op FileSystemOperation, dc DirContext, src, dest string, mode os.FileMode) error {
		return op.ProcessPath(ctx, dc, src, dest, mode)
	}}
}

func recoveryPrefix(dest string) string {
	name := strings.ToLower(filepath.Base(dest))
	sum := sha256.Sum256([]byte(name))
	readable := name
	if len(readable) > 80 {
		readable = strings.ToValidUTF8(readable[:80], "")
	}
	return fmt.Sprintf(".wrtag-%s-%x-", readable, sum[:8])
}

func FindAlbumRecoveryPaths(scope string) ([]string, error) {
	scope = filepath.Clean(scope)
	paths, err := associatedRecoveryPaths(scope)
	if err != nil {
		return nil, err
	}
	err = filepath.WalkDir(scope, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == scope && errors.Is(err, os.ErrNotExist) && len(paths) != 0 {
				return nil
			}
			return err
		}
		if d.IsDir() && isAlbumRecoveryName(d.Name()) {
			if !slices.Contains(paths, p) {
				paths = append(paths, p)
			}
			return filepath.SkipDir
		}
		return nil
	})
	slices.Sort(paths)
	return paths, err
}

func isAlbumRecoveryName(name string) bool {
	if !strings.HasPrefix(name, ".wrtag-") {
		return false
	}
	for _, marker := range []string{"-stage-", "-backup-"} {
		if i := strings.LastIndex(name, marker); i > len(".wrtag-") && i+len(marker) < len(name) {
			return true
		}
	}
	return false
}

func associatedRecoveryPaths(dest string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Dir(dest))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), recoveryPrefix(dest)) && isAlbumRecoveryName(e.Name()) {
			paths = append(paths, filepath.Join(filepath.Dir(dest), e.Name()))
		}
	}
	return paths, nil
}

func resolveAlbumPath(p string) (string, error) {
	p = filepath.Clean(p)
	resolved, err := filepath.EvalSymlinks(p)
	if err == nil {
		return resolved, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	parent := filepath.Dir(p)
	if parent == p {
		return "", err
	}
	parent, err = resolveAlbumPath(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(p)), nil
}

func validateAlbumOperation(op FileSystemOperation) error {
	switch op := op.(type) {
	case Move, Copy, Reflink:
		return nil
	case *Move:
		if op != nil {
			return nil
		}
	case *Copy:
		if op != nil {
			return nil
		}
	case *Reflink:
		if op != nil {
			return nil
		}
	}
	return fmt.Errorf("unsupported album filesystem operation: %T", op)
}

func importAlbum(ctx context.Context, op FileSystemOperation, src, dest string, files []albumFile, mode os.FileMode, prepare func(string) error, ops albumIO) (err error) {
	if err := validateAlbumOperation(op); err != nil {
		return err
	}
	src, err = resolveAlbumPath(src)
	if err != nil {
		return err
	}
	dest, err = resolveAlbumPath(dest)
	if err != nil {
		return err
	}
	if fileWithin(src, dest) || fileWithin(dest, src) {
		return fmt.Errorf("source/destination overlap: %s, %s", src, dest)
	}
	unlock := lockPaths(src, dest)
	defer unlock()
	for _, p := range []string{src, dest} {
		paths, err := associatedRecoveryPaths(p)
		if err != nil {
			return err
		}
		if len(paths) != 0 {
			return fmt.Errorf("in-progress or interrupted album operation; inspect recovery paths: %v", paths)
		}
	}
	if _, err := os.Lstat(dest); err == nil {
		return fmt.Errorf("album destination already exists: %s", dest)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	names := map[string]string{}
	for i := range files {
		f := &files[i]
		if f.name == "." || f.name == "" || filepath.Base(f.name) != f.name {
			return fmt.Errorf("invalid flat album path: %s", f.name)
		}
		key := strings.ToLower(f.name)
		if prev, ok := names[key]; ok {
			return fmt.Errorf("album asset collision: %s and %s", prev, f.source)
		}
		names[key] = f.source
		f.info, err = os.Lstat(f.source)
		if err != nil {
			return err
		}
		if !f.info.Mode().IsRegular() {
			return fmt.Errorf("selected asset is not a regular file: %s", f.source)
		}
	}
	if !op.CanModifyDest() {
		slog.InfoContext(ctx, "dry-run: prepare complete album before publication", "destination", dest)
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := mkdirAllFileMode(filepath.Dir(dest), mode); err != nil {
		return err
	}
	stage, err := ops.stage(filepath.Dir(dest), recoveryPrefix(dest)+"stage-*")
	if err != nil {
		return err
	}
	published := false
	defer func() {
		if _, e := os.Lstat(stage); errors.Is(e, os.ErrNotExist) {
			return
		}
		if e := ops.removeAll(stage); e != nil {
			cleanupErr := fmt.Errorf("staging cleanup incomplete; inspect %s: %w", stage, e)
			if published {
				cleanupErr = fmt.Errorf("destination complete at %s; %w", dest, cleanupErr)
			}
			err = errors.Join(err, cleanupErr)
		}
	}()
	transfer := FileSystemOperation(Copy{})
	switch op.(type) {
	case Reflink, *Reflink:
		transfer = Reflink{}
	}
	dc := NewDirContext()
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		target := filepath.Join(stage, f.name)
		fileTransfer := transfer
		if f.copyOnly {
			fileTransfer = Copy{}
		}
		if err := ops.transfer(ctx, fileTransfer, dc, f.source, target, mode); err != nil {
			return fmt.Errorf("prepare asset %s: %w", f.source, err)
		}
		if err := os.Chtimes(target, f.info.ModTime(), f.info.ModTime()); err != nil {
			return err
		}
	}
	protected := map[string][32]byte{}
	for _, f := range files {
		if !f.protected {
			continue
		}
		sum, err := albumAssetHash(filepath.Join(stage, f.name))
		if err != nil {
			return err
		}
		protected[f.name] = sum
	}
	if err := prepare(stage); err != nil {
		return err
	}
	for name, before := range protected {
		after, err := albumAssetHash(filepath.Join(stage, name))
		if err != nil {
			return err
		}
		if before != after {
			return fmt.Errorf("addon collision with kept asset: %s", name)
		}
	}
	entries, err := os.ReadDir(stage)
	if err != nil {
		return err
	}
	stagedNames := map[string]string{}
	for _, e := range entries {
		key := strings.ToLower(e.Name())
		if prev, ok := stagedNames[key]; ok {
			return fmt.Errorf("staged asset collision: %s and %s", prev, e.Name())
		}
		stagedNames[key] = e.Name()
		if !e.Type().IsRegular() {
			return fmt.Errorf("unsupported staged asset: %s", e.Name())
		}
		if err := finalizeAlbumAsset(stage, e.Name(), mode, ops); err != nil {
			return err
		}
	}
	requiredInfos := make(map[string]os.FileInfo, len(files))
	for _, f := range files {
		info, err := os.Lstat(filepath.Join(stage, f.name))
		if err != nil {
			return fmt.Errorf("required staged asset missing: %w", err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("invalid staged asset: %s", f.name)
		}
		for previousName, previous := range requiredInfos {
			if os.SameFile(previous, info) {
				return fmt.Errorf("required staged asset collision: %s and %s resolve to the same file", previousName, f.name)
			}
		}
		requiredInfos[f.name] = info
	}
	parent, err := os.Stat(filepath.Dir(stage))
	if err != nil {
		return err
	}
	dirMode := mode.Perm() | (mode.Perm()&0444)>>2 | parent.Mode()&os.ModeSetgid
	if err := ops.chmod(stage, dirMode); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := publishPreparedAlbum(ctx, stage, dest, ops); err != nil {
		return err
	}
	published = true
	switch op.(type) {
	case Move, *Move:
		if err := cleanAlbumSource(src, files, ops); err != nil {
			return fmt.Errorf("destination complete at %s; source cleanup incomplete: %w", dest, err)
		}
	}
	return nil
}

func fileWithin(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func publishPreparedAlbum(ctx context.Context, stage, dest string, ops albumIO) (err error) {
	fallback := false
	defer func() {
		if err != nil && fallback {
			err = fmt.Errorf("prepared per-file fallback publication failed (partial visibility and failed rollback possible); inspect %s: %w", dest, err)
		}
	}()
	err = ops.publish(stage, dest)
	if !errors.Is(err, errDirectoryPublicationUnsupported) {
		if err == nil {
			ops.reportMode("no-replace directory publication")
		}
		return err
	}
	fallback = true
	ops.reportMode("prepared per-file fallback: partial visibility and failed rollback possible")
	slog.WarnContext(ctx, "no-replace directory publication unsupported; publishing complete prepared album per-file; partial visibility and failed rollback possible", "destination", dest)
	info, err := os.Stat(stage)
	if err != nil {
		return err
	}
	if err := os.Mkdir(dest, info.Mode()); err != nil {
		return err
	}
	if err := ops.chmod(dest, info.Mode()); err != nil {
		return errors.Join(err, ops.remove(dest))
	}
	entries, err := os.ReadDir(stage)
	if err != nil {
		return errors.Join(err, ops.remove(dest))
	}
	var published []string
	for _, e := range entries {
		target := filepath.Join(dest, e.Name())
		if err := ctx.Err(); err != nil {
			return rollbackPreparedFiles(dest, published, err, ops)
		}
		if err := os.Link(filepath.Join(stage, e.Name()), target); err != nil {
			return rollbackPreparedFiles(dest, published, err, ops)
		}
		published = append(published, target)
	}
	return nil
}

func rollbackPreparedFiles(dest string, published []string, cause error, ops albumIO) error {
	errs := []error{cause}
	for _, p := range published {
		if err := ops.remove(p); err != nil {
			errs = append(errs, fmt.Errorf("publication cleanup incomplete; survivor %s: %w", p, err))
		}
	}
	if err := ops.remove(dest); err != nil {
		errs = append(errs, fmt.Errorf("publication cleanup incomplete; inspect %s: %w", dest, err))
	}
	return errors.Join(errs...)
}

func cleanAlbumSource(src string, files []albumFile, ops albumIO) error {
	var errs []error
	dirs := []string{src}
	for _, f := range files {
		if !fileWithin(src, f.source) {
			continue
		}
		info, err := os.Lstat(f.source)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !os.SameFile(f.info, info) || f.info.Size() != info.Size() || !f.info.ModTime().Equal(info.ModTime()) || f.info.Mode() != info.Mode() {
			errs = append(errs, fmt.Errorf("source changed; retained %s", f.source))
			continue
		}
		if err := ops.remove(f.source); err != nil {
			errs = append(errs, err)
		}
		if d := filepath.Dir(f.source); d != src && fileWithin(src, d) && !slices.Contains(dirs, d) {
			dirs = append(dirs, d)
		}
	}
	slices.SortFunc(dirs, func(a, b string) int { return len(b) - len(a) })
	for _, d := range dirs {
		entries, err := os.ReadDir(d)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if len(entries) != 0 {
			slog.Warn("unselected or changed source assets retained", "path", d)
			continue
		}
		if err := ops.remove(d); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func albumAssetHash(p string) ([32]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return [32]byte{}, err
	}
	h := sha256.New()
	_, copyErr := io.Copy(h, f)
	closeErr := f.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return [32]byte{}, err
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum, nil
}

func finalizeAlbumAsset(stage, name string, mode os.FileMode, ops albumIO) error {
	parent, err := os.Stat(stage)
	if err != nil {
		return err
	}
	asset := filepath.Join(stage, name)
	info, err := os.Lstat(asset)
	if err != nil {
		return err
	}
	if !albumGroupsMatch(parent, info) {
		if err := copyFile(asset, asset); err != nil {
			return fmt.Errorf("restage asset group %s: %w", asset, err)
		}
		info, err = os.Stat(asset)
		if err != nil {
			return err
		}
		if !albumGroupsMatch(parent, info) {
			return fmt.Errorf("staged asset group does not match album directory: %s", asset)
		}
	}
	return ops.chmod(asset, mode)
}
