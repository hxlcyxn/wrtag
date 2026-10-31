package wrtag

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"go.senan.xyz/wrtag/coverparse"
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
	rename     func(string, string) error
	link       func(string, string) error
	reportMode func(string)
	stage      func(string, string) (string, error)
	transfer   func(context.Context, FileSystemOperation, DirContext, string, string, os.FileMode) error
}

var albumGroupsMatch = func(os.FileInfo, os.FileInfo) bool { return true }

var errDirectoryPublicationUnsupported = errors.New("no-replace directory publication unsupported")
var publishAlbumDirectory = func(string, string) error { return errDirectoryPublicationUnsupported }

func defaultAlbumIO() albumIO {
	return albumIO{publish: publishAlbumDirectory, removeAll: os.RemoveAll, remove: os.Remove, chmod: os.Chmod, rename: os.Rename, link: os.Link, reportMode: func(string) {}, stage: os.MkdirTemp, transfer: func(ctx context.Context, op FileSystemOperation, dc DirContext, src, dest string, mode os.FileMode) error {
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
	sourceInfo, err := os.Stat(src)
	if err != nil {
		return err
	}
	destInfo, statErr := os.Stat(dest)
	exists := statErr == nil
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	same := exists && os.SameFile(sourceInfo, destInfo)
	if !same && (fileWithin(src, dest) || fileWithin(dest, src)) {
		return fmt.Errorf("source/destination overlap: %s, %s", src, dest)
	}
	var unlock func()
	if same {
		unlock = lockPaths(dest)
	} else {
		unlock = lockPaths(src, dest)
	}
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

	names := map[string]string{}
	for i := range files {
		f := &files[i]
		parent, resolveErr := resolveAlbumPath(filepath.Dir(f.source))
		if resolveErr != nil {
			return resolveErr
		}
		f.source = filepath.Join(parent, filepath.Base(f.source))
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
	var oldAssets map[string]os.FileInfo
	if exists {
		if !destInfo.IsDir() {
			return fmt.Errorf("destination is not an album directory: %s", dest)
		}
		oldAssets, err = inventoryDestination(src, dest, files, same)
		if err != nil {
			return err
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
	if exists {
		if err := verifyDestinationInventory(dest, oldAssets); err != nil {
			return err
		}
		if err := replacePreparedAlbum(ctx, stage, dest, ops); err != nil {
			return err
		}
	} else if err := publishPreparedAlbum(ctx, stage, dest, ops); err != nil {
		return err
	}
	published = true
	if same {
		return nil
	}
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
	var published []publishedAlbumPath
	rootInfo, err := os.Lstat(dest)
	if err != nil {
		return err
	}
	published = append(published, publishedAlbumPath{dest, rootInfo})
	err = filepath.WalkDir(stage, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == stage {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(stage, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dest, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			if err := os.Mkdir(target, info.Mode()); err != nil {
				return err
			}
		case info.Mode().IsRegular():
			if err := ops.link(p, target); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported publication asset: %s", p)
		}
		created, err := os.Lstat(target)
		if err != nil {
			return err
		}
		published = append(published, publishedAlbumPath{target, created})
		if d.IsDir() {
			return ops.chmod(target, info.Mode())
		}
		return nil
	})
	if err != nil {
		return rollbackPreparedFiles(dest, published, err, ops)
	}
	return nil
}

type publishedAlbumPath struct {
	path string
	info os.FileInfo
}

func rollbackPreparedFiles(_ string, published []publishedAlbumPath, cause error, ops albumIO) error {
	errs := []error{cause}
	for _, p := range slices.Backward(published) {
		info, err := os.Lstat(p.path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err == nil && !os.SameFile(p.info, info) {
			err = errors.New("path changed after publication")
		}
		if err == nil {
			err = ops.remove(p.path)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("publication cleanup incomplete; survivor %s: %w", p.path, err))
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

func inventoryDestination(src, dest string, files []albumFile, same bool) (map[string]os.FileInfo, error) {
	inventory := map[string]os.FileInfo{}
	outputNames := map[string]bool{}
	selected := map[string]bool{}
	hasCover := false
	for _, f := range files {
		outputNames[f.name] = true
		selected[filepath.Clean(f.source)] = true
		if coverparse.IsCover(f.name) {
			hasCover = true
		}
	}
	var losses []string
	oldCover := ""
	consumed := map[string]string{}
	err := filepath.WalkDir(dest, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == dest {
			return nil
		}
		rel, err := filepath.Rel(dest, p)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		inventory[rel] = info
		if d.IsDir() {
			if filepath.Dir(rel) != "." {
				losses = append(losses, p)
				return filepath.SkipDir
			}
			return nil
		}
		accounted := info.Mode().IsRegular() && ((same && selected[filepath.Join(src, rel)]) || (!same && outputNames[filepath.Base(rel)]))
		if accounted && !same {
			name := filepath.Base(rel)
			if previous, ok := consumed[name]; ok {
				losses = append(losses, previous, p)
			}
			consumed[name] = p
		}
		if accounted && coverparse.IsCover(p) {
			if oldCover != "" {
				losses = append(losses, oldCover)
			}
			oldCover = p
		}
		if !accounted && info.Mode().IsRegular() && coverparse.IsCover(p) && hasCover && oldCover == "" {
			oldCover = p
			accounted = true
		}
		if !accounted {
			losses = append(losses, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for rel, info := range inventory {
		if !info.IsDir() {
			continue
		}
		supported := false
		for _, f := range files {
			if same && filepath.Dir(f.source) == filepath.Join(src, rel) {
				supported = true
			}
			if !same {
				for child, childInfo := range inventory {
					if childInfo.Mode().IsRegular() && filepath.Dir(child) == rel && outputNames[filepath.Base(child)] {
						supported = true
					}
				}
			}
		}
		if !supported {
			losses = append(losses, filepath.Join(dest, rel))
		}
	}
	if len(losses) != 0 {
		slices.Sort(losses)
		return nil, fmt.Errorf("unaccounted destination asset loss; select or move these assets before importing: %v", losses)
	}
	return inventory, nil
}

func verifyDestinationInventory(dest string, inventory map[string]os.FileInfo) error {
	count := 0
	err := filepath.WalkDir(dest, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == dest {
			return nil
		}
		rel, err := filepath.Rel(dest, p)
		if err != nil {
			return err
		}
		old, ok := inventory[rel]
		if !ok {
			return fmt.Errorf("destination changed during preparation; retained %s", p)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !os.SameFile(old, info) || old.Size() != info.Size() || !old.ModTime().Equal(info.ModTime()) || old.Mode() != info.Mode() {
			return fmt.Errorf("destination changed during preparation; retained %s", p)
		}
		count++
		return nil
	})
	if err != nil {
		return err
	}
	if count != len(inventory) {
		return fmt.Errorf("destination changed during preparation: %s", dest)
	}
	return nil
}

func replacePreparedAlbum(ctx context.Context, stage, dest string, ops albumIO) error {
	backup, err := os.MkdirTemp(filepath.Dir(dest), recoveryPrefix(dest)+"backup-*")
	if err != nil {
		return err
	}
	if err := ops.remove(backup); err != nil {
		return fmt.Errorf("backup reservation cleanup incomplete; inspect %s: %w", backup, err)
	}
	if err := ops.rename(dest, backup); err != nil {
		return fmt.Errorf("move destination %s to backup %s: %w", dest, backup, err)
	}
	if err := publishPreparedAlbum(ctx, stage, dest, ops); err != nil {
		restoreErr := publishPreparedAlbum(context.WithoutCancel(ctx), backup, dest, ops)
		if restoreErr != nil {
			return errors.Join(err, fmt.Errorf("restoration failed; inspect destination %s and original backup %s: %w", dest, backup, restoreErr))
		}
		if cleanupErr := ops.removeAll(backup); cleanupErr != nil {
			return errors.Join(err, fmt.Errorf("original restored; backup cleanup incomplete; inspect %s: %w", backup, cleanupErr))
		}
		return fmt.Errorf("publication failed; original destination restored at %s: %w", dest, err)
	}
	if err := ops.removeAll(backup); err != nil {
		return fmt.Errorf("destination complete at %s; backup cleanup incomplete; inspect %s: %w", dest, backup, err)
	}
	return nil
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
