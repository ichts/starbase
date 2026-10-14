package app_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"starbase/internal/app"
)

// TestWatchdogReopensASilentStream: a render stream whose connection drops
// without closing (no FIN or RST) goes quiet, its heartbeats ($_ping) stop,
// and the page's watchdog re-creates #stream after three missed ones. The
// new stream carries the next command's outcome. A stream that keeps pinging
// is left alone.
func TestWatchdogReopensASilentStream(t *testing.T) {
	chrome := findChrome(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + ln.Addr().String()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cfg := appConfig(t, base)
	cfg.Heartbeat = 300 * time.Millisecond
	copySeeded(t, cfg.DBPath)
	a, err := app.New(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })

	var mu sync.Mutex
	var streams []*silencer // the page's render streams, in the order they opened
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Accept") != "text/event-stream" {
			a.Handler.ServeHTTP(w, r)
			return
		}
		s := &silencer{ResponseWriter: w}
		mu.Lock()
		streams = append(streams, s)
		mu.Unlock()
		a.Handler.ServeHTTP(s, r)
	})
	stall := func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		streams[len(streams)-1].silent.Store(true)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}
	results := serveProbe(t, ln, "/showcase", watchdogJS, h, map[string]http.HandlerFunc{"/__probe/stall": stall})
	got := string(runChrome(t, chrome, base+"/showcase", results))
	mu.Lock()
	n := len(streams)
	mu.Unlock()
	if want := "steady, reopened, 30%"; got != want || n != 2 {
		t.Fatalf("the page shows %q after %d streams, want %q after 2", got, n, want)
	}
}

// silencer passes a stream on until it goes silent, and then swallows it, as
// a connection that dropped without closing would.
type silencer struct {
	http.ResponseWriter
	silent atomic.Bool
}

func (s *silencer) Write(b []byte) (int, error) {
	if s.silent.Load() {
		return len(b), nil
	}
	return s.ResponseWriter.Write(b)
}

func (s *silencer) Unwrap() http.ResponseWriter { return s.ResponseWriter }

const watchdogJS = `
const until = async (f, ms) => {
	for (const end = Date.now() + ms; Date.now() < end; await new Promise((r) => setTimeout(r, 50))) {
		const v = f()
		if (v) return v
	}
	return ''
}
const clock = () => document.querySelector('.mission__clock')?.textContent ?? ''
const c = clock()
await until(() => clock() !== c, 15000)
const thrust = () => [...document.querySelectorAll('.flight__state dd')].map((d) => d.textContent).find((t) => t.endsWith('%')) ?? ''
const command = async (value, ms) => {
	document.querySelector('sb-slider[name="thrust"]').dispatchEvent(new CustomEvent('sb-change', { detail: { name: 'thrust', value }, bubbles: true, composed: true }))
	return (await until(() => thrust() === value + '%' && thrust(), ms)) || thrust() || 'nothing'
}
const out = []
const first = document.getElementById('stream')
// Six heartbeats with pings: the watchdog keeps still.
await new Promise((r) => setTimeout(r, 1800))
out.push(document.getElementById('stream') === first ? 'steady' : 'reopened too early')
await fetch('/__probe/stall', { method: 'POST' })
out.push((await until(() => document.getElementById('stream') !== first, 10000)) ? 'reopened' : 'still silent')
out.push(await command(30, 10000))
await fetch('/__probe/result', { method: 'POST', body: out.join(', ') })
`
