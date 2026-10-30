package wrtag

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/KarpelesLab/reflink"
	"github.com/stretchr/testify/require"
)

const placementMode os.FileMode = 0o660

func placementFixture(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	src, dest := filepath.Join(dir, "source"), filepath.Join(dir, "destination")
	require.NoError(t, os.WriteFile(src, []byte("source contents"), placementMode))
	require.NoError(t, os.Chmod(src, placementMode))
	require.NoError(t, os.WriteFile(dest, []byte("existing destination"), placementMode))
	return src, dest
}

func requirePlacementContents(t *testing.T, path, expected string) {
	t.Helper()
	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, expected, string(contents))
}

func requireNoStagingFiles(t *testing.T, dest string) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(filepath.Dir(dest), ".wrtag-*"))
	require.NoError(t, err)
	require.Empty(t, paths)
}

func requireReflinkOrSkip(t *testing.T, err error) {
	t.Helper()
	if errors.Is(err, reflink.ErrReflinkUnsupported) || errors.Is(err, reflink.ErrReflinkFailed) || errors.Is(err, syscall.EXDEV) || errors.Is(err, syscall.ENOTTY) {
		t.Skipf("actual reflink unavailable: %v", err)
	}
	require.NoError(t, err)
}

func TestPlacementFailures(t *testing.T) {
	t.Parallel()

	injected := errors.New("injected failure")
	for _, operation := range []string{"copy", "reflink", "move-mode", "move-exdev"} {
		for _, failure := range []string{"transfer", "chmod", "close", "publish"} {
			t.Run(operation+"/"+failure, func(t *testing.T) {
				t.Parallel()
				src, dest := placementFixture(t)
				p := newFilePlacement(copyContents)
				if operation == "reflink" {
					p.transfer = reflinkContents
					probe := filepath.Join(t.TempDir(), "probe")
					requireReflinkOrSkip(t, p.stage(src, probe, placementMode))
				}
				if operation == "move-mode" {
					require.NoError(t, os.Chmod(src, 0o640))
				}
				if operation == "move-exdev" {
					p.rename = func(from, to string) error {
						if from == src {
							return &os.LinkError{Op: "rename", Old: from, New: to, Err: syscall.EXDEV}
						}
						return os.Rename(from, to)
					}
				}

				switch failure {
				case "transfer":
					p.transfer = func(tmp, source *os.File) error {
						_, err := tmp.WriteString("partial contents")
						require.NoError(t, err)
						return injected
					}
				case "chmod":
					p.chmod = func(*os.File, os.FileMode) error { return injected }
				case "close":
					p.close = func(tmp *os.File) error {
						require.NoError(t, tmp.Close())
						return injected
					}
				case "publish":
					rename := p.rename
					p.rename = func(from, to string) error {
						if from != src {
							return injected
						}
						return rename(from, to)
					}
				}

				var err error
				if operation == "move-mode" || operation == "move-exdev" {
					err = p.move(src, dest, placementMode)
				} else {
					err = p.stage(src, dest, placementMode)
				}
				require.ErrorIs(t, err, injected)
				requirePlacementContents(t, src, "source contents")
				requirePlacementContents(t, dest, "existing destination")
				requireNoStagingFiles(t, dest)
			})
		}
	}
}

func TestMovePublicationOrder(t *testing.T) {
	t.Parallel()

	for _, operation := range []string{"matching-mode", "mismatched-mode", "exdev", "remove-failure"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			src, dest := placementFixture(t)
			p := newFilePlacement(copyContents)
			if operation != "matching-mode" {
				require.NoError(t, os.Chmod(src, 0o640))
			}
			if operation == "exdev" {
				require.NoError(t, os.Chmod(src, placementMode))
			}

			closed := false
			p.close = func(tmp *os.File) error {
				info, err := tmp.Stat()
				require.NoError(t, err)
				require.Equal(t, placementMode, info.Mode().Perm())
				err = tmp.Close()
				closed = true
				return err
			}
			p.rename = func(from, to string) error {
				requirePlacementContents(t, src, "source contents")
				requirePlacementContents(t, dest, "existing destination")
				if from == src && operation == "exdev" {
					return syscall.EXDEV
				}
				if from != src {
					require.True(t, closed)
				}
				return os.Rename(from, to)
			}
			injected := errors.New("remove source denied")
			p.remove = func(path string) error {
				requirePlacementContents(t, dest, "source contents")
				if operation == "remove-failure" {
					return injected
				}
				return os.Remove(path)
			}

			err := p.move(src, dest, placementMode)
			if operation == "remove-failure" {
				require.ErrorIs(t, err, injected)
				requirePlacementContents(t, src, "source contents")
			} else {
				require.NoError(t, err)
				_, err := os.Stat(src)
				require.ErrorIs(t, err, os.ErrNotExist)
			}
			requirePlacementContents(t, dest, "source contents")
			requireNoStagingFiles(t, dest)
		})
	}
}

func TestPlacementOperations(t *testing.T) {
	t.Parallel()

	for _, operation := range []string{"move", "copy", "reflink"} {
		for _, dryRun := range []bool{false, true} {
			name := operation
			if dryRun {
				name += "-dry-run"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				src, dest := placementFixture(t)
				require.NoError(t, os.Chmod(src, 0o640))
				var op FileSystemOperation
				switch operation {
				case "move":
					op = NewMove(dryRun)
				case "copy":
					op = NewCopy(dryRun)
				case "reflink":
					op = NewReflink(dryRun)
				}
				dc := NewDirContext()
				err := op.ProcessPath(context.Background(), dc, src, dest, placementMode)
				if operation == "reflink" && !dryRun && err != nil {
					requirePlacementContents(t, src, "source contents")
					requirePlacementContents(t, dest, "existing destination")
					requireNoStagingFiles(t, dest)
					requireReflinkOrSkip(t, err)
				}
				require.NoError(t, err)
				require.Contains(t, dc.knownDestPaths, dest)
				info, err := os.Stat(dest)
				require.NoError(t, err)
				if dryRun {
					requirePlacementContents(t, src, "source contents")
					requirePlacementContents(t, dest, "existing destination")
					info, err := os.Stat(src)
					require.NoError(t, err)
					require.Equal(t, os.FileMode(0o640), info.Mode().Perm())
				} else {
					requirePlacementContents(t, dest, "source contents")
					require.Equal(t, placementMode, info.Mode().Perm())
				}
				requireNoStagingFiles(t, dest)
			})
		}
	}
}

func TestSamePathMovePreservesMode(t *testing.T) {
	t.Parallel()

	for _, dryRun := range []bool{false, true} {
		src, _ := placementFixture(t)
		require.NoError(t, os.Chmod(src, 0o640))
		require.NoError(t, NewMove(dryRun).ProcessPath(context.Background(), NewDirContext(), src, src, placementMode))
		info, err := os.Stat(src)
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o640), info.Mode().Perm())
		requirePlacementContents(t, src, "source contents")
		requireNoStagingFiles(t, src)
	}
}
