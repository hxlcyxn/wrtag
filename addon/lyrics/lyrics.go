package lyrics

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
	var sources lyrics.MultiSource
	for arg := range strings.FieldsSeq(conf) {
		source, err := lyrics.NewSource(arg)
		if err != nil {
			return LyricsAddon{}, fmt.Errorf("source %q: %w", arg, err)
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
