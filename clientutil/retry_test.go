package clientutil_test

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.senan.xyz/wrtag/clientutil"
)

func TestWithRetryStatus(t *testing.T) {
	t.Parallel()

	for _, status := range []int{
		http.StatusOK,
		http.StatusNotFound,
		http.StatusInternalServerError,
		http.StatusTooManyRequests,
		http.StatusServiceUnavailable,
	} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				const maxAttempts = 3
				calls := 0
				transport := clientutil.WithRetry(maxAttempts)(clientutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
					calls++
					return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
				}))
				request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://example.com", nil)
				require.NoError(t, err)

				response, err := transport.RoundTrip(request)
				require.NoError(t, err)
				require.NoError(t, response.Body.Close())
				assert.Equal(t, status, response.StatusCode)

				expectedCalls := 1
				if status == http.StatusInternalServerError || status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable {
					expectedCalls = maxAttempts
				}
				assert.Equal(t, expectedCalls, calls)
			})
		})
	}
}
