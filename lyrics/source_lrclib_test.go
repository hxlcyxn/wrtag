package lyrics

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.senan.xyz/wrtag/clientutil"
)

func lrclibResponseForTest(status int, retryAfter, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Retry-After": {retryAfter}}, Body: io.NopCloser(strings.NewReader(body))}
}

func lrclibForTest(t *testing.T, timeout, interval time.Duration, transport http.RoundTripper) *LRCLib {
	t.Helper()
	source, err := NewLRCLib(LRCLibConfig{Timeout: timeout, RequestInterval: interval})
	require.NoError(t, err)
	source.HTTPClient.Transport = clientutil.WithoutRetry(clientutil.WithRetry(5)(transport))
	return source
}

func TestLRCLibResponse(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   string
		err    bool
	}{
		{"synced", 200, `{"syncedLyrics":"synced","plainLyrics":"plain"}`, "synced", false},
		{"plain", 200, `{"plainLyrics":"plain"}`, "plain", false},
		{"instrumental", 200, `{"instrumental":true}`, "", false},
		{"missing", 404, `{}`, "", true},
		{"unauthorized", 401, `{}`, "", true},
		{"malformed", 200, `{`, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			source := lrclibForTest(t, time.Second, time.Millisecond, clientutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				assert.Equal(t, lrclibAppID, r.Header.Get("Lrclib-App-Id"))
				assert.Equal(t, "artist", r.URL.Query().Get("artist_name"))
				assert.Equal(t, "song", r.URL.Query().Get("track_name"))
				assert.Equal(t, "123", r.URL.Query().Get("duration"))
				return lrclibResponseForTest(tc.status, "", tc.body), nil
			}))
			data, err := source.Search(t.Context(), "artist", "song", 123*time.Second)
			assert.Equal(t, tc.want, data)
			assert.Equal(t, tc.err, err != nil)
			assert.Equal(t, 1, calls)
			if tc.status == 404 {
				assert.ErrorIs(t, err, ErrTrackNotFound)
			}
		})
	}
}

type lrclibTrackedBody struct {
	io.Reader
	closed *bool
}

func (b lrclibTrackedBody) Close() error {
	*b.closed = true
	return nil
}

func TestLRCLibRetries(t *testing.T) {
	t.Parallel()
	for _, status := range []int{429, 503} {
		for _, exhaust := range []bool{false, true} {
			t.Run(http.StatusText(status)+fmtBool(exhaust), func(t *testing.T) {
				t.Parallel()
				synctest.Test(t, func(t *testing.T) {
					calls := 0
					closed := true
					var previous time.Time
					source := lrclibForTest(t, 0, 750*time.Millisecond, clientutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
						assert.True(t, closed)
						if calls > 0 {
							assert.GreaterOrEqual(t, time.Since(previous), time.Second)
						}
						previous = time.Now()
						calls++
						code := status
						if !exhaust && calls == 2 {
							code = 200
						}
						closed = false
						response := lrclibResponseForTest(code, "1", `{"plainLyrics":"plain"}`)
						response.Body = lrclibTrackedBody{Reader: response.Body, closed: &closed}
						return response, nil
					}))
					_, err := source.Search(t.Context(), "", "", 0)
					assert.Equal(t, exhaust, err != nil)
					assert.True(t, closed)
					if exhaust {
						assert.Equal(t, 5, calls)
					} else {
						assert.Equal(t, 2, calls)
					}
				})
			})
		}
	}
}

func fmtBool(value bool) string {
	if value {
		return "/exhaustion"
	}
	return "/success"
}

func TestLRCLibRetryAfter(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC().Truncate(time.Second)
	for _, tc := range []struct {
		value   string
		minimum time.Duration
	}{
		{"2", 2 * time.Second},
		{now.Add(5 * time.Second).Format(http.TimeFormat), 5 * time.Second},
		{now.Add(-time.Second).Format(http.TimeFormat), 0},
		{"0", 0},
		{"invalid", 3 * time.Second},
		{"-1", 3 * time.Second},
	} {
		delay := lrclibRetryAfter(tc.value, 3, now).Sub(now)
		assert.GreaterOrEqual(t, delay, tc.minimum)
		assert.LessOrEqual(t, delay, tc.minimum+time.Duration(float64(tc.minimum)*lrclibRetryJitter))
	}
	for _, value := range []string{"9223372037", "99999999999999999999999"} {
		assert.Equal(t, time.Duration(1<<63-1), lrclibRetryAfter(value, 3, now).Sub(now))
	}
}

func TestLRCLibConcurrentSpacing(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		var previousEnd time.Time
		calls := 0
		source := lrclibForTest(t, 30*time.Second, 750*time.Millisecond, clientutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
			if calls > 0 {
				assert.GreaterOrEqual(t, time.Since(previousEnd), 750*time.Millisecond)
			}
			calls++
			time.Sleep(time.Second)
			previousEnd = time.Now()
			return lrclibResponseForTest(200, "", `{}`), nil
		}))
		var wg sync.WaitGroup
		for range 4 {
			wg.Go(func() {
				_, err := source.Search(t.Context(), "", "", 0)
				assert.NoError(t, err)
			})
		}
		wg.Wait()
		assert.Equal(t, 4, calls)
	})
}

type lrclibBlockingBody struct {
	done <-chan struct{}
	err  func() error
}

func (b lrclibBlockingBody) Read([]byte) (int, error) {
	<-b.done
	return 0, b.err()
}

func (b lrclibBlockingBody) Close() error { return nil }

func TestLRCLibBudget(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"headers", "body", "retry"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				calls := 0
				source := lrclibForTest(t, time.Second, 750*time.Millisecond, clientutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
					calls++
					switch mode {
					case "headers":
						<-r.Context().Done()
						return nil, r.Context().Err()
					case "body":
						response := lrclibResponseForTest(200, "", "")
						response.Body = lrclibBlockingBody{r.Context().Done(), r.Context().Err}
						return response, nil
					default:
						return lrclibResponseForTest(429, "10", ""), nil
					}
				}))
				start := time.Now()
				_, err := source.Search(t.Context(), "", "", 0)
				require.ErrorIs(t, err, context.DeadlineExceeded)
				assert.Equal(t, time.Second, time.Since(start))
				assert.Equal(t, 1, calls)
			})
		})
	}
}

func TestLRCLibAdmissionOutsideBudget(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		source := lrclibForTest(t, time.Second, 2*time.Second, clientutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
			return lrclibResponseForTest(200, "", `{}`), nil
		}))
		for range 2 {
			_, err := source.Search(t.Context(), "", "", 0)
			require.NoError(t, err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err := source.Search(ctx, "", "", 0)
		assert.ErrorIs(t, err, context.Canceled)
	})
}

func TestLRCLibSharedCooldown(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		start := time.Now()
		source := lrclibForTest(t, time.Second, time.Millisecond, clientutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			if calls == 1 {
				return lrclibResponseForTest(429, "10", ""), nil
			}
			assert.GreaterOrEqual(t, time.Since(start), 10*time.Second)
			return lrclibResponseForTest(200, "", `{}`), nil
		}))
		_, err := source.Search(t.Context(), "", "", 0)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		_, err = source.Search(t.Context(), "", "", 0)
		require.NoError(t, err)
		assert.Equal(t, 2, calls)
	})
}

func TestLRCLibParentCancellation(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		source := lrclibForTest(t, 0, time.Second, clientutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
			<-r.Context().Done()
			return nil, r.Context().Err()
		}))
		ctx, cancel := context.WithCancel(t.Context())
		var wg sync.WaitGroup
		wg.Go(func() {
			_, err := source.Search(ctx, "", "", 0)
			assert.ErrorIs(t, err, context.Canceled)
		})
		synctest.Wait()
		queued, stop := context.WithTimeout(t.Context(), time.Second)
		defer stop()
		_, err := source.Search(queued, "", "", 0)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		cancel()
		wg.Wait()
	})
}

func TestLRCLibConfiguration(t *testing.T) {
	t.Parallel()
	source, err := NewSource("lrclib")
	require.NoError(t, err)
	lrclib, ok := source.(*LRCLib)
	require.True(t, ok)
	assert.Equal(t, 30*time.Second, lrclib.HTTPClient.Timeout)
	assert.Equal(t, 750*time.Millisecond, lrclib.config.RequestInterval)
	require.ErrorIs(t, lrclib.HTTPClient.CheckRedirect(nil, nil), http.ErrUseLastResponse)

	for _, config := range []LRCLibConfig{
		{Timeout: -time.Second, RequestInterval: time.Second},
		{Timeout: time.Second, RequestInterval: 0},
		{Timeout: time.Second, RequestInterval: -time.Second},
	} {
		_, err := NewLRCLib(config)
		require.Error(t, err)
	}
	for _, name := range []string{"genius", "musixmatch"} {
		other, err := NewSource(name)
		require.NoError(t, err)
		assert.Equal(t, name, fmt.Sprint(other))
	}
}

func TestLRCLibTransportError(t *testing.T) {
	t.Parallel()
	want := errors.New("transport failed")
	source := lrclibForTest(t, 0, time.Millisecond, clientutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, want
	}))
	_, err := source.Search(t.Context(), "", "", 0)
	assert.ErrorIs(t, err, want)
}
