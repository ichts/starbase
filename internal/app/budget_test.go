package app_test

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"runtime/debug"
	"testing"
	"time"

	"github.com/zweiundeins/lith/live"
	"github.com/zweiundeins/lith/sse"

	"starbase/internal/app"
)

// TestBrotliStreamsStayCompressed: under production's memory limit, 60
// brotli-only streams (Safari) all get brotli. With lith's default budget,
// half of MemoryLimit, about 48 did and the rest went out uncompressed.
func TestBrotliStreamsStayCompressed(t *testing.T) {
	defer debug.SetMemoryLimit(debug.SetMemoryLimit(app.MemoryLimit))
	hub := live.New(app.HubOptions(nil, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Minute))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { hub.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	view := live.View{Key: "page", Render: func(context.Context, *sql.Tx) (any, error) {
		return sse.PatchElements([]byte(`<div id="app">frame</div>`)), nil
	}}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hub.Serve(w, r, live.Spec{Views: []live.View{view}})
	}))
	defer ts.Close()

	const n = 60
	tr := &http.Transport{DisableCompression: true, MaxConnsPerHost: n + 1}
	defer tr.CloseIdleConnections()
	c := &http.Client{Transport: tr}
	for i := range n {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL, nil)
		req.Header.Set("Accept-Encoding", "gzip, deflate, br")
		res, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if got := res.Header.Get("Content-Encoding"); got != "br" {
			t.Fatalf("stream %d in %q, want br (budget %+v)", i, got, hub.Budget().Stats())
		}
	}
	if st := hub.Budget().Stats(); st.Streams["br q4 2^19"] != n {
		t.Errorf("streams by tier %v, want %d br q4 2^19", st.Streams, n)
	}
}
