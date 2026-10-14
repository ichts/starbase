package app_test

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"runtime/debug"
	"slices"
	"testing"
	"time"

	"github.com/zweiundeins/lith/live"
	"github.com/zweiundeins/lith/sse"

	"starbase/internal/app"
)

// TestBrotliStreamsStayCompressed: under production's memory limit, the
// first 20 streams get their largest tier, and a brotli stream goes out
// uncompressed only once the budget cannot hold its smallest tier (q3 2^16,
// 912 KiB). Brotli streams keep their tier until they close, so 150 MiB
// cannot hold all 150 of maxStreams: brotli-only clients (Safari before
// 26.3) get 93 compressed streams, and half zstd, half brotli (alternating,
// without the frames that would move zstd down) get 53 brotli, and every
// zstd stream is zstd (22 send raw blocks until room frees up). Before lith's curve, 57 brotli-only streams were compressed.
func TestBrotliStreamsStayCompressed(t *testing.T) {
	defer debug.SetMemoryLimit(debug.SetMemoryLimit(app.MemoryLimit))
	const zstdOrBr, brOnly = "gzip, deflate, br, zstd", "gzip, deflate, br"
	for _, c := range []struct {
		name    string
		clients []string // Accept-Encoding of each stream, in the order they open
		br      int      // brotli streams compressed at least
	}{
		{"brotli", slices.Repeat([]string{brOnly}, 150), 93},
		{"mixed", slices.Repeat([]string{zstdOrBr, brOnly}, 75), 53},
	} {
		t.Run(c.name, func(t *testing.T) {
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

			tr := &http.Transport{DisableCompression: true, MaxConnsPerHost: len(c.clients) + 1}
			defer tr.CloseIdleConnections()
			cl := &http.Client{Transport: tr}
			br := 0
			for i, ae := range c.clients {
				before := hub.Budget().Stats()
				req, _ := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL, nil)
				req.Header.Set("Accept-Encoding", ae)
				res, err := cl.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer res.Body.Close()
				got, st := res.Header.Get("Content-Encoding"), hub.Budget().Stats()
				switch {
				case ae == zstdOrBr && got != "zstd":
					t.Fatalf("stream %d in %q, want zstd (budget %+v)", i, got, st)
				case got == "br":
					br++
				case ae == brOnly && st.Limit-st.Reserved >= 912<<10:
					t.Fatalf("stream %d in %q with %d KiB left (budget %+v)", i, got, (st.Limit-st.Reserved)>>10, st)
				}
				if i < 20 {
					want := "br q4 2^19"
					if ae == zstdOrBr {
						want = "zstd 2^19 low memory"
					}
					if st.Streams[want] != before.Streams[want]+1 {
						t.Fatalf("stream %d of a quiet server: %v, then %v", i, before.Streams, st.Streams)
					}
				}
			}
			st := hub.Budget().Stats()
			if br < c.br || st.Reserved > st.Limit {
				t.Errorf("%d brotli streams compressed, want %d: %v, %d of %d bytes reserved", br, c.br, st.Streams, st.Reserved, st.Limit)
			}
			t.Logf("streams by tier %v, %d MiB reserved", st.Streams, st.Reserved>>20)
		})
	}
}
