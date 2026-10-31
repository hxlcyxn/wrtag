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
