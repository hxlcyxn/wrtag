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
	"time"

	"go.senan.xyz/wrtag/clientutil"
)

var lrclibBaseURL = `https://lrclib.net/api/get`

// public client-side app id, not a secret
// https://lrclib.net/docs#implementation-requirements-client-identification
// https://lrclib.net/developers/authorizations/i9MlkzOG0ymkTK1HdWKfiaRv6P-Pd4LAK1f38ThSlH0
const lrclibAppID = "i9MlkzOG0ymkTK1HdWKfiaRv6P-Pd4LAK1f38ThSlH0"

const (
	defaultLRCLibTimeout         = 30 * time.Second
	defaultLRCLibRequestInterval = 750 * time.Millisecond
	lrclibMaxAttempts            = 5
	lrclibRetryJitter            = 0.3
)

type LRCLibConfig struct {
	Timeout         time.Duration
	RequestInterval time.Duration
}

func DefaultLRCLibConfig() LRCLibConfig {
	return LRCLibConfig{Timeout: defaultLRCLibTimeout, RequestInterval: defaultLRCLibRequestInterval}
}

type LRCLib struct {
	HTTPClient  *http.Client
	config      LRCLibConfig
	gate        chan struct{}
	nextRequest time.Time
}

func NewLRCLib(config LRCLibConfig) (*LRCLib, error) {
	if config.Timeout < 0 || config.RequestInterval <= 0 {
		return nil, errors.New("LRCLIB requires a nonnegative timeout and positive request interval")
	}
	client := clientutil.Wrap(&http.Client{
		Timeout:       config.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, clientutil.WithoutRetry)
	return &LRCLib{HTTPClient: client, config: config, gate: make(chan struct{}, 1)}, nil
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
		if !retry || attempt >= lrclibMaxAttempts {
			return data, err
		}
	}
}

func (l *LRCLib) String() string {
	return fmt.Sprintf("lrclib lrclib-timeout=%s lrclib-request-interval=%s", l.config.Timeout, l.config.RequestInterval)
}

func (l *LRCLib) search(ctx context.Context, artist, song string, duration time.Duration, attempt int) (string, time.Time, bool, error) {
	u, _ := url.Parse(lrclibBaseURL)
	q := u.Query()
	q.Set("artist_name", artist)
	q.Set("track_name", song)
	if duration > 0 {
		q.Set("duration", fmt.Sprintf("%.0f", duration.Seconds()))
	}
	u.RawQuery = q.Encode()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	req.Header.Set("Lrclib-App-Id", lrclibAppID)

	resp, err := l.HTTPClient.Do(req)
	if err != nil {
		return "", time.Time{}, false, fmt.Errorf("req page: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return "", time.Time{}, false, ErrTrackNotFound
	}
	if resp.StatusCode/100 != 2 {
		retry := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable
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
