package lyrics

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.senan.xyz/wrtag/lyrics"
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
		assert.Equal(t, "lyrics (lrclib lrclib-timeout=30s lrclib-request-interval=750ms, genius, musixmatch)", addon.String())
	}

	addon, err := NewLyricsAddon("genius lrclib-timeout=0 lrclib-request-interval=1s lrclib")
	require.NoError(t, err)
	assert.Equal(t, "lyrics (genius, lrclib lrclib-timeout=0s lrclib-request-interval=1s)", addon.String())
}

func TestLyricsInvalidConfiguration(t *testing.T) {
	t.Parallel()
	for _, conf := range []string{
		"", "lrclib-timeout=1s", "genius lrclib-timeout=1s", "musixmatch lrclib-request-interval=1s",
		"lrclib lrclib-timeout=-1s", "lrclib lrclib-timeout=no", "lrclib lrclib-timeout=",
		"lrclib lrclib-request-interval=0", "lrclib lrclib-request-interval=-1s", "lrclib lrclib-request-interval=no",
		"lrclib lrclib-timeout=1s lrclib-timeout=2s", "lrclib lrclib-request-interval=1s lrclib-request-interval=2s",
		"lrclib unknown=1s", "unknown",
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
	first, err := NewLyricsAddon("lrclib lrclib-timeout=1s lrclib-request-interval=2s genius")
	require.NoError(t, err)
	second, err := NewLyricsAddon("lrclib genius")
	require.NoError(t, err)
	assert.NotEqual(t, first.String(), second.String())
	firstSources, ok := first.source.(lyrics.MultiSource)
	require.True(t, ok)
	secondSources, ok := second.source.(lyrics.MultiSource)
	require.True(t, ok)
	assert.NotSame(t, firstSources[0], secondSources[0])
	assert.NotSame(t, firstSources[1], secondSources[1])
	assert.Contains(t, second.String(), "lrclib-timeout=30s lrclib-request-interval=750ms")
}
