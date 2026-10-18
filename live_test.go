package popnow

import (
	"context"
	"errors"
	"net/http"
	"os"
	"testing"
	"time"
)

// TestLiveUnauthorizedEnvelope sends one real request with a fake key and checks that the answer is
// the documented 401 envelope, not an edge error page. It is the cheapest end-to-end proof that the
// base URL, the User-Agent and the envelope decoding all agree with production, and it costs
// nothing because the key is refused before anything runs.
//
// It needs the network, so it only runs with POPNOW_LIVE_TESTS=1.
func TestLiveUnauthorizedEnvelope(t *testing.T) {
	if os.Getenv("POPNOW_LIVE_TESTS") == "" {
		t.Skip("set POPNOW_LIVE_TESTS=1 to send one request to the live API")
	}
	client, err := New(Options{
		APIKey:     "sk-popnow-000000000000000000000000000000000000000000000000",
		MaxRetries: -1,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err = client.GetBalance(ctx)
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("want *APIError, got %T: %v", err, err)
	}
	t.Logf("live answer: %v", apiErr)
	if apiErr.Status != http.StatusUnauthorized || apiErr.Code != CodeUnauthorized {
		t.Errorf("status %d code %d, want the 401 envelope", apiErr.Status, apiErr.Code)
	}
	if apiErr.RequestID == "" {
		t.Error("the 401 envelope carried no request_id")
	}
}
