package lyrics

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go.senan.xyz/wrtag/clientutil"
)

const defaultLRCLibURL = `https://lrclib.net/api/get`

// public client-side app id, not a secret
// https://lrclib.net/docs#implementation-requirements-client-identification
// https://lrclib.net/developers/authorizations/i9MlkzOG0ymkTK1HdWKfiaRv6P-Pd4LAK1f38ThSlH0
const lrclibAppID = "i9MlkzOG0ymkTK1HdWKfiaRv6P-Pd4LAK1f38ThSlH0"

const (
	defaultLRCLibTimeout         = 30 * time.Second
	defaultLRCLibRequestInterval = 750 * time.Millisecond
	defaultLRCLibMaxAttempts     = 5
	lrclibRetryJitter            = 0.3
)

type LRCLibConfig struct {
	URL             string
	Timeout         time.Duration
	RequestInterval time.Duration
	MaxAttempts     int
}

func DefaultLRCLibConfig() LRCLibConfig {
	return LRCLibConfig{URL: defaultLRCLibURL, Timeout: defaultLRCLibTimeout, RequestInterval: defaultLRCLibRequestInterval, MaxAttempts: defaultLRCLibMaxAttempts}
}

type LRCLib struct {
	HTTPClient  *http.Client
	config      LRCLibConfig
	endpoint    *url.URL
	gate        chan struct{}
	nextRequest time.Time
}

func NewLRCLib(config LRCLibConfig) (*LRCLib, error) {
	if config.Timeout < 0 || config.RequestInterval <= 0 {
		return nil, errors.New("LRCLIB requires a nonnegative timeout and positive request interval")
	}
	if config.MaxAttempts <= 0 {
		return nil, errors.New("LRCLIB requires positive max attempts")
	}
	endpoint, err := url.Parse(config.URL)
	if err != nil {
		return nil, fmt.Errorf("invalid LRCLIB URL: %w", err)
	}
	if (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Hostname() == "" || endpoint.User != nil || strings.Contains(config.URL, "#") {
		return nil, errors.New("LRCLIB URL must be absolute HTTP(S) with a host and without credentials or fragments")
	}
	if _, err := url.ParseQuery(endpoint.RawQuery); err != nil {
		return nil, fmt.Errorf("invalid LRCLIB URL query: %w", err)
	}
	client := clientutil.Wrap(&http.Client{
		Timeout:       config.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, clientutil.WithoutRetry)
	return &LRCLib{HTTPClient: client, config: config, endpoint: endpoint, gate: make(chan struct{}, 1)}, nil
}

func waitLRCLib(ctx context.Context, until time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	timer := time.NewTimer(time.Until(until))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (l *LRCLib) Search(ctx context.Context, artist, song string, duration time.Duration) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case l.gate <- struct{}{}:
	}
	defer func() { <-l.gate }()

	if err := waitLRCLib(ctx, l.nextRequest); err != nil {
		return "", err
	}
	if l.config.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, l.config.Timeout)
		defer cancel()
	}

	for attempt := 1; ; attempt++ {
		if err := waitLRCLib(ctx, l.nextRequest); err != nil {
			return "", err
		}
		data, retryAfter, retry, err := l.search(ctx, artist, song, duration, attempt)
		l.nextRequest = time.Now().Add(l.config.RequestInterval)
		if retryAfter.After(l.nextRequest) {
			l.nextRequest = retryAfter
		}
		if !retry || attempt >= l.config.MaxAttempts {
			return data, err
		}
	}
}

func (l *LRCLib) String() string {
	return fmt.Sprintf("lrclib lrclib-url=%s lrclib-timeout=%s lrclib-request-interval=%s lrclib-max-attempts=%d", l.config.URL, l.config.Timeout, l.config.RequestInterval, l.config.MaxAttempts)
}

func (l *LRCLib) search(ctx context.Context, artist, song string, duration time.Duration, attempt int) (string, time.Time, bool, error) {
	u := *l.endpoint
	q := u.Query()
	q.Set("artist_name", artist)
	q.Set("track_name", song)
	q.Del("duration")
	if duration > 0 {
		q.Set("duration", fmt.Sprintf("%.0f", duration.Seconds()))
	}
	u.RawQuery = q.Encode()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if u.Scheme == "https" && strings.EqualFold(u.Hostname(), "lrclib.net") && (u.Port() == "" || u.Port() == "443") {
		req.Header.Set("Lrclib-App-Id", lrclibAppID)
	}

	resp, err := l.HTTPClient.Do(req)
	if err != nil {
		return "", time.Time{}, false, fmt.Errorf("req page: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return "", time.Time{}, false, ErrTrackNotFound
	}
	if resp.StatusCode/100 != 2 {
		retry := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusBadGateway || resp.StatusCode == http.StatusServiceUnavailable
		var until time.Time
		if retry {
			until = lrclibRetryAfter(resp.Header.Get("Retry-After"), attempt, time.Now())
		}
		return "", until, retry, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var result lrclibResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", time.Time{}, false, fmt.Errorf("decode response: %w", err)
	}

	// prefer synced lyrics if available, otherwise fall back to plain lyrics
	if result.SyncedLyrics != "" {
		return result.SyncedLyrics, time.Time{}, false, nil
	}
	if result.PlainLyrics != "" {
		return result.PlainLyrics, time.Time{}, false, nil
	}

	return "", time.Time{}, false, nil
}

func lrclibRetryAfter(value string, attempt int, now time.Time) time.Time {
	wait := time.Duration(attempt) * time.Second
	const maxDelay = time.Duration(1<<63 - 1)
	if seconds, err := strconv.ParseUint(value, 10, 64); err == nil || errors.Is(err, strconv.ErrRange) {
		wait = maxDelay
		if err == nil && seconds <= uint64(maxDelay/time.Second) {
			wait = time.Duration(seconds) * time.Second
		}
	} else if date, err := http.ParseTime(value); err == nil {
		wait = max(0, date.Sub(now))
	}
	const jitterSteps = 1000
	random, err := rand.Int(rand.Reader, big.NewInt(jitterSteps))
	if err != nil {
		return now.Add(wait)
	}
	jitter := time.Duration(float64(wait) * lrclibRetryJitter * float64(random.Int64()) / jitterSteps)
	return now.Add(wait).Add(jitter)
}

type lrclibResponse struct {
	ID           int     `json:"id"`
	Name         string  `json:"name"`
	TrackName    string  `json:"trackName"`
	ArtistName   string  `json:"artistName"`
	AlbumName    string  `json:"albumName"`
	Duration     float64 `json:"duration"`
	Instrumental bool    `json:"instrumental"`
	PlainLyrics  string  `json:"plainLyrics"`
	SyncedLyrics string  `json:"syncedLyrics"`
}
