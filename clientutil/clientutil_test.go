package clientutil_test

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.senan.xyz/wrtag/clientutil"
)

func TestWithoutRetry(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		transport := clientutil.WithRetry(5)(clientutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
		}))
		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://example.com", nil)
		require.NoError(t, err)
		response, err := clientutil.WithoutRetry(transport).RoundTrip(request)
		require.NoError(t, err)
		require.NoError(t, response.Body.Close())
		assert.Equal(t, 1, calls)

		response, err = transport.RoundTrip(request)
		require.NoError(t, err)
		require.NoError(t, response.Body.Close())
		assert.Equal(t, 6, calls)
	})
}
