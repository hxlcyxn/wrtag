package wrtag

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func replacementFixture(t *testing.T, same bool) (string, string, []albumFile) {
	t.Helper()
	src, dest, files := albumFixture(t)
	if same {
		dest = src
	} else {
		require.NoError(t, os.Mkdir(dest, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(dest, "track"), []byte("old destination"), 0644))
	}
	return src, dest, files
}

func TestAlbumReplacementComplete(t *testing.T) {
	t.Parallel()
	for _, same := range []bool{false, true} {
		for _, fallback := range []bool{false, true} {
			t.Run(strings.Join([]string{map[bool]string{false: "external", true: "same-path"}[same], map[bool]string{false: "directory", true: "fallback"}[fallback]}, "/"), func(t *testing.T) {
				t.Parallel()
				src, dest, files := replacementFixture(t, same)
				original, err := os.Stat(filepath.Join(dest, "track"))
				require.NoError(t, err)
				ops := defaultAlbumIO()
				if fallback {
					ops.publish = func(string, string) error { return errDirectoryPublicationUnsupported }
				}
				err = importAlbum(context.Background(), NewMove(false), src, dest, files, 0660, func(stage string) error {
					b, e := os.ReadFile(filepath.Join(dest, "track"))
					require.NoError(t, e)
					if same {
						require.Equal(t, "original", string(b))
					} else {
						require.Equal(t, "old destination", string(b))
					}
					require.NoError(t, os.WriteFile(filepath.Join(stage, "addon"), []byte("complete asset"), 0600))
					return os.WriteFile(filepath.Join(stage, "track"), []byte("prepared"), 0600)
				}, ops)
				require.NoError(t, err)
				content, e := os.ReadFile(filepath.Join(dest, "track"))
				require.NoError(t, e)
				require.Equal(t, "prepared", string(content))
				current, e := os.Stat(filepath.Join(dest, "track"))
				require.NoError(t, e)
				require.False(t, os.SameFile(original, current))
				require.FileExists(t, filepath.Join(dest, "addon"))
				paths, e := FindAlbumRecoveryPaths(filepath.Dir(dest))
				require.NoError(t, e)
				require.Empty(t, paths)
				if !same {
					require.NoDirExists(t, src)
				}
			})
		}
	}
}

func TestAlbumReplacementFailureBoundaries(t *testing.T) {
	t.Parallel()
	for _, same := range []bool{false, true} {
		for _, boundary := range []string{"prepare", "chmod", "backup-rename", "publication", "restore", "backup-cleanup", "source-cleanup", "cancel-publication", "concurrent-destination"} {
			t.Run(map[bool]string{false: "external/", true: "same-path/"}[same]+boundary, func(t *testing.T) {
				t.Parallel()
				src, dest, files := replacementFixture(t, same)
				before, e := os.ReadFile(filepath.Join(dest, "track"))
				require.NoError(t, e)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				ops := defaultAlbumIO()
				boom := errors.New("injected " + boundary)
				realPublish := ops.publish
				calls := 0
				if boundary == "chmod" {
					ops.chmod = func(string, os.FileMode) error { return boom }
				}
				if boundary == "backup-rename" {
					ops.rename = func(string, string) error { return boom }
				}
				ops.publish = func(stage, target string) error {
					calls++
					if boundary == "cancel-publication" && calls == 1 {
						cancel()
						return context.Canceled
					}
					if boundary == "concurrent-destination" && calls == 1 {
						require.NoError(t, os.Mkdir(target, 0755))
						require.NoError(t, os.WriteFile(filepath.Join(target, "concurrent"), []byte("other"), 0644))
						return boom
					}
					if (boundary == "publication" && calls == 1) || boundary == "restore" {
						return boom
					}
					return realPublish(stage, target)
				}
				removeAll := ops.removeAll
				if boundary == "backup-cleanup" {
					ops.removeAll = func(p string) error {
						if strings.Contains(filepath.Base(p), "-backup-") {
							return boom
						}
						return removeAll(p)
					}
				}
				remove := ops.remove
				if boundary == "source-cleanup" {
					ops.remove = func(p string) error {
						if p == files[0].source {
							return boom
						}
						return remove(p)
					}
				}
				err := importAlbum(ctx, NewMove(false), src, dest, files, 0660, func(stage string) error {
					if boundary == "prepare" {
						return boom
					}
					return os.WriteFile(filepath.Join(stage, "track"), []byte("prepared"), 0600)
				}, ops)
				if boundary == "source-cleanup" && same {
					require.NoError(t, err)
					return
				}
				require.Error(t, err)
				paths, e := FindAlbumRecoveryPaths(filepath.Dir(dest))
				require.NoError(t, e)
				switch boundary {
				case "backup-cleanup":
					require.ErrorContains(t, err, "destination complete")
					require.ErrorContains(t, err, "backup cleanup incomplete")
					require.Len(t, paths, 1)
					content, e := os.ReadFile(filepath.Join(paths[0], "track"))
					require.NoError(t, e)
					require.Equal(t, before, content)
				case "restore", "concurrent-destination":
					require.ErrorContains(t, err, "restoration failed")
					require.Len(t, paths, 1)
					content, e := os.ReadFile(filepath.Join(paths[0], "track"))
					require.NoError(t, e)
					require.Equal(t, before, content)
					if boundary == "concurrent-destination" {
						require.FileExists(t, filepath.Join(dest, "concurrent"))
					} else {
						require.NoDirExists(t, dest)
					}
				case "source-cleanup":
					require.ErrorContains(t, err, "destination complete")
					require.ErrorContains(t, err, "source cleanup incomplete")
					require.FileExists(t, files[0].source)
					require.Empty(t, paths)
				default:
					content, e := os.ReadFile(filepath.Join(dest, "track"))
					require.NoError(t, e)
					require.Equal(t, before, content)
					require.Empty(t, paths)
				}
				unlock := lockPaths(dest)
				unlock()
			})
		}
	}
}

func TestAlbumReplacementAssetLossRejected(t *testing.T) {
	t.Parallel()
	for _, asset := range []string{"bonus.flac", "lyrics.txt", "subdir/unknown", "CD1/track", "cover.png"} {
		t.Run(asset, func(t *testing.T) {
			t.Parallel()
			src, dest, files := replacementFixture(t, false)
			if asset == "cover.png" {
				cover := filepath.Join(src, "cover.jpg")
				require.NoError(t, os.WriteFile(cover, []byte("new cover"), 0644))
				files = append(files, albumFile{source: cover, name: "cover.jpg"})
				require.NoError(t, os.WriteFile(filepath.Join(dest, "cover.jpg"), []byte("old cover"), 0644))
			}
			p := filepath.Join(dest, asset)
			require.NoError(t, os.MkdirAll(filepath.Dir(p), 0755))
			require.NoError(t, os.WriteFile(p, []byte("must retain"), 0644))
			err := importAlbum(context.Background(), NewMove(false), src, dest, files, 0660, func(string) error { t.Fatal("preparation called"); return nil }, defaultAlbumIO())
			require.ErrorContains(t, err, "unaccounted destination asset loss")
			require.FileExists(t, p)
			require.FileExists(t, files[0].source)
		})
	}
}

func TestAlbumSamePathDiscInputFlattens(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	files := []albumFile{}
	for _, disc := range []string{"CD1", "CD2"} {
		dir := filepath.Join(root, disc)
		require.NoError(t, os.Mkdir(dir, 0755))
		source := filepath.Join(dir, "track.flac")
		require.NoError(t, os.WriteFile(source, []byte(disc), 0644))
		files = append(files, albumFile{source: source, name: disc + "-track.flac"})
	}
	require.NoError(t, importAlbum(context.Background(), NewMove(false), root, root, files, 0660, func(string) error { return nil }, defaultAlbumIO()))
	for _, f := range files {
		require.FileExists(t, filepath.Join(root, f.name))
		require.NoFileExists(t, f.source)
	}
}

func TestAlbumReplacementDetectsDestinationChanges(t *testing.T) {
	t.Parallel()
	src, dest, files := replacementFixture(t, false)
	err := importAlbum(context.Background(), NewMove(false), src, dest, files, 0660, func(string) error {
		return os.WriteFile(filepath.Join(dest, "new-extra"), []byte("concurrent"), 0644)
	}, defaultAlbumIO())
	require.ErrorContains(t, err, "destination changed")
	require.FileExists(t, filepath.Join(dest, "new-extra"))
	require.FileExists(t, files[0].source)
}

func TestAlbumAliasSamePath(t *testing.T) {
	t.Parallel()
	src, _, files := albumFixture(t)
	alias := filepath.Join(filepath.Dir(src), "alias")
	if err := os.Symlink(src, alias); err != nil {
		t.Skipf("directory symlink unavailable: %v", err)
	}
	files[0].source = filepath.Join(alias, "track")
	require.NoError(t, importAlbum(context.Background(), NewMove(false), alias, src, files, 0660, func(stage string) error { return os.WriteFile(filepath.Join(stage, "track"), []byte("retagged"), 0600) }, defaultAlbumIO()))
	require.FileExists(t, filepath.Join(alias, "track"))
	content, err := os.ReadFile(filepath.Join(src, "track"))
	require.NoError(t, err)
	require.Equal(t, "retagged", string(content))
}

func TestAlbumFallbackRollbackAndSurvivors(t *testing.T) {
	t.Parallel()
	for _, failedRollback := range []bool{false, true} {
		src, dest, files := albumFixture(t)
		ops := defaultAlbumIO()
		ops.publish = func(string, string) error { return errDirectoryPublicationUnsupported }
		link := ops.link
		calls := 0
		ops.link = func(a, b string) error {
			calls++
			if calls == 2 {
				return errors.New("link denied")
			}
			return link(a, b)
		}
		remove := ops.remove
		if failedRollback {
			ops.remove = func(p string) error {
				if fileWithin(dest, p) {
					return errors.New("cleanup denied")
				}
				return remove(p)
			}
		}
		err := importAlbum(context.Background(), NewMove(false), src, dest, files, 0660, func(stage string) error { return os.WriteFile(filepath.Join(stage, "addon"), []byte("addon"), 0644) }, ops)
		require.ErrorContains(t, err, "prepared per-file fallback publication failed")
		require.FileExists(t, files[0].source)
		if failedRollback {
			require.ErrorContains(t, err, "survivor")
			require.DirExists(t, dest)
		} else {
			require.NoDirExists(t, dest)
		}
	}
}

func TestAlbumReplacementDryRun(t *testing.T) {
	t.Parallel()
	for _, same := range []bool{false, true} {
		src, dest, files := replacementFixture(t, same)
		before, err := os.ReadFile(filepath.Join(dest, "track"))
		require.NoError(t, err)
		require.NoError(t, importAlbum(context.Background(), NewMove(true), src, dest, files, 0660, func(string) error { t.Fatal("prepared dry run"); return nil }, defaultAlbumIO()))
		after, err := os.ReadFile(filepath.Join(dest, "track"))
		require.NoError(t, err)
		require.Equal(t, before, after)
		paths, err := FindAlbumRecoveryPaths(filepath.Dir(dest))
		require.NoError(t, err)
		require.Empty(t, paths)
	}
}

func TestAlbumFallbackRestoresDiscOriginalAfterCancellation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	files := []albumFile{}
	for _, disc := range []string{"CD1", "CD2"} {
		dir := filepath.Join(root, disc)
		require.NoError(t, os.Mkdir(dir, 0755))
		p := filepath.Join(dir, "track")
		require.NoError(t, os.WriteFile(p, []byte(disc), 0644))
		files = append(files, albumFile{source: p, name: disc + "-track"})
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ops := defaultAlbumIO()
	ops.publish = func(string, string) error { return errDirectoryPublicationUnsupported }
	link := ops.link
	calls := 0
	ops.link = func(a, b string) error {
		calls++
		err := link(a, b)
		if calls == 1 {
			cancel()
		}
		return err
	}
	err := importAlbum(ctx, NewMove(false), root, root, files, 0660, func(string) error { return nil }, ops)
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorContains(t, err, "original destination restored")
	for _, f := range files {
		require.FileExists(t, f.source)
		require.NoFileExists(t, filepath.Join(root, f.name))
	}
	paths, e := FindAlbumRecoveryPaths(filepath.Dir(root))
	require.NoError(t, e)
	require.Empty(t, paths)
}
