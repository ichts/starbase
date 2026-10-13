package web_test

import (
	"bufio"
	"bytes"
	"context"
	"html"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/zweiundeins/lith/sse"

	"starbase/internal/ui"
)

// TestStreamsAskToReconnectWhenTheServerStops: when the server stops (a
// deploy), a tab's render stream and the telemetry end with sse.Reconnect for
// the elements that opened them, and the stopping server answers new streams
// with 503, which retry 'error' asks again.
func TestStreamsAskToReconnectWhenTheServerStops(t *testing.T) {
	s := startServer(t)
	get(t, s.c, s.ts.URL+"/") // a session

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	open := func(method, path, body string) *http.Response {
		t.Helper()
		req, _ := http.NewRequestWithContext(ctx, method, s.ts.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Datastar-Request", "true")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		req.Header.Set("Accept-Encoding", "identity")
		res, err := s.c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { res.Body.Close() })
		return res
	}
	streams := []struct {
		name string
		body *bufio.Reader
		want []byte
	}{
		{"render stream", bufio.NewReader(open(http.MethodPost, "/", `{"tabid":"tab12345"}`).Body), sse.Reconnect(ui.StreamID)},
		{"telemetry", bufio.NewReader(open(http.MethodGet, "/demo/telemetry", "").Body), sse.Reconnect(ui.TelemetryID)},
	}
	for _, st := range streams {
		for { // the first event: the stream is under way
			line, err := st.body.ReadString('\n')
			if err != nil {
				t.Fatalf("%s: %v", st.name, err)
			}
			if line == "\n" {
				break
			}
		}
	}

	s.stop()
	for _, st := range streams {
		rest, err := io.ReadAll(st.body)
		if err != nil {
			t.Fatalf("%s: %v", st.name, err)
		}
		if !bytes.HasSuffix(rest, st.want) {
			t.Errorf("%s ends with %q, want sse.Reconnect: %q", st.name, rest[max(0, len(rest)-len(st.want)):], st.want)
		}
	}
	for _, r := range []struct{ method, path, body string }{
		{http.MethodPost, "/", `{"tabid":"tab67890"}`},
		{http.MethodGet, "/demo/telemetry", ""},
	} {
		if res := open(r.method, r.path, r.body); res.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("%s %s while stopping = %d, want 503", r.method, r.path, res.StatusCode)
		}
	}
}

// TestTelemetryOpensFromItsElement: Mission Control and every docs example
// open /demo/telemetry from the one #telemetry of their page, with retry
// 'error', the element the stream's last event copies on a deploy.
func TestTelemetryOpensFromItsElement(t *testing.T) {
	ts, c, _, cat := newServerWriter(t)
	pages := []string{"/showcase"}
	for _, comp := range cat.Components {
		if strings.Contains(strings.Join(comp.Examples, "\n"), "/demo/telemetry") {
			pages = append(pages, "/components/"+comp.Slug)
		}
	}
	if len(pages) < 4 {
		t.Fatalf("pages with the telemetry: %v, want the Showcase and the gauge, meter and sparkline docs", pages)
	}
	want := `<div id="telemetry" hidden data-init="@get('/demo/telemetry', {retry: 'error', retryMaxCount: Infinity})"></div>`
	opener := regexp.MustCompile(`<[^<>]*@get\('/demo/telemetry'[^<>]*>(</div>)?`)
	for _, p := range pages {
		_, body := get(t, c, ts.URL+p)
		if n := strings.Count(body, `id="telemetry"`); n != 1 {
			t.Errorf("%s has %d elements with id telemetry, want 1", p, n)
		}
		// Unescaped, the copy button's text of an example is markup too.
		for _, m := range opener.FindAllString(html.UnescapeString(body), -1) {
			if m != want {
				t.Errorf("%s opens the telemetry with %s, want %s", p, m, want)
			}
		}
	}
}
