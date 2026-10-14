package web_test

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
	"github.com/zweiundeins/lith/sse"

	"starbase/internal/ui"
)

// TestStreamsAskToReconnectWhenTheServerStops: when the server stops (a
// deploy), a tab's render stream and the telemetry end with sse.Reconnect for
// the elements that opened them, in every encoding, and end their compressed
// streams cleanly; the stopping server answers new streams with 503, which
// retry 'error' asks again.
func TestStreamsAskToReconnectWhenTheServerStops(t *testing.T) {
	s := startServer(t)
	get(t, s.c, s.ts.URL+"/") // a session

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	open := func(method, path, body, encoding string) *http.Response {
		t.Helper()
		req, _ := http.NewRequestWithContext(ctx, method, s.ts.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Datastar-Request", "true")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		req.Header.Set("Accept-Encoding", encoding)
		res, err := s.c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { res.Body.Close() })
		return res
	}
	type stream struct {
		name string
		body *bufio.Reader
		want []byte
	}
	var streams []stream
	for i, enc := range []string{"identity", "gzip", "br", "zstd"} {
		page := open(http.MethodPost, "/", fmt.Sprintf(`{"tabid":"tab1234%d"}`, i), enc)
		if got := page.Header.Get("Content-Encoding"); got != strings.TrimPrefix(enc, "identity") {
			t.Errorf("render stream for %s in %q", enc, got)
		}
		tm := open(http.MethodGet, "/demo/telemetry", "", enc)
		if got := tm.Header.Get("Content-Encoding"); got != "" {
			t.Errorf("telemetry for %s in %q, want it uncompressed: its events are smaller than an encoder's memory", enc, got)
		}
		streams = append(streams,
			stream{"render stream in " + enc, bufio.NewReader(decoded(t, page)), sse.Reconnect(ui.StreamID)},
			stream{"telemetry for " + enc, bufio.NewReader(decoded(t, tm)), sse.Reconnect(ui.TelemetryID)})
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
		rest, err := io.ReadAll(st.body) // to the end of the compressed stream
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
		if res := open(r.method, r.path, r.body, "identity"); res.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("%s %s while stopping = %d, want 503", r.method, r.path, res.StatusCode)
		}
	}
}

// decoded is res's body through the decoder its Content-Encoding names. A
// read past the last event fails unless the stream ended cleanly.
func decoded(t *testing.T, res *http.Response) io.Reader {
	t.Helper()
	switch enc := res.Header.Get("Content-Encoding"); enc {
	case "":
		return res.Body
	case "gzip":
		d, err := gzip.NewReader(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		return d
	case "br":
		return brotli.NewReader(res.Body)
	case "zstd":
		d, err := zstd.NewReader(res.Body, zstd.WithDecoderConcurrency(1))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(d.Close)
		return d
	default:
		t.Fatalf("Content-Encoding %q", enc)
		return nil
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

// TestStreamRefusalsAre204: the server answers a stream request it refuses
// for good with 204, which retry 'error' does not repeat: a malformed one, a
// cross-site one, and one for a page that does not exist. Commands keep their
// 4xx.
func TestStreamRefusalsAre204(t *testing.T) {
	ts, c := newServer(t)
	get(t, c, ts.URL+"/") // a session
	for _, tc := range []struct {
		path, body, site string
		stream           bool
		want             int
	}{
		{"/", `{"tabid":"x"}`, "same-origin", true, http.StatusNoContent},
		{"/", `not json`, "same-origin", true, http.StatusNoContent},
		{"/", `{"tabid":"tab12345"}`, "cross-site", true, http.StatusNoContent},
		{"/", `{"tabid":"tab12345"}`, "cross-site", false, http.StatusForbidden},
		{"/no-such-page", `{"tabid":"tab12345"}`, "same-origin", true, http.StatusNoContent},
		{"/no-such-page", `{"tabid":"tab12345"}`, "same-origin", false, http.StatusNotFound},
	} {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+tc.path, strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Datastar-Request", "true")
		req.Header.Set("Sec-Fetch-Site", tc.site)
		req.Header.Set("Accept", "text/event-stream, text/html, application/json") // Datastar's
		if tc.stream {
			req.Header.Set("Accept", "text/event-stream") // streamInit's
		}
		res, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != tc.want {
			t.Errorf("POST %s %s (stream %v) = %d, want %d", tc.path, tc.body, tc.stream, res.StatusCode, tc.want)
		}
	}
}

// TestRenderStreamInBrotliWithoutZstd: a browser that takes brotli but not
// zstd (Safari) gets its render stream in brotli. With ZstdLowMemory, lith
// v0.1.0's budget sent it uncompressed.
func TestRenderStreamInBrotliWithoutZstd(t *testing.T) {
	s := startServer(t)
	get(t, s.c, s.ts.URL+"/")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, s.ts.URL+"/", strings.NewReader(`{"tabid":"abcdef1234"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	res, err := s.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if got := res.Header.Get("Content-Encoding"); got != "br" {
		t.Fatalf("render stream for gzip, deflate, br in %q, want br", got)
	}
	line, err := bufio.NewReader(decoded(t, res)).ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "event: ") {
		t.Errorf("first line %q, %v", line, err)
	}
}
