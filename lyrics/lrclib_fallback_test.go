package lyrics_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.senan.xyz/wrtag/clientutil"
	"go.senan.xyz/wrtag/lyrics"
)

func TestLRCLibTimeoutFallbackIntegration(t *testing.T) {
	t.Parallel()
	for _, status := range []int{http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				config := lyrics.DefaultLRCLibConfig()
				config.URL = "https://example.invalid/compat/lrclib/api/get?key=value"
				config.Timeout = time.Second
				config.RequestInterval = 25 * time.Millisecond
				source, err := lyrics.NewLRCLib(config)
				require.NoError(t, err)
				calls := 0
				source.HTTPClient.Transport = clientutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
					calls++
					assert.Equal(t, "/compat/lrclib/api/get", r.URL.Path)
					assert.Equal(t, "value", r.URL.Query().Get("key"))
					assert.Equal(t, "artist", r.URL.Query().Get("artist_name"))
					assert.Empty(t, r.Header.Get("Lrclib-App-Id"))
					if calls > 1 {
						<-r.Context().Done()
						return nil, r.Context().Err()
					}
					return &http.Response{
						StatusCode: status,
						Header:     http.Header{"Retry-After": {"0"}},
						Body:       io.NopCloser(strings.NewReader("")),
					}, nil
				})
				fallback := &fakeSource{lyrics: "fallback lyrics"}
				started := time.Now()
				data, err := (lyrics.MultiSource{source, fallback}).Search(t.Context(), "artist", "song", 0)
				require.NoError(t, err)
				assert.Equal(t, "fallback lyrics", data)
				assert.True(t, fallback.called)
				assert.Equal(t, 2, calls)
				assert.Equal(t, config.Timeout, time.Since(started))
				require.NoError(t, t.Context().Err())

				_, err = (lyrics.MultiSource{source, &fakeSource{err: lyrics.ErrTrackNotFound}}).Search(t.Context(), "artist", "song", 0)
				require.ErrorIs(t, err, lyrics.ErrProviderFailed)
				require.ErrorIs(t, err, context.DeadlineExceeded)
				require.NotErrorIs(t, err, lyrics.ErrTrackNotFound)
				assert.Contains(t, fmt.Sprint(source), config.URL)
			})
		})
	}
}
