package app_test

import "testing"

// TestRenderStreamInChrome: a command's outcome reaches the page through the
// tab's render stream, compressed as Chrome asks for it (zstd), and morphs in.
func TestRenderStreamInChrome(t *testing.T) {
	_, body := probe(t, "/showcase", renderStreamJS)
	if got := string(body); got != "70% 1 watching" {
		t.Fatalf("after the command the page shows %q, want the thrust and the viewer count", got)
	}
}

const renderStreamJS = `
const until = async (f, ms) => {
	for (const end = Date.now() + ms; Date.now() < end; await new Promise((r) => setTimeout(r, 50))) {
		const v = f()
		if (v) return v
	}
	return ''
}
const thrust = () => [...document.querySelectorAll('.flight__state dd')].map((d) => d.textContent).find((t) => t.endsWith('%'))
const slider = await until(() => document.querySelector('sb-slider[name="thrust"]'), 10000)
// Until Datastar has wired the page, the event goes nowhere: send it again.
let shown = ''
for (let i = 0; i < 10 && !shown; i++) {
	slider.dispatchEvent(new CustomEvent('sb-change', { detail: { name: 'thrust', value: 70 }, bubbles: true, composed: true }))
	shown = await until(() => thrust() === '70%' && thrust(), 1000)
}
const viewers = document.querySelector('.board-stats')?.textContent.trim().split(' · ')[0] ?? ''
await fetch('/__probe/result', { method: 'POST', body: (shown || thrust() || 'nothing') + ' ' + viewers })
`
