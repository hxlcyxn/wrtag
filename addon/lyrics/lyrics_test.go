package lyrics

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	provider "go.senan.xyz/wrtag/lyrics"
	"go.senan.xyz/wrtag/tags"
	"go.senan.xyz/wrtag/tags/normtag"
)

type sourceFunc func(context.Context, string, string, time.Duration) (string, error)

func (f sourceFunc) Search(ctx context.Context, artist, song string, duration time.Duration) (string, error) {
	return f(ctx, artist, song, duration)
}

func trackFile(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("../../tags/testdata/empty.flac")
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "track.flac")
	require.NoError(t, os.WriteFile(path, data, 0600))
	initial := map[string][]string{}
	normtag.Set(initial, normtag.ArtistCredit, "artist")
	normtag.Set(initial, normtag.Title, "song")
	require.NoError(t, tags.WriteTags(path, initial, 0))
	return path
}

func TestLyricsAddonProviderFailureLeavesTagsUnchanged(t *testing.T) {
	t.Parallel()

	path := trackFile(t)
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	a := LyricsAddon{sourceFunc(func(ctx context.Context, artist, song string, duration time.Duration) (string, error) {
		assert.Equal(t, "artist", artist)
		assert.Equal(t, "song", song)
		return "must not write", errors.New("provider unavailable")
	})}
	require.NoError(t, a.ProcessRelease(t.Context(), "", []string{path}))
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

func TestLyricsAddonExistingLyricsSkip(t *testing.T) {
	t.Parallel()

	path := trackFile(t)
	data := map[string][]string{}
	normtag.Set(data, normtag.Lyrics, "existing lyrics")
	require.NoError(t, tags.WriteTags(path, data, 0))
	a := LyricsAddon{sourceFunc(func(context.Context, string, string, time.Duration) (string, error) {
		t.Error("existing lyrics must skip search")
		return "", nil
	})}
	require.NoError(t, a.ProcessRelease(t.Context(), "", []string{path}))
	got, err := tags.ReadTags(path)
	require.NoError(t, err)
	assert.Equal(t, "existing lyrics", normtag.Get(got, normtag.Lyrics))
}

func TestLyricsAddonTagErrorsFatal(t *testing.T) {
	t.Parallel()

	t.Run("read", func(t *testing.T) {
		t.Parallel()

		a := LyricsAddon{sourceFunc(func(context.Context, string, string, time.Duration) (string, error) {
			t.Error("failed tag read must skip search")
			return "", nil
		})}
		err := a.ProcessRelease(t.Context(), "", []string{filepath.Join(t.TempDir(), "missing.flac")})
		require.ErrorContains(t, err, "read first")
	})
	t.Run("write", func(t *testing.T) {
		t.Parallel()

		path := trackFile(t)
		a := LyricsAddon{sourceFunc(func(context.Context, string, string, time.Duration) (string, error) {
			require.NoError(t, os.Remove(path))
			require.NoError(t, os.Mkdir(path, 0700))
			return "new lyrics", nil
		})}
		require.ErrorContains(t, a.ProcessRelease(t.Context(), "", []string{path}), "write new lyrics")
	})
}

func TestLyricsAddonParentCancellation(t *testing.T) {
	t.Parallel()

	for _, sourceErr := range []error{nil, provider.ErrTrackNotFound, errors.New("provider failed")} {
		path := trackFile(t)
		before, err := os.ReadFile(path)
		require.NoError(t, err)
		ctx, cancel := context.WithCancel(t.Context())
		a := LyricsAddon{sourceFunc(func(context.Context, string, string, time.Duration) (string, error) {
			cancel()
			return "must not write", sourceErr
		})}
		require.ErrorIs(t, a.ProcessRelease(ctx, "", []string{path}), context.Canceled)
		after, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, before, after)
	}
	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	require.ErrorIs(t, (LyricsAddon{}).ProcessRelease(ctx, "", nil), context.DeadlineExceeded)
}

func TestLyricsAddonProviderTimeout(t *testing.T) {
	t.Parallel()

	path := trackFile(t)
	a := LyricsAddon{sourceFunc(func(context.Context, string, string, time.Duration) (string, error) {
		return "", context.DeadlineExceeded
	})}
	require.NoError(t, a.ProcessRelease(t.Context(), "", []string{path}))
}
