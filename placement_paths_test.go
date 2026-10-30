package wrtag

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMatchingModeMoveUsesOriginalInode(t *testing.T) {
	t.Parallel()

	src, dest := placementFixture(t)
	before, err := os.Stat(src)
	require.NoError(t, err)
	p := newFilePlacement(func(*os.File, *os.File) error {
		t.Fatal("matching mode move must not stage a copy")
		return nil
	})
	p.chmod = func(*os.File, os.FileMode) error {
		t.Fatal("matching mode move must not chmod")
		return nil
	}
	require.NoError(t, p.move(src, dest, placementMode))
	after, err := os.Stat(dest)
	require.NoError(t, err)
	require.True(t, os.SameFile(before, after))
}

func TestPlacementFilesystemFailures(t *testing.T) {
	t.Parallel()

	for _, failure := range []string{"source", "staging", "publication"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			src, dest := placementFixture(t)
			switch failure {
			case "source":
				src = filepath.Join(t.TempDir(), "missing")
			case "staging":
				dest = filepath.Join(src, "not-a-directory")
			case "publication":
				dest = t.TempDir()
				require.NoError(t, os.WriteFile(filepath.Join(dest, "existing"), []byte("existing destination"), placementMode))
			}

			err := newFilePlacement(copyContents).stage(src, dest, placementMode)
			require.Error(t, err)
			if failure != "source" {
				requirePlacementContents(t, src, "source contents")
			}
			if failure == "publication" {
				requirePlacementContents(t, filepath.Join(dest, "existing"), "existing destination")
			}
			if failure == "source" {
				requirePlacementContents(t, dest, "existing destination")
			}
			requireNoStagingFiles(t, dest)
		})
	}
}

func TestPlacementDryRunCreatesNoDirectories(t *testing.T) {
	t.Parallel()

	for _, op := range []FileSystemOperation{NewMove(true), NewCopy(true), NewReflink(true)} {
		src, _ := placementFixture(t)
		dir := filepath.Join(t.TempDir(), "absent")
		dest := filepath.Join(dir, "destination")
		require.NoError(t, op.ProcessPath(context.Background(), NewDirContext(), src, dest, placementMode))
		_, err := os.Stat(dir)
		require.ErrorIs(t, err, os.ErrNotExist)
	}
}

func TestMoveRenameFailureDoesNotStage(t *testing.T) {
	t.Parallel()

	src, dest := placementFixture(t)
	p := newFilePlacement(func(*os.File, *os.File) error {
		t.Fatal("non-EXDEV rename failure must not stage")
		return nil
	})
	p.rename = func(string, string) error { return os.ErrPermission }
	require.ErrorIs(t, p.move(src, dest, placementMode), os.ErrPermission)
	requirePlacementContents(t, src, "source contents")
	requirePlacementContents(t, dest, "existing destination")
	requireNoStagingFiles(t, dest)
}

func TestPlacementCleanupFailure(t *testing.T) {
	t.Parallel()

	src, dest := placementFixture(t)
	transferErr, cleanupErr := errors.New("transfer failed"), errors.New("cleanup failed")
	p := newFilePlacement(func(*os.File, *os.File) error { return transferErr })
	p.remove = func(string) error { return cleanupErr }
	err := p.stage(src, dest, placementMode)
	require.ErrorIs(t, err, transferErr)
	require.ErrorIs(t, err, cleanupErr)
	requirePlacementContents(t, src, "source contents")
	requirePlacementContents(t, dest, "existing destination")
}

func TestMoveRejectsSameFileAliases(t *testing.T) {
	t.Parallel()

	for _, alias := range []string{"directory-symlink", "hardlink", "case-insensitive"} {
		for _, mode := range []os.FileMode{placementMode, 0o640} {
			t.Run(fmt.Sprintf("%s/%o", alias, mode), func(t *testing.T) {
				t.Parallel()
				dir := t.TempDir()
				realDir := filepath.Join(dir, "real")
				require.NoError(t, os.Mkdir(realDir, 0o700))
				src := filepath.Join(realDir, "track.flac")
				require.NoError(t, os.WriteFile(src, []byte("source contents"), mode))
				require.NoError(t, os.Chmod(src, mode))
				var dest string
				switch alias {
				case "directory-symlink":
					aliasDir := filepath.Join(dir, "alias")
					require.NoError(t, os.Symlink(realDir, aliasDir))
					dest = filepath.Join(aliasDir, "track.flac")
				case "hardlink":
					dest = filepath.Join(realDir, "linked.flac")
					require.NoError(t, os.Link(src, dest))
				case "case-insensitive":
					dest = filepath.Join(realDir, "TRACK.FLAC")
					_, err := os.Stat(dest)
					if errors.Is(err, os.ErrNotExist) {
						t.Skip("filesystem is case-sensitive")
					}
					require.NoError(t, err)
				}
				before, err := os.Stat(src)
				require.NoError(t, err)
				err = NewMove(false).ProcessPath(context.Background(), NewDirContext(), src, dest, placementMode)
				require.ErrorContains(t, err, "source and destination refer to the same file")
				for _, path := range []string{src, dest} {
					requirePlacementContents(t, path, "source contents")
					after, err := os.Stat(path)
					require.NoError(t, err)
					require.True(t, os.SameFile(before, after))
					require.Equal(t, mode, after.Mode().Perm())
				}
				requireNoStagingFiles(t, dest)
			})
		}
	}
}
