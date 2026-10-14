package web_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestStreamsPerClientAreCapped: one client holds at most 12 render streams
// (maxStreamsPerClient), the next gets 429 while another client still gets
// its own, and a closed stream makes room again.
func TestStreamsPerClientAreCapped(t *testing.T) {
	s := startServer(t)
	get(t, s.c, s.ts.URL+"/") // a session
	open := func(ctx context.Context, tab, xff string) *http.Response {
		t.Helper()
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, s.ts.URL+"/", strings.NewReader(`{"tabid":"`+tab+`"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		req.Header.Set("Accept", "text/event-stream")
		if xff != "" {
			req.Header.Set("X-Forwarded-For", xff) // from loopback: the proxy's word
		}
		res, err := s.c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { res.Body.Close() })
		return res
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	first, closeFirst := context.WithCancel(ctx)
	for i := range 12 {
		c := ctx
		if i == 0 {
			c = first
		}
		if res := open(c, fmt.Sprintf("captab%02d", i), ""); res.StatusCode != http.StatusOK {
			t.Fatalf("stream %d = %d, want 200", i, res.StatusCode)
		}
	}
	if res := open(ctx, "captab12", ""); res.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("13th stream = %d, want 429", res.StatusCode)
	}
	if res := open(ctx, "othertab", "203.0.113.9"); res.StatusCode != http.StatusOK {
		t.Fatalf("another client's stream = %d, want 200", res.StatusCode)
	}
	closeFirst()
	for {
		res := open(ctx, "captab13", "")
		if res.StatusCode == http.StatusOK {
			break
		}
		res.Body.Close()
		if ctx.Err() != nil {
			t.Fatal("no room after a stream closed")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
