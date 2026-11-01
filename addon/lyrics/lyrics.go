package lyrics

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.senan.xyz/wrtag/addon"
	"go.senan.xyz/wrtag/lyrics"
	"go.senan.xyz/wrtag/tags"
	"go.senan.xyz/wrtag/tags/normtag"
)

func init() {
	addon.Register("lyrics", NewLyricsAddon)
}

type LyricsAddon struct {
	source lyrics.Source
}

func NewLyricsAddon(conf string) (LyricsAddon, error) {
	config := lyrics.DefaultLRCLibConfig()
	var names []string
	seen := make(map[string]bool)
	hasLRCLib := false
	for arg := range strings.FieldsSeq(conf) {
		name, value, option := strings.Cut(arg, "=")
		if !option {
			names = append(names, name)
			hasLRCLib = hasLRCLib || name == "lrclib"
			continue
		}
		if seen[name] {
			return LyricsAddon{}, fmt.Errorf("duplicate option %q", name)
		}
		seen[name] = true
		if name == "lrclib-url" {
			config.URL = value
			continue
		}
		if name == "lrclib-max-attempts" {
			attempts, err := strconv.Atoi(value)
			if err != nil || attempts <= 0 {
				return LyricsAddon{}, fmt.Errorf("invalid max attempts: %q", value)
			}
			config.MaxAttempts = attempts
			continue
		}
		if name != "lrclib-timeout" && name != "lrclib-request-interval" {
			return LyricsAddon{}, fmt.Errorf("unknown option %q", name)
		}
		duration, err := time.ParseDuration(value)
		if err != nil || duration < 0 || (name == "lrclib-request-interval" && duration == 0) {
			return LyricsAddon{}, fmt.Errorf("invalid duration for %q: %q", name, value)
		}
		if name == "lrclib-timeout" {
			config.Timeout = duration
		} else {
			config.RequestInterval = duration
		}
	}
	if len(seen) > 0 && !hasLRCLib {
		return LyricsAddon{}, errors.New("LRCLIB options require an lrclib source")
	}

	var sources lyrics.MultiSource
	for _, name := range names {
		var source lyrics.Source
		var err error
		if name == "lrclib" {
			source, err = lyrics.NewLRCLib(config)
		} else {
			source, err = lyrics.NewSource(name)
		}
		if err != nil {
			return LyricsAddon{}, fmt.Errorf("source %q: %w", name, err)
		}
		sources = append(sources, source)
	}
	if len(sources) == 0 {
		return LyricsAddon{}, errors.New("no lyrics sources provided")
	}

	return LyricsAddon{sources}, nil
}

func (l LyricsAddon) Check() error {
	return nil
}

func (l LyricsAddon) ProcessRelease(ctx context.Context, cover string, paths []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var wg sync.WaitGroup

	var pathErrs = make([]error, len(paths))
	for i, path := range paths {
		wg.Go(func() {
			pathErrs[i] = func() error {
				t, err := tags.ReadTags(path)
				if err != nil {
					return fmt.Errorf("read first: %w", err)
				}
				if normtag.Get(t, normtag.Lyrics) != "" {
					return nil
				}

				var duration time.Duration
				if props, err := tags.ReadProperties(path); err == nil {
					duration = props.Length
				}

				lyricData, err := l.source.Search(ctx, normtag.Get(t, normtag.ArtistCredit), normtag.Get(t, normtag.Title), duration)
				if parentErr := ctx.Err(); parentErr != nil {
					return parentErr
				}
				if err != nil && !errors.Is(err, lyrics.ErrTrackNotFound) {
					slog.WarnContext(ctx, "leaving lyrics unchanged after provider failure", "path", path, "error", err)
					return nil
				}

				lt := map[string][]string{}
				normtag.Set(lt, normtag.Lyrics, lyricData)

				if err := tags.WriteTags(path, lt, 0); err != nil {
					return fmt.Errorf("write new lyrics: %w", err)
				}
				return nil
			}()
		})
	}

	wg.Wait()

	return errors.Join(append(pathErrs, ctx.Err())...)
}

func (l LyricsAddon) String() string {
	return fmt.Sprintf("lyrics (%s)", l.source)
}
