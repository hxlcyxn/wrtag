package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
	"go.senan.xyz/sqlb"
	"go.senan.xyz/wrtag"
	"go.senan.xyz/wrtag/musicbrainz"
)

func TestJobPublicationMode(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{"", "no-replace directory publication", "prepared per-file fallback: partial visibility and failed rollback possible"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()

			job := Job{
				Status: StatusComplete,
				SearchResult: sqlb.NewJSON(&wrtag.SearchResult{
					Release:         &musicbrainz.Release{ID: "release"},
					PublicationMode: mode,
				}),
			}
			var output bytes.Buffer
			require.NoError(t, uiTmpl.ExecuteTemplate(&output, "job", job))
			if mode == "" {
				require.NotContains(t, output.String(), "publication:")
			} else {
				require.Contains(t, output.String(), "publication: "+mode)
			}
		})
	}
}
