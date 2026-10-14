package web_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"starbase/internal/app"
	"starbase/internal/commands"
	"starbase/internal/config"
)

// tab is a render stream a test holds open, reading what it sends.
type tab struct {
	res    *http.Response
	cancel context.CancelFunc
	read   atomic.Int64 // bytes on the wire so far
	done   chan struct{}
}

func (b *tab) close() {
	b.cancel()
	<-b.done
}

// openTab opens the render stream of path for tab id, as a browser that
// accepts ae on an address of its own (X-Forwarded-For, the proxy's word
// from loopback), so the per-client cap stays out of the way. It reads the
// stream until it ends.
func openTab(t *testing.T, url string, c *http.Client, id, ae string, n int) *tab {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(`{"tabid":"`+id+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Accept-Encoding", ae)
	req.Header.Set("X-Forwarded-For", fmt.Sprintf("10.%d.%d.%d", n>>16&255, n>>8&255, n&255))
	res, err := c.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	b := &tab{res: res, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(b.done)
		defer res.Body.Close()
		buf := make([]byte, 32<<10)
		for {
			n, err := res.Body.Read(buf)
			b.read.Add(int64(n))
			if err != nil {
				return
			}
		}
	}()
	return b
}

// retryTab asks for tab id's stream as retry 'error' does on the page:
// again after 400 ms, doubling up to 4 s, for as long as within. It
// returns the stream that got in, or nil, and the 503s it got.
func retryTab(t *testing.T, url string, c *http.Client, id, ae string, n int, within time.Duration) (*tab, int) {
	t.Helper()
	refused := 0
	wait := 400 * time.Millisecond
	for end := time.Now().Add(within); ; {
		b := openTab(t, url, c, id, ae, n)
		switch b.res.StatusCode {
		case http.StatusOK:
			return b, refused
		case http.StatusServiceUnavailable:
			refused++
		default:
			t.Fatalf("tab %s: %d", id, b.res.StatusCode)
		}
		b.close()
		if time.Now().Add(wait).After(end) {
			return nil, refused
		}
		time.Sleep(wait)
		wait = min(2*wait, 4*time.Second)
	}
}

// peaks samples the process's heap in use, the Go runtime's memory (what
// GOMEMLIMIT bounds) and its resident set until the function it returns is
// called, which reports their peaks in MiB.
func peaks() func() (heap, gomem, rss float64) {
	var mu sync.Mutex
	var heap, gomem, rss uint64
	sample := func() {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		r := residentSet()
		mu.Lock()
		heap, gomem, rss = max(heap, m.HeapInuse), max(gomem, m.Sys-m.HeapReleased), max(rss, r)
		mu.Unlock()
	}
	quit, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		for {
			sample()
			select {
			case <-quit:
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
	}()
	return func() (float64, float64, float64) {
		close(quit)
		<-done
		return float64(heap) / (1 << 20), float64(gomem) / (1 << 20), float64(rss) / (1 << 20)
	}
}

// residentSet is the process's resident set in bytes (Linux; 0 elsewhere).
func residentSet() uint64 {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0
	}
	for line := range strings.Lines(string(b)) {
		if v, ok := strings.CutPrefix(line, "VmRSS:"); ok {
			kb, _ := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(v), " kB"), 10, 64)
			return kb << 10
		}
	}
	return 0
}

// TestStreamBudgetCapsTabs: memory caps the render streams, not a count.
// Under production's hub options and memory limit, tabs open one by one,
// each retrying a 503 as the page does, until the budget of 150 MiB turns
// one away for good. Every stream it lets in compresses (none goes out
// uncompressed or in raw blocks). On the data table's page, the largest,
// that is about 130 tabs with zstd and 97 with brotli only (each tab's
// view holds a frame of 230 KB); once three close, the tab turned away gets
// in. On a page with small frames more than 150 get in. The heartbeat is
// 200 ms instead of 5 s: zstd streams move down to the window their frames
// need at their next write after a stream found no room, and the ping is
// what they write while the page is still.
func TestStreamBudgetCapsTabs(t *testing.T) {
	if testing.Short() {
		t.Skip("opens hundreds of streams")
	}
	defer debug.SetMemoryLimit(debug.SetMemoryLimit(app.MemoryLimit))
	const zstdOrBr, brOnly = "gzip, deflate, br, zstd", "gzip, deflate, br"
	for _, c := range []struct {
		name, path, ae string
		least, most    int // tabs in when full
	}{
		{"data table, zstd", "/components/data-table", zstdOrBr, 120, 145},
		{"data table, brotli", "/components/data-table", brOnly, 90, 105},
		{"small frames, zstd", "/about", zstdOrBr, 151, 400},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := startServerWith(t, config.Config{Heartbeat: 200 * time.Millisecond})
			url := s.ts.URL + c.path
			get(t, s.c, url) // a session
			tr := &http.Transport{DisableCompression: true, MaxIdleConnsPerHost: -1}
			defer tr.CloseIdleConnections()
			cl := &http.Client{Transport: tr, Jar: s.c.Jar}
			want := map[string]string{zstdOrBr: "zstd", brOnly: "br"}[c.ae]
			runtime.GC()
			before := residentSet()
			peak := peaks()
			var open []*tab
			defer func() {
				for _, b := range open {
					b.close()
				}
			}()
			last, refused := "", 0
			for i := 0; ; i++ {
				id := fmt.Sprintf("budgettab%04d", i)
				b, n := retryTab(t, url, cl, id, c.ae, i, 3*time.Second)
				refused += n
				if b == nil {
					last = id
					break
				}
				if got := b.res.Header.Get("Content-Encoding"); got != want {
					t.Fatalf("tab %d in %q, want %s", i, got, want)
				}
				open = append(open, b)
				for end := time.Now().Add(10 * time.Second); b.read.Load() == 0; time.Sleep(time.Millisecond) {
					if time.Now().After(end) {
						t.Fatalf("tab %d: no first frame", i)
					}
				}
				if st := s.hub.Budget().Stats(); st.Streams["identity"] > 0 || st.Streams["zstd asleep"] > 0 {
					t.Fatalf("tab %d: a stream without an encoder after its first frame: %v", i, st.Streams)
				}
				if len(open) > c.most {
					break
				}
			}
			st := s.hub.Budget().Stats()
			heap, gomem, rss := peak()
			t.Logf("%d tabs in, %d 503s; %v; %.1f of %.0f MiB booked, %.1f MiB of it frames; peaks: heap %.0f MiB, Go %.0f MiB, resident %.0f MiB (%.0f before)",
				len(open), refused, st.Streams, float64(st.Reserved)/(1<<20), float64(st.Limit)/(1<<20), float64(st.Frames)/(1<<20),
				heap, gomem, rss, float64(before)/(1<<20))
			if len(open) < c.least || len(open) > c.most || int64(refused) != st.Refused {
				t.Fatalf("%d tabs in, want %d to %d; %d 503s, the budget counts %d", len(open), c.least, c.most, refused, st.Refused)
			}
			if last == "" {
				return
			}

			// Every tab renders again, as after a command that touches all.
			peak = peaks()
			for range 3 {
				s.hub.Changed([]string{commands.TopicAll})
				time.Sleep(time.Second)
			}
			heap, gomem, rss = peak()
			t.Logf("every tab rendered three times: peaks: heap %.0f MiB, Go %.0f MiB, resident %.0f MiB", heap, gomem, rss)

			for _, b := range open[:3] {
				b.close()
			}
			open = open[3:]
			b, _ := retryTab(t, url, cl, last, c.ae, len(open)+3, 20*time.Second)
			if b == nil {
				t.Fatal("the tab turned away did not get in once three tabs closed")
			}
			open = append(open, b)
			if got := b.res.Header.Get("Content-Encoding"); got != want {
				t.Fatalf("the tab that got in at last: %q, want %s", got, want)
			}
		})
	}
}
