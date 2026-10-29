package wrtag

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
	"go.senan.xyz/wrtag/tags"
)

const sharedPlacementEnv = "WRTAG_SHARED_PLACEMENT_ROOT"

func TestSharedGroupPlacement(t *testing.T) {
	t.Parallel()

	root := os.Getenv(sharedPlacementEnv)
	if root == "" {
		t.Skip("requires disposable multi-user Linux fixture")
	}

	for _, operation := range []string{"move", "copy", "reflink", "retag"} {
		for _, mode := range []os.FileMode{0o660, 0o640} {
			for _, ext := range []string{"flac", "mp3", "m4a"} {
				name := fmt.Sprintf("%s-%o-%s", operation, mode, ext)
				t.Run(name, func(t *testing.T) {
					t.Parallel()

					src := filepath.Join(root, "source", name, "track."+ext)
					dest := filepath.Join(root, "destination", name, "track."+ext)
					info, err := os.Stat(src)
					require.NoError(t, err)
					sourceStat, ok := info.Sys().(*syscall.Stat_t)
					require.True(t, ok)
					require.NotEqual(t, uint32(os.Getuid()), sourceStat.Uid)
					require.Equal(t, mode, info.Mode().Perm())

					var op FileSystemOperation
					switch operation {
					case "move", "retag":
						op = NewMove(false)
					case "copy":
						op = NewCopy(false)
					case "reflink":
						op = NewReflink(false)
					}
					if operation == "retag" {
						dest = src
					}
					err = op.ProcessPath(context.Background(), NewDirContext(), src, dest, placementMode)
					if operation == "reflink" {
						requireReflinkOrSkip(t, err)
					}
					require.NoError(t, err)
					expectedMode := placementMode
					if operation == "retag" {
						expectedMode = mode
					}

					for _, size := range []int{16, 128 * 1024} {
						value := strings.Repeat("x", size)
						err := tags.WriteTags(dest, map[string][]string{"TITLE": {value}}, tags.Clear)
						if operation == "retag" && mode == 0o640 {
							require.Error(t, err)
						} else {
							require.NoError(t, err)
							read, err := tags.ReadTags(dest)
							require.NoError(t, err)
							require.Equal(t, []string{value}, read["TITLE"])
						}
						info, err := os.Stat(dest)
						require.NoError(t, err)
						require.Equal(t, expectedMode, info.Mode().Perm())
						stat, ok := info.Sys().(*syscall.Stat_t)
						require.True(t, ok)
						require.Equal(t, sourceStat.Gid, stat.Gid)
						t.Logf("tag size=%d mode=%o uid=%d gid=%d", size, info.Mode().Perm(), stat.Uid, stat.Gid)
					}
				})
			}
		}
	}
}

func TestSharedGroupObserver(t *testing.T) {
	t.Parallel()

	root := os.Getenv(sharedPlacementEnv)
	if root == "" {
		t.Skip("requires disposable multi-user Linux fixture")
	}

	paths, err := filepath.Glob(filepath.Join(root, "destination", "*", "track.*"))
	require.NoError(t, err)
	require.NotEmpty(t, paths)

	retags, err := filepath.Glob(filepath.Join(root, "source", "retag-660-*", "track.*"))
	require.NoError(t, err)
	require.Len(t, retags, 3)
	paths = append(paths, retags...)
	for _, path := range paths {
		t.Run(filepath.Base(filepath.Dir(path)), func(t *testing.T) {
			t.Parallel()

			require.NoError(t, tags.WriteTags(path, map[string][]string{"TITLE": {"observer edit"}}, tags.Clear))
			info, err := os.Stat(path)
			require.NoError(t, err)
			require.Equal(t, placementMode, info.Mode().Perm())
		})
	}
}
