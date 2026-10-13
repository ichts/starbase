package web_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zweiundeins/lith/live"
	"github.com/zweiundeins/lith/writer"

	"starbase/components"
	"starbase/content"
	"starbase/internal/app"
	"starbase/internal/catalog"
	"starbase/internal/commands"
	"starbase/internal/config"
	"starbase/internal/db"
	"starbase/internal/queries"
	"starbase/internal/tscheck"
	"starbase/internal/web"
	"starbase/static"
)

// openStream opens a tab's render stream, uncompressed, and returns a
// function that waits for its next event, and one that closes it.
func openStream(t *testing.T, c *http.Client, url, tabID string) (next func() string, stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(`{"tabid":"`+tabID+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Accept-Encoding", "identity")
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	events := make(chan string)
	go func() {
		defer res.Body.Close()
		defer close(events)
		sc := bufio.NewScanner(res.Body)
		sc.Buffer(make([]byte, 1<<20), 8<<20)
		var e strings.Builder
		for sc.Scan() {
			if sc.Text() != "" || e.Len() == 0 {
				e.WriteString(sc.Text() + "\n")
				continue
			}
			select {
			case events <- e.String():
			case <-ctx.Done():
				return
			}
			e.Reset()
		}
	}()
	return func() string {
		t.Helper()
		select {
		case e, ok := <-events:
			if !ok {
				t.Fatal("the stream ended")
			}
			return e
		case <-time.After(5 * time.Second):
			t.Fatal("no event for 5 s")
		}
		return ""
	}, cancel
}

// until returns the first of the stream's next events that contains want.
func until(t *testing.T, next func() string, want string) string {
	t.Helper()
	for range 10 {
		if e := next(); strings.Contains(e, want) {
			return e
		}
	}
	t.Fatalf("no event with %q in ten", want)
	return ""
}

// The Showcase's "N watching" is a view that its viewers share: a join or a
// leave sends the others the count, not their whole page again.
func TestShowcaseViewers(t *testing.T) {
	ts, c := newServer(t)
	if _, page := get(t, c, ts.URL+"/showcase"); !strings.Contains(page, `<span id="board-viewers">1</span> watching`) {
		t.Fatal("the document lacks the viewer count")
	}
	a, _ := openStream(t, c, ts.URL+"/showcase", "taba0000")
	until(t, a, `id="app"`)
	b, leave := openStream(t, c, ts.URL+"/showcase", "tabb0000")
	until(t, b, `<span id="board-viewers">2</span>`)
	if e := until(t, a, `<span id="board-viewers">2</span>`); strings.Contains(e, `id="app"`) {
		t.Error("a join sent the other viewer its whole page")
	}
	leave()
	if e := until(t, a, `<span id="board-viewers">1</span>`); strings.Contains(e, `id="app"`) {
		t.Error("a leave sent the other viewer its whole page")
	}
}

// join opens a render stream on /showcase with accept as its
// Accept-Encoding and waits for its first bytes, or with identity for its
// page; then it reads the rest away until the stream ends.
func join(b *testing.B, ctx context.Context, client *http.Client, url, tabID, accept string, wg *sync.WaitGroup) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url+"/showcase", strings.NewReader(`{"tabid":"`+tabID+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Datastar-Request", "true")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Accept-Encoding", accept)
	res, err := client.Do(req)
	if err != nil {
		b.Fatal(err)
	}
	br := bufio.NewReaderSize(res.Body, 4096)
	if accept == "identity" {
		for {
			line, err := br.ReadSlice('\n')
			if err != nil && err != bufio.ErrBufferFull {
				b.Fatalf("%s: %v", tabID, err)
			}
			if strings.Contains(string(line), `id="app"`) {
				break
			}
		}
	} else if _, err := br.Peek(1); err != nil {
		b.Fatalf("%s: %v", tabID, err)
	}
	wg.Go(func() {
		io.Copy(io.Discard, br)
		res.Body.Close()
	})
}

// settle waits until n has not changed for a second.
func settle(n func() int64) {
	for last, still := n(), 0; still < 10; time.Sleep(100 * time.Millisecond) {
		if now := n(); now != last {
			last, still = now, 0
		} else {
			still++
		}
	}
}

// BenchmarkShowcaseJoins opens 100 render streams on /showcase one after
// another, each once its page has arrived, and reports the renders and read
// transactions that took until they settled. Then it opens 100 more as
// browsers that accept zstd and reports the heap each open stream holds
// with the app's stream options.
//
//	go test ./internal/web -run '^$' -bench ShowcaseJoins -benchtime 1x
func BenchmarkShowcaseJoins(b *testing.B) {
	const streams = 100
	for range b.N {
		ctx, cancel := context.WithCancel(context.Background())
		log := slog.New(slog.NewTextHandler(io.Discard, nil))
		path := filepath.Join(b.TempDir(), "bench.db")
		d, err := db.Open(ctx, path)
		if err != nil {
			b.Fatal(err)
		}
		cat, err := catalog.Load(components.FS)
		if err != nil {
			b.Fatal(err)
		}
		hub := live.New(app.HubOptions(d.R, log))
		hubDone := make(chan struct{})
		go func() { hub.Run(ctx); close(hubDone) }()
		wr, err := writer.New(d, writer.Options{OnCommit: hub.Changed, Log: log})
		if err != nil {
			b.Fatal(err)
		}
		go wr.Run(ctx)
		if err := wr.Do(ctx, commands.SyncCatalog{Catalog: cat, Datastar: static.Datastar()}); err != nil {
			b.Fatal(err)
		}
		if err := wr.Do(ctx, commands.SeedBoard{}); err != nil {
			b.Fatal(err)
		}
		ts := httptest.NewUnstartedServer(nil)
		ts.Start()
		cfg := config.Config{BaseURL: ts.URL, RepoURL: "https://example.com/repo"}
		srv := web.New(ctx, web.Deps{Config: cfg, Log: log, Writer: wr, Hub: hub, Queries: queries.New(d),
			Catalog: cat, StaticFS: static.FS, Content: content.FS, Checker: tscheck.New(b.TempDir())})
		ts.Config.Handler = srv.Handler()
		client := &http.Client{Transport: &http.Transport{DisableCompression: true, MaxIdleConnsPerHost: 2 * streams}}
		if res, err := client.Get(ts.URL + "/showcase"); err != nil {
			b.Fatal(err)
		} else {
			io.Copy(io.Discard, res.Body)
			res.Body.Close()
		}
		renders := func() int64 { return hub.Stats().Renders }

		b.ResetTimer()
		r0, tx0 := renders(), hub.Stats().ReadTxs
		joins, stop := context.WithCancel(ctx)
		var wg sync.WaitGroup
		for i := range streams {
			join(b, joins, client, ts.URL, fmt.Sprintf("join%05d", i), "identity", &wg)
		}
		settle(renders)
		b.ReportMetric(float64(renders()-r0), "renders")
		b.ReportMetric(float64(hub.Stats().ReadTxs-tx0), "readtx")
		stop()
		wg.Wait()
		settle(func() int64 { return int64(hub.Stats().Streams) })

		runtime.GC()
		var m0, m1 runtime.MemStats
		runtime.ReadMemStats(&m0)
		for i := range streams {
			join(b, ctx, client, ts.URL, fmt.Sprintf("heap%05d", i), "gzip, deflate, br, zstd", &wg)
		}
		settle(renders)
		b.StopTimer()
		runtime.GC()
		runtime.ReadMemStats(&m1)
		b.ReportMetric(float64(int64(m1.HeapAlloc)-int64(m0.HeapAlloc))/streams/(1<<20), "MiB/stream")

		cancel()
		ts.CloseClientConnections()
		wg.Wait()
		ts.Close()
		<-hubDone
		wr.Close()
		d.Close()
	}
}
