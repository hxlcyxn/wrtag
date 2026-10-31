package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.senan.xyz/wrtag"
	"go.senan.xyz/wrtag/musicbrainz"
	"go.senan.xyz/wrtag/tags"
	"golang.org/x/time/rate"
)

func TestSyncTimestamp(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	old := time.Unix(946684800, 0)
	require.NoError(t, os.Chtimes(dir, old, old))
	require.NoError(t, syncTimestamp(dir))
	info, err := os.Stat(dir)
	require.NoError(t, err)
	require.True(t, info.ModTime().After(old))
	require.ErrorIs(t, syncTimestamp(filepath.Join(dir, "missing")), os.ErrNotExist)
}

func TestSyncDirTimestamp(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{"dry-run", "normal", "moved", "failed"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			src := filepath.Join(root, "Kat Moda")
			require.NoError(t, os.Mkdir(src, 0o755))
			for i := 1; i <= 3; i++ {
				p := filepath.Join(src, fmt.Sprintf("%d.flac", i))
				require.NoError(t, os.WriteFile(p, emptyFLAC, 0o600))
				require.NoError(t, tags.WriteTags(p, map[string][]string{
					"MUSICBRAINZ_ALBUMID": {"e47d04a4-7460-427d-a731-cc82386d85f1"},
				}, 0))
			}
			cfg := wrtag.Config{
				MusicBrainzClient: musicbrainz.MBClient{
					BaseURL:    "file:///testdata/responses/musicbrainz/ws/2",
					HTTPClient: http.DefaultClient,
					Limiter:    rate.NewLimiter(rate.Inf, 1),
				},
				FileMode: 0o600,
			}
			dest := root
			if mode == "moved" {
				dest = filepath.Join(root, "moved")
			}
			require.NoError(t, cfg.PathFormat.Parse(dest+"/{{ .Release.Title | safepath }}/{{ .Track.Position }}{{ .Ext }}"))
			if mode == "failed" {
				require.NoError(t, os.Remove(filepath.Join(src, "3.flac")))
			}
			old := time.Unix(946684800, 0)
			require.NoError(t, os.Chtimes(src, old, old))
			_, err := syncDir(context.Background(), &cfg, 0, 0, wrtag.NewMove(mode == "dry-run"), src)
			if mode == "failed" {
				require.ErrorIs(t, err, wrtag.ErrTrackCountMismatch)
			} else {
				require.NoError(t, err)
			}
			info, err := os.Stat(src)
			if mode == "moved" {
				require.ErrorIs(t, err, os.ErrNotExist)
				return
			}
			require.NoError(t, err)
			if mode == "normal" {
				require.True(t, info.ModTime().After(old))
			} else {
				require.Equal(t, old, info.ModTime())
			}
		})
	}
}
