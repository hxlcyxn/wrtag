package wrtag

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func albumFixture(t *testing.T) (string, string, []albumFile) {
	t.Helper()
	root := t.TempDir()
	src, dest := filepath.Join(root, "input"), filepath.Join(root, "output")
	require.NoError(t, os.Mkdir(src, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(src, "track"), []byte("original"), 0644))
	return src, dest, []albumFile{{source: filepath.Join(src, "track"), name: "track"}}
}

func TestAlbumPreparationFailurePreservesSource(t *testing.T) {
	t.Parallel()
	for _, boundary := range []string{"tag", "addon", "chmod", "publication", "cancel"} {
		t.Run(boundary, func(t *testing.T) {
			t.Parallel()
			src, dest, files := albumFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ops := defaultAlbumIO()
			boom := errors.New(boundary)
			prepare := func(stage string) error {
				require.FileExists(t, files[0].source)
				require.NoFileExists(t, dest)
				require.NoError(t, os.WriteFile(filepath.Join(stage, "track"), []byte("tagged"), 0600))
				if boundary == "tag" || boundary == "addon" {
					return boom
				}
				if boundary == "cancel" {
					cancel()
				}
				return nil
			}
			if boundary == "chmod" {
				ops.chmod = func(string, os.FileMode) error { return boom }
			}
			if boundary == "publication" {
				ops.publish = func(string, string) error { return boom }
			}
			err := importAlbum(ctx, NewMove(false), src, dest, files, 0660, prepare, ops)
			require.Error(t, err)
			b, e := os.ReadFile(files[0].source)
			require.NoError(t, e)
			require.Equal(t, "original", string(b))
			require.NoFileExists(t, dest)
			paths, e := FindAlbumRecoveryPaths(filepath.Dir(src))
			require.NoError(t, e)
			require.Empty(t, paths)
			unlock := lockPaths(src, dest)
			unlock()
		})
	}
}

func TestAlbumCompletePublication(t *testing.T) {
	t.Parallel()
	for _, fallback := range []bool{false, true} {
		t.Run(map[bool]string{true: "fallback", false: "directory"}[fallback], func(t *testing.T) {
			t.Parallel()
			src, dest, files := albumFixture(t)
			ops := defaultAlbumIO()
			if fallback {
				ops.publish = func(string, string) error { return errDirectoryPublicationUnsupported }
			}
			err := importAlbum(context.Background(), NewMove(false), src, dest, files, 0660, func(stage string) error {
				require.DirExists(t, src)
				require.NoError(t, os.WriteFile(filepath.Join(stage, "lyrics"), []byte("addon"), 0600))
				return os.WriteFile(filepath.Join(stage, "track"), []byte("tagged"), 0600)
			}, ops)
			require.NoError(t, err)
			require.NoDirExists(t, src)
			for _, name := range []string{"track", "lyrics"} {
				info, e := os.Stat(filepath.Join(dest, name))
				require.NoError(t, e)
				require.Equal(t, os.FileMode(0660), info.Mode().Perm())
			}
			paths, e := FindAlbumRecoveryPaths(filepath.Dir(dest))
			require.NoError(t, e)
			require.Empty(t, paths)
		})
	}
}

func TestAlbumPreflight(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"existing", "same-path", "collision", "overlap", "recovery", "nested"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			src, dest, files := albumFixture(t)
			switch kind {
			case "existing":
				require.NoError(t, os.Mkdir(dest, 0755))
			case "same-path":
				dest = src
			case "collision":
				files = append(files, albumFile{source: files[0].source, name: "TRACK"})
			case "overlap":
				dest = filepath.Join(src, "child")
			case "recovery":
				require.NoError(t, os.Mkdir(filepath.Join(filepath.Dir(dest), recoveryPrefix(dest)+"stage-orphan"), 0755))
			case "nested":
				files[0].name = "disc/track"
			}
			err := importAlbum(context.Background(), NewMove(false), src, dest, files, 0660, func(string) error { t.Fatal("prepare called"); return nil }, defaultAlbumIO())
			require.Error(t, err)
			require.FileExists(t, files[0].source)
		})
	}
}

func TestAlbumDryRun(t *testing.T) {
	t.Parallel()
	src, dest, files := albumFixture(t)
	require.NoError(t, importAlbum(context.Background(), NewMove(true), src, dest, files, 0660, func(string) error { t.Fatal("prepare called"); return nil }, defaultAlbumIO()))
	require.NoDirExists(t, dest)
	paths, e := FindAlbumRecoveryPaths(filepath.Dir(src))
	require.NoError(t, e)
	require.Empty(t, paths)
}

func TestAlbumNoReplaceRace(t *testing.T) {
	t.Parallel()
	src, dest, files := albumFixture(t)
	err := importAlbum(context.Background(), NewMove(false), src, dest, files, 0660, func(string) error {
		require.NoError(t, os.Mkdir(dest, 0755))
		return os.WriteFile(filepath.Join(dest, "concurrent"), []byte("other writer"), 0644)
	}, defaultAlbumIO())
	require.Error(t, err)
	require.FileExists(t, filepath.Join(dest, "concurrent"))
	require.NoFileExists(t, filepath.Join(dest, "track"))
	require.FileExists(t, files[0].source)
}

func TestAlbumChangedSourceAndExtrasRetained(t *testing.T) {
	t.Parallel()
	for _, changed := range []bool{false, true} {
		src, dest, files := albumFixture(t)
		err := importAlbum(context.Background(), NewMove(false), src, dest, files, 0660, func(string) error {
			if changed {
				return os.WriteFile(files[0].source, []byte("changed original"), 0644)
			}
			return os.WriteFile(filepath.Join(src, "extra"), []byte("unselected"), 0644)
		}, defaultAlbumIO())
		if changed {
			require.ErrorContains(t, err, "destination complete")
			require.ErrorContains(t, err, "source cleanup incomplete")
		} else {
			require.NoError(t, err)
		}
		require.FileExists(t, filepath.Join(dest, "track"))
		if changed {
			require.FileExists(t, files[0].source)
		} else {
			require.FileExists(t, filepath.Join(src, "extra"))
		}
	}
}

func TestFindAlbumRecoveryPaths(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "artist", recoveryPrefix("album")+"backup-orphan")
	require.NoError(t, os.MkdirAll(path, 0755))
	paths, err := FindAlbumRecoveryPaths(root)
	require.NoError(t, err)
	require.Equal(t, []string{path}, paths)
	other := t.TempDir()
	if err := os.Symlink(root, filepath.Join(other, "alias")); err == nil {
		paths, err = FindAlbumRecoveryPaths(other)
		require.NoError(t, err)
		require.Empty(t, paths)
	}
}

func TestAlbumCleanupFailureReported(t *testing.T) {
	t.Parallel()
	src, dest, files := albumFixture(t)
	ops := defaultAlbumIO()
	ops.removeAll = func(string) error { return errors.New("denied cleanup") }
	err := importAlbum(context.Background(), NewCopy(false), src, dest, files, 0660, func(string) error { return errors.New("addon failed") }, ops)
	require.ErrorContains(t, err, "staging cleanup incomplete")
	paths, e := FindAlbumRecoveryPaths(filepath.Dir(dest))
	require.NoError(t, e)
	require.Len(t, paths, 1)
	require.FileExists(t, files[0].source)
}

func TestFindAlbumRecoveryPathsIncludesScopeSiblings(t *testing.T) {
	t.Parallel()
	for _, exists := range []bool{false, true} {
		root := t.TempDir()
		scope := filepath.Join(root, "album")
		if exists {
			require.NoError(t, os.Mkdir(scope, 0755))
		}
		recovery := filepath.Join(root, recoveryPrefix(scope)+"backup-interrupted")
		require.NoError(t, os.Mkdir(recovery, 0755))
		paths, err := FindAlbumRecoveryPaths(scope)
		require.NoError(t, err)
		require.Equal(t, []string{recovery}, paths)
	}
}

func TestAlbumStagedAssetCollisions(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"casefold", "kept"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			src, dest, files := albumFixture(t)
			files[0].protected = kind == "kept"
			err := importAlbum(context.Background(), NewMove(false), src, dest, files, 0660, func(stage string) error {
				name := "TRACK"
				if kind == "kept" {
					name = "track"
				}
				return os.WriteFile(filepath.Join(stage, name), []byte("addon collision"), 0600)
			}, defaultAlbumIO())
			require.ErrorContains(t, err, "collision")
			require.NoDirExists(t, dest)
			content, e := os.ReadFile(files[0].source)
			require.NoError(t, e)
			require.Equal(t, "original", string(content))
		})
	}
}

func TestAlbumTransferFailures(t *testing.T) {
	t.Parallel()
	for _, boundary := range []string{"staging", "copy", "reflink", "close"} {
		t.Run(boundary, func(t *testing.T) {
			t.Parallel()
			src, dest, files := albumFixture(t)
			ops := defaultAlbumIO()
			boom := errors.New(boundary + " failed")
			if boundary == "staging" {
				ops.stage = func(string, string) (string, error) { return "", boom }
			} else {
				ops.transfer = func(_ context.Context, _ FileSystemOperation, _ DirContext, _, target string, _ os.FileMode) error {
					require.NoError(t, os.WriteFile(target, []byte("partial"), 0600))
					return boom
				}
			}
			op := FileSystemOperation(NewMove(false))
			if boundary == "reflink" {
				op = NewReflink(false)
			}
			err := importAlbum(context.Background(), op, src, dest, files, 0660, func(string) error { t.Fatal("preparation called"); return nil }, ops)
			require.ErrorIs(t, err, boom)
			require.FileExists(t, files[0].source)
			require.NoDirExists(t, dest)
			paths, e := FindAlbumRecoveryPaths(filepath.Dir(src))
			require.NoError(t, e)
			require.Empty(t, paths)
		})
	}
}

func TestFindAlbumRecoveryPathsMissingScope(t *testing.T) {
	t.Parallel()
	paths, err := FindAlbumRecoveryPaths(filepath.Join(t.TempDir(), "missing"))
	require.ErrorIs(t, err, os.ErrNotExist)
	require.Empty(t, paths)
}

func TestAlbumPublishedStagingCleanupFailure(t *testing.T) {
	t.Parallel()
	src, dest, files := albumFixture(t)
	ops := defaultAlbumIO()
	ops.publish = func(string, string) error { return errDirectoryPublicationUnsupported }
	ops.removeAll = func(string) error { return errors.New("cleanup denied") }
	err := importAlbum(context.Background(), NewCopy(false), src, dest, files, 0660, func(string) error { return nil }, ops)
	require.ErrorContains(t, err, "destination complete")
	require.ErrorContains(t, err, "staging cleanup incomplete")
	require.FileExists(t, filepath.Join(dest, "track"))
	require.FileExists(t, files[0].source)
}

type unsupportedAlbumOperation struct{ Move }

func TestAlbumUnsupportedOperationPreservesOriginals(t *testing.T) {
	t.Parallel()
	wrapper := unsupportedAlbumOperation{Move: NewMove(false)}
	for _, op := range []FileSystemOperation{wrapper, &wrapper, nil, (*Move)(nil), (*Copy)(nil), (*Reflink)(nil)} {
		src, dest, files := albumFixture(t)
		ops := defaultAlbumIO()
		ops.stage = func(string, string) (string, error) { t.Fatal("unsupported operation created staging"); return "", nil }
		err := importAlbum(context.Background(), op, src, dest, files, 0660, func(string) error { t.Fatal("unsupported operation prepared album"); return nil }, ops)
		require.ErrorContains(t, err, "unsupported album filesystem operation")
		content, err := os.ReadFile(files[0].source)
		require.NoError(t, err)
		require.Equal(t, "original", string(content))
		require.NoDirExists(t, dest)
		paths, err := FindAlbumRecoveryPaths(filepath.Dir(src))
		require.NoError(t, err)
		require.Empty(t, paths)
	}
}

func TestProcessDirRejectsUnsupportedOperationBeforePreparation(t *testing.T) {
	t.Parallel()
	src, _, files := albumFixture(t)
	result, err := ProcessDir(context.Background(), &Config{}, unsupportedAlbumOperation{Move: NewMove(false)}, src, Always, "")
	require.Nil(t, result)
	require.ErrorContains(t, err, "unsupported album filesystem operation")
	content, err := os.ReadFile(files[0].source)
	require.NoError(t, err)
	require.Equal(t, "original", string(content))
}

func TestAlbumBuiltinOperationValuesAndPointers(t *testing.T) {
	t.Parallel()
	move, copyOp, reflink := NewMove(true), NewCopy(true), NewReflink(true)
	for _, op := range []FileSystemOperation{move, &move, copyOp, &copyOp, reflink, &reflink} {
		src, dest, files := albumFixture(t)
		require.NoError(t, importAlbum(context.Background(), op, src, dest, files, 0660, func(string) error { t.Fatal("dry-run preparation"); return nil }, defaultAlbumIO()))
		require.FileExists(t, files[0].source)
		require.NoDirExists(t, dest)
	}
}

func TestAlbumRequiredStagedInodeCollisionPreservesOriginals(t *testing.T) {
	t.Parallel()
	src, dest, files := albumFixture(t)
	second := filepath.Join(src, "second")
	require.NoError(t, os.WriteFile(second, []byte("second original"), 0644))
	files = append(files, albumFile{source: second, name: "second"})
	err := importAlbum(context.Background(), NewMove(false), src, dest, files, 0660, func(stage string) error {
		first := filepath.Join(stage, files[0].name)
		second := filepath.Join(stage, files[1].name)
		require.NoError(t, os.Remove(second))
		return os.Link(first, second)
	}, defaultAlbumIO())
	require.ErrorContains(t, err, "required staged asset collision: track and second")
	for i, want := range []string{"original", "second original"} {
		content, err := os.ReadFile(files[i].source)
		require.NoError(t, err)
		require.Equal(t, want, string(content))
	}
	require.NoDirExists(t, dest)
	paths, err := FindAlbumRecoveryPaths(filepath.Dir(src))
	require.NoError(t, err)
	require.Empty(t, paths)
}

func TestAlbumRequiredUnicodeNormalizedAliasPreservesOriginals(t *testing.T) {
	t.Parallel()
	src, dest, files := albumFixture(t)
	names := []string{"\u00e9.flac", "e\u0301.flac"}
	probe := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(probe, names[0]), []byte("probe"), 0600))
	first, err := os.Lstat(filepath.Join(probe, names[0]))
	require.NoError(t, err)
	second, err := os.Lstat(filepath.Join(probe, names[1]))
	if errors.Is(err, os.ErrNotExist) {
		t.Skip("filesystem does not alias Unicode-normalized names")
	}
	require.NoError(t, err)
	if !os.SameFile(first, second) {
		t.Skip("filesystem does not alias Unicode-normalized names")
	}
	files[0].name = names[0]
	other := filepath.Join(src, "second")
	require.NoError(t, os.WriteFile(other, []byte("second original"), 0644))
	files = append(files, albumFile{source: other, name: names[1]})
	err = importAlbum(context.Background(), NewMove(false), src, dest, files, 0660, func(string) error { return nil }, defaultAlbumIO())
	require.ErrorContains(t, err, "required staged asset collision")
	for i, want := range []string{"original", "second original"} {
		content, err := os.ReadFile(files[i].source)
		require.NoError(t, err)
		require.Equal(t, want, string(content))
	}
	require.NoDirExists(t, dest)
	paths, err := FindAlbumRecoveryPaths(filepath.Dir(src))
	require.NoError(t, err)
	require.Empty(t, paths)
}
