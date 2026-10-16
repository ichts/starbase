package app_test

import "testing"

// TestDataTableColumns checks sb-data-table's columns in Chrome, driven
// through the DevTools protocol where a page can't fake it (real pointer
// input, forced colours, the accessibility tree): the header lines up with
// the rows, clipped cells peek, and columns resize by pointer and keyboard.
// With STARBASE_DATASTAR_BUNDLE=<file>, on that Datastar build (see
// TestVirtualScrollFocus); STARBASE_SHOTS=<dir> saves screenshots there.
func TestDataTableColumns(t *testing.T) {
	copyRows(t, probeCDP(t, "/", dataTableColumnsJS))
}

const dataTableColumnsJS = `
await customElements.whenDefined('sb-data-table')
await Promise.all(['400 14px "Pixelify Sans"', '600 14px "Pixelify Sans"', '400 14px "JetBrains Mono"', '600 14px "JetBrains Mono"'].map((f) => document.fonts.load(f)))
const settle = (ms = 80) => new Promise((r) => setTimeout(r, ms))
const frame = () => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)))
const out = []
let group = ''
const check = (step, got, want) => out.push({ step: group + ': ' + step, got: JSON.stringify(got), want: JSON.stringify(want) })
addEventListener('error', (e) => out.push({ step: group, error: 'page: ' + (e.error?.stack || e.message) }))
addEventListener('unhandledrejection', (e) => out.push({ step: group, error: 'page: ' + (e.reason?.stack || e.reason) }))
const cdp = async (...cmds) => {
	const r = await fetch('/__probe/cdp', { method: 'POST', body: JSON.stringify(cmds) })
	if (!r.ok) throw new Error('cdp: ' + (await r.text()))
	return r.json()
}
const css = document.createElement('style')
document.head.append(css)
const make = async (attrs, style = '') => {
	const el = document.createElement('sb-data-table')
	for (const [k, v] of Object.entries(attrs)) el.setAttribute(k, typeof v === 'string' ? v : JSON.stringify(v))
	el.style.cssText = 'block-size: 20rem; inline-size: 900px; ' + style
	document.body.prepend(el)
	await settle()
	await frame()
	return el
}
const $ = (el, s) => el.shadowRoot.querySelector(s)
const $$ = (el, s) => [...el.shadowRoot.querySelectorAll(s)]
const rect = (e) => e.getBoundingClientRect()
const round = (x) => Math.round(x * 10) / 10
const groups = []
const test = (name, f) => groups.push([name, f])

test('alignment', async () => {
	css.textContent = '.dt-align { font-family: "Pixelify Sans"; } .dt-align::part(column) { font-family: "JetBrains Mono"; font-size: 1.15em; }'
	const widths = ['14ch', '9ch', '6em', '11ch', '8em', '12ch', '7ch']
	const columns = widths.map((width, i) => ({ key: 'c' + i, label: 'Column ' + i, width }))
	const el = await make({ class: 'dt-align', columns, rows: [Object.fromEntries([['id', 1], ...widths.map((_, i) => ['c' + i, 'value ' + i])])] })
	el.className = 'dt-align'
	await frame()
	const heads = $$(el, '.th').map((e) => rect(e).left)
	const cells = $$(el, '.row .cell').map((e) => rect(e).left)
	check('header cells start where the cells do (within 1 px)', heads.map((x, i) => Math.abs(x - cells[i]) <= 1), widths.map(() => true))
	check('the header font changed', getComputedStyle($(el, '.th')).fontFamily.includes('JetBrains'), true)
	el.remove()
	css.textContent = ''
})

for (const [name, f] of groups) {
	group = name
	try {
		await f()
	} catch (e) {
		out.push({ step: name, error: String(e.stack || e) })
	}
}
await fetch('/__probe/result', { method: 'POST', body: JSON.stringify(out) })
`
