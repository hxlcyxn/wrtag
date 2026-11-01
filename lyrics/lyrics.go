// Package lyrics provides functionality for fetching song lyrics from various sources.
// It supports multiple lyrics providers including LRCLib, Genius, and Musixmatch.
package lyrics

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"go.senan.xyz/wrtag/clientutil"
	"golang.org/x/net/html"
	"golang.org/x/time/rate"
)

type Source interface {
	Search(ctx context.Context, artist, song string, duration time.Duration) (string, error)
}

const browserUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/150.0.0.0 Safari/537.36"

func NewSource(name string) (Source, error) {
	browserClient := clientutil.Wrap(nil,
		clientutil.WithUserAgent(browserUA),
	)
	switch name {
	case "genius":
		return &Genius{browserClient, rate.NewLimiter(rate.Every(500*time.Millisecond), 1)}, nil
	case "musixmatch":
		return &Musixmatch{browserClient, rate.NewLimiter(rate.Every(500*time.Millisecond), 1)}, nil
	case "lrclib":
		return &LRCLib{&http.Client{}, rate.NewLimiter(rate.Every(time.Minute/180), 1)}, nil
	default:
		return nil, errors.New("unknown source")
	}
}

var ErrTrackNotFound = errors.New("track not found")
var ErrProviderFailed = errors.New("lyrics provider failed")

type MultiSource []Source

func (ms MultiSource) Search(ctx context.Context, artist, song string, duration time.Duration) (string, error) {
	var failures []error
	for _, src := range ms {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		lyricData, err := src.Search(ctx, artist, song, duration)
		if parentErr := ctx.Err(); parentErr != nil {
			return "", parentErr
		}
		if err == nil {
			return lyricData, nil
		}
		if errors.Is(err, ErrTrackNotFound) {
			continue
		}
		failure := fmt.Errorf("source %v, artist %q, track %q: %w", src, artist, song, err)
		failures = append(failures, failure)
		slog.WarnContext(ctx, "lyrics provider failed", "source", fmt.Sprint(src), "artist", artist, "track", song, "error", err)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if len(failures) != 0 {
		return "", errors.Join(append([]error{ErrProviderFailed}, failures...)...)
	}
	return "", ErrTrackNotFound
}

func (ms MultiSource) String() string {
	var parts []string
	for _, s := range ms {
		parts = append(parts, fmt.Sprint(s))
	}
	return strings.Join(parts, ", ")
}

func findDocumentText(n *html.Node, buf *strings.Builder) {
	if n == nil {
		return
	}

	for n := range n.Descendants() {
		switch n.Type {
		case html.TextNode:
			buf.WriteString(n.Data)
		case html.ElementNode:
			switch n.Data {
			case "p", "div", "h1", "h2", "h3", "h4", "h5", "h6", "br":
				buf.WriteString("\n")
			}
		}
	}
}
