package lyrics

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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
	source, err := NewLRCLib(LRCLibConfig{URL: defaultLRCLibURL, Timeout: timeout, RequestInterval: interval, MaxAttempts: defaultLRCLibMaxAttempts})
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
	for _, status := range []int{http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable} {
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

func TestLRCLibMaxAttempts(t *testing.T) {
	t.Parallel()
	for _, attempts := range []int{1, 2, 7} {
		t.Run(fmt.Sprint(attempts), func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				calls := 0
				source := lrclibForTest(t, 0, time.Millisecond, clientutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
					calls++
					return lrclibResponseForTest(http.StatusBadGateway, "0", ""), nil
				}))
				source.config.MaxAttempts = attempts
				_, err := source.Search(t.Context(), "", "", 0)
				require.Error(t, err)
				assert.Equal(t, attempts, calls)
			})
		})
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
	assert.Equal(t, 5, lrclib.config.MaxAttempts)
	require.ErrorIs(t, lrclib.HTTPClient.CheckRedirect(nil, nil), http.ErrUseLastResponse)

	for _, config := range []LRCLibConfig{
		{URL: defaultLRCLibURL, Timeout: -time.Second, RequestInterval: time.Second, MaxAttempts: 5},
		{URL: defaultLRCLibURL, Timeout: time.Second, RequestInterval: 0, MaxAttempts: 5},
		{URL: defaultLRCLibURL, Timeout: time.Second, RequestInterval: -time.Second, MaxAttempts: 5},
		{URL: defaultLRCLibURL, RequestInterval: time.Second, MaxAttempts: 0},
		{URL: defaultLRCLibURL, RequestInterval: time.Second, MaxAttempts: -1},
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

func TestLRCLibEndpointValidation(t *testing.T) {
	t.Parallel()
	for _, endpoint := range []string{"", "/api/get", "//example.org/get", "ftp://example.org/get", "https:///get", "https://:443/get", "https://user@example.org/get", "https://user:pass@example.org/get", "https://example.org/get#fragment", "https://example.org/get#", "https://example.org/%zz", "https://example.org/?x=%zz", "https://exa mple.org/get", "://"} {
		t.Run(endpoint, func(t *testing.T) {
			t.Parallel()
			config := DefaultLRCLibConfig()
			config.URL = endpoint
			_, err := NewLRCLib(config)
			require.Error(t, err)
		})
	}
}

func TestLRCLibEndpointAppID(t *testing.T) {
	t.Parallel()
	for _, endpoint := range []string{"https://lrclib.net/other", "https://LRCLIB.NET:443/get?key=value", "http://lrclib.net/api/get", "https://lrclib.net:8443/api/get", "https://lrclib.net.example.org/api/get"} {
		t.Run(endpoint, func(t *testing.T) {
			t.Parallel()
			config := DefaultLRCLibConfig()
			config.URL = endpoint
			source, err := NewLRCLib(config)
			require.NoError(t, err)
			source.HTTPClient.Transport = clientutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
				if strings.HasPrefix(endpoint, "https://lrclib.net/other") || strings.Contains(endpoint, "LRCLIB.NET:443") {
					assert.Equal(t, lrclibAppID, r.Header.Get("Lrclib-App-Id"))
				} else {
					assert.Empty(t, r.Header.Get("Lrclib-App-Id"))
				}
				return lrclibResponseForTest(200, "", `{}`), nil
			})
			_, err = source.Search(t.Context(), "", "", 0)
			require.NoError(t, err)
		})
	}
}

func TestLRCLibCustomEndpoint(t *testing.T) {
	t.Parallel()
	var calls int
	var previous time.Time
	const interval = 10 * time.Millisecond
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/compat/lrclib/api/get", r.URL.Path)
		assert.Equal(t, []string{"one", "two"}, r.URL.Query()["key"])
		assert.Equal(t, "Björk & friends", r.URL.Query().Get("artist_name"))
		assert.Equal(t, "曲 / ?", r.URL.Query().Get("track_name"))
		if calls < 2 {
			assert.Equal(t, "123", r.URL.Query().Get("duration"))
		} else {
			assert.NotContains(t, r.URL.Query(), "duration")
		}
		assert.Empty(t, r.Header.Get("Lrclib-App-Id"))
		if calls > 0 {
			assert.GreaterOrEqual(t, time.Since(previous), interval)
		}
		previous = time.Now()
		calls++
		switch calls {
		case 1:
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusServiceUnavailable)
		case 2:
			_, err := fmt.Fprint(w, `{"syncedLyrics":"synced","plainLyrics":"plain"}`)
			assert.NoError(t, err)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	config := DefaultLRCLibConfig()
	config.URL = server.URL + "/compat/lrclib/api/get?key=one&key=two&artist_name=old&track_name=old&duration=1"
	config.RequestInterval = interval
	source, err := NewLRCLib(config)
	require.NoError(t, err)
	data, err := source.Search(t.Context(), "Björk & friends", "曲 / ?", 123*time.Second)
	require.NoError(t, err)
	assert.Equal(t, "synced", data)
	_, err = source.Search(t.Context(), "Björk & friends", "曲 / ?", 0)
	require.ErrorIs(t, err, ErrTrackNotFound)
	assert.Equal(t, 3, calls)
	defaultSource, err := NewSource("lrclib")
	require.NoError(t, err)
	assert.Contains(t, fmt.Sprint(defaultSource), "lrclib-url=https://lrclib.net/api/get")
}

func TestLRCLibCustomEndpointRedirect(t *testing.T) {
	t.Parallel()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("redirect was followed")
	}))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer server.Close()
	config := DefaultLRCLibConfig()
	config.URL = server.URL
	source, err := NewLRCLib(config)
	require.NoError(t, err)
	_, err = source.Search(t.Context(), "", "", 0)
	require.ErrorContains(t, err, "302")
}

func TestLRCLibCustomEndpointTimeout(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		err := http.NewResponseController(w).Flush()
		assert.NoError(t, err)
		<-r.Context().Done()
	}))
	defer server.Close()
	config := DefaultLRCLibConfig()
	config.URL = server.URL
	config.Timeout = 50 * time.Millisecond
	source, err := NewLRCLib(config)
	require.NoError(t, err)
	_, err = source.Search(t.Context(), "", "", 0)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}
