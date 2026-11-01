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
	"go.senan.xyz/wrtag/lyrics"
	"go.senan.xyz/wrtag/tags"
	"go.senan.xyz/wrtag/tags/normtag"
)

func TestLyricsConfiguration(t *testing.T) {
	t.Parallel()
	for _, conf := range []string{
		"lrclib genius musixmatch",
		"lrclib-timeout=30s lrclib genius musixmatch lrclib-request-interval=750ms",
		"lrclib lrclib-request-interval=750ms genius lrclib-timeout=30s musixmatch",
	} {
		addon, err := NewLyricsAddon(conf)
		require.NoError(t, err)
		assert.Equal(t, "lyrics (lrclib lrclib-url=https://lrclib.net/api/get lrclib-timeout=30s lrclib-request-interval=750ms lrclib-max-attempts=5, genius, musixmatch)", addon.String())
	}

	addon, err := NewLyricsAddon("genius lrclib-timeout=0 lrclib-request-interval=1s lrclib")
	require.NoError(t, err)
	assert.Equal(t, "lyrics (genius, lrclib lrclib-url=https://lrclib.net/api/get lrclib-timeout=0s lrclib-request-interval=1s lrclib-max-attempts=5)", addon.String())
}

func TestLyricsMaxAttemptsOptionOrder(t *testing.T) {
	t.Parallel()
	for _, conf := range []string{
		"lrclib-max-attempts=1 lrclib genius",
		"lrclib lrclib-max-attempts=1 genius",
		"lrclib genius lrclib-max-attempts=1",
	} {
		addon, err := NewLyricsAddon(conf)
		require.NoError(t, err)
		assert.Contains(t, addon.String(), "lrclib-max-attempts=1")
	}
}

func TestLyricsInvalidConfiguration(t *testing.T) {
	t.Parallel()
	for _, conf := range []string{
		"", "lrclib-timeout=1s", "genius lrclib-timeout=1s", "musixmatch lrclib-request-interval=1s",
		"lrclib lrclib-timeout=-1s", "lrclib lrclib-timeout=no", "lrclib lrclib-timeout=",
		"lrclib lrclib-request-interval=0", "lrclib lrclib-request-interval=-1s", "lrclib lrclib-request-interval=no",
		"lrclib lrclib-timeout=1s lrclib-timeout=2s", "lrclib lrclib-request-interval=1s lrclib-request-interval=2s",
		"lrclib unknown=1s", "unknown",
		"genius lrclib-max-attempts=2", "lrclib lrclib-max-attempts=0",
		"lrclib lrclib-max-attempts=-1", "lrclib lrclib-max-attempts=no",
		"lrclib lrclib-max-attempts=", "lrclib lrclib-max-attempts=1.5",
		"lrclib lrclib-max-attempts=999999999999999999999999",
		"lrclib lrclib-max-attempts=2 lrclib-max-attempts=3",
		"lrclib-url=https://example.org/get", "genius lrclib-url=https://example.org/get",
		"lrclib lrclib-url=", "lrclib lrclib-url=/get", "lrclib lrclib-url=https://user:pass@example.org/get",
		"lrclib lrclib-url=https://example.org/get#fragment", "lrclib lrclib-url=https://a/get lrclib-url=https://b/get",
	} {
		t.Run(conf, func(t *testing.T) {
			t.Parallel()
			_, err := NewLyricsAddon(conf)
			require.Error(t, err)
		})
	}
}

func TestLyricsInstanceIsolation(t *testing.T) {
	t.Parallel()
	first, err := NewLyricsAddon("lrclib lrclib-url=https://example.org/lookup lrclib-timeout=1s lrclib-request-interval=2s lrclib-max-attempts=2 genius")
	require.NoError(t, err)
	second, err := NewLyricsAddon("lrclib genius")
	require.NoError(t, err)
	assert.NotEqual(t, first.String(), second.String())
	assert.Contains(t, first.String(), "lrclib-max-attempts=2")
	assert.Contains(t, second.String(), "lrclib-max-attempts=5")
	firstSources, ok := first.source.(lyrics.MultiSource)
	require.True(t, ok)
	secondSources, ok := second.source.(lyrics.MultiSource)
	require.True(t, ok)
	assert.NotSame(t, firstSources[0], secondSources[0])
	assert.NotSame(t, firstSources[1], secondSources[1])
	assert.Contains(t, second.String(), "lrclib-timeout=30s lrclib-request-interval=750ms")
}

func TestLyricsEndpointOptionOrder(t *testing.T) {
	t.Parallel()
	for _, conf := range []string{
		"lrclib-url=https://example.org/compat/get?key=value genius lrclib",
		"genius lrclib-url=https://example.org/compat/get?key=value lrclib",
		"genius lrclib lrclib-url=https://example.org/compat/get?key=value",
	} {
		addon, err := NewLyricsAddon(conf)
		require.NoError(t, err)
		assert.Equal(t, "lyrics (genius, lrclib lrclib-url=https://example.org/compat/get?key=value lrclib-timeout=30s lrclib-request-interval=750ms lrclib-max-attempts=5)", addon.String())
	}
}

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

	for _, sourceErr := range []error{nil, lyrics.ErrTrackNotFound, errors.New("provider failed")} {
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
