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
const mouse = (type, x, y, extra = {}) => cdp({ method: 'Input.dispatchMouseEvent', params: { type, x, y, button: type === 'mouseMoved' ? 'none' : 'left', ...extra } })
const center = (e) => { const r = rect(e); return [r.left + r.width / 2, r.top + r.height / 2] }
const keyCodes = { ArrowLeft: 37, ArrowRight: 39, ArrowUp: 38, ArrowDown: 40, Home: 36, End: 35, Enter: 13, Escape: 27, Tab: 9 }
// A real key press; modifiers: 1 Alt, 8 Shift.
const press = async (key, modifiers = 0) => {
	const p = { key, code: key, windowsVirtualKeyCode: keyCodes[key], modifiers }
	await cdp({ method: 'Input.dispatchKeyEvent', params: { type: 'rawKeyDown', ...p } }, { method: 'Input.dispatchKeyEvent', params: { type: 'keyUp', ...p } })
	await frame()
}
const shot = (name, el, pad = 8) => { const r = rect(el); return cdp({ method: 'shot', params: { name, clip: { x: r.left - pad, y: r.top - pad, width: r.width + 2 * pad, height: r.height + 2 * pad } } }) }
const scrollerOf = (el) => $(el, '.grid').shadowRoot.querySelector('.scroller')
const long = 'a value far too long for its column to show'

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

test('peek', async () => {
	const rows = [...Array(12).keys()].map((i) => ({ id: i, a: i === 1 ? 'short' : 'A ' + long, b: i === 1 ? 'short' : 'B ' + long, c: i === 1 ? 'short' : 'C ' + long }))
	const el = await make({ 'column-picker': '', columns: [{ key: 'a', label: 'A', width: '8rem' }, { key: 'b', label: 'B', width: '8rem' }, { key: 'c', label: 'C', width: '8rem' }], rows }, 'inline-size: 420px')
	const scroller = scrollerOf(el)
	const before = [scroller.scrollWidth, scroller.clientHeight]
	const cellAt = (r, j) => $$(el, '.row')[r].querySelectorAll('.cell')[j]
	const tracks = [0, 1, 2].map((j) => rect(cellAt(0, j)))
	const height = rect($$(el, '.row')[2]).height
	const sizes = () => [scroller.scrollWidth, scroller.clientHeight]

	const mid = cellAt(2, 1)
	await mouse('mouseMoved', ...center(mid))
	await frame()
	check('a hovered clipped cell peeks', [mid.hasAttribute('data-peek'), getComputedStyle(mid).overflow, getComputedStyle(mid).zIndex], [true, 'visible', '1'])
	check('it grows from its start', [round(rect(mid).left) === round(tracks[1].left), rect(mid).width > tracks[1].width + 50], [true, true])
	check('the row keeps its height', rect($$(el, '.row')[2]).height, height)
	check('the scroller keeps its scroll width and client height', sizes(), before)
	await shot('data-table-peek-middle', $(el, '.grid'))

	const short = cellAt(1, 1)
	await mouse('mouseMoved', ...center(short))
	await frame()
	check('an unclipped cell does not peek', [short.hasAttribute('data-peek'), round(rect(short).width)], [false, round(tracks[1].width)])
	check('the cell left behind stops peeking', [mid.hasAttribute('data-peek'), round(rect(mid).width)], [false, round(tracks[1].width)])

	const last = cellAt(2, 2)
	await mouse('mouseMoved', ...center(last))
	await frame()
	check('the last column peeks toward the start', [last.hasAttribute('data-peek'), Math.abs(rect(last).right - tracks[2].right) <= 1, rect(last).left < tracks[2].left - 50], [true, true, true])
	check('and the scroller still keeps its sizes', sizes(), before)
	await shot('data-table-peek-last', $(el, '.grid'))

	await mouse('mouseMoved', 1400, 990)
	await frame()
	check('the pointer gone: no peek', $$(el, '.cell[data-peek]').length, 0)

	// The keyboard: the focus moves from the first cell of row 3 to the next.
	cellAt(3, 0).focus()
	await frame()
	check('a focused clipped cell peeks', cellAt(3, 0).hasAttribute('data-peek'), true)
	await press('ArrowRight')
	const next = cellAt(3, 1)
	check('the arrow key moves the peek with the focus', [el.shadowRoot.activeElement === next, next.hasAttribute('data-peek'), cellAt(3, 0).hasAttribute('data-peek')], [true, true, false])
	check('the focused peek grows', rect(next).width > tracks[1].width + 50, true)
	check('focus keeps the sizes too', sizes(), before)

	// The sticky header paints over a peeking cell scrolled under it.
	scroller.scrollTop = 20
	await frame()
	const under = cellAt(0, 1)
	under.focus({ preventScroll: true })
	await frame()
	const head = rect($(el, '.head'))
	const hit = el.shadowRoot.elementFromPoint(Math.min(rect(under).right, head.right) - 20, head.bottom - 4)
	check('the header paints over it', [under.hasAttribute('data-peek'), rect(under).top < head.bottom, !!hit?.closest('.head')], [true, true, true])
	scroller.scrollTop = 0
	await frame()

	// The column menu (top layer) paints over a peek.
	const peeking = cellAt(2, 1)
	el.shadowRoot.activeElement?.blur()
	await mouse('mouseMoved', rect(peeking).left + 10, rect(peeking).top + 10)
	await frame()
	$(el, '.columns').click()
	await settle()
	const menu = rect($(el, '.menu')), p = rect(peeking)
	const x = (Math.max(menu.left, p.left) + Math.min(menu.right, p.right)) / 2, y = (Math.max(menu.top, p.top) + Math.min(menu.bottom, p.bottom)) / 2
	const overlap = Math.min(menu.right, p.right) > Math.max(menu.left, p.left) && Math.min(menu.bottom, p.bottom) > Math.max(menu.top, p.top)
	check('the column menu paints over it', [peeking.hasAttribute('data-peek'), overlap, !!el.shadowRoot.elementFromPoint(x, y)?.closest('.menu')], [true, true, true])
	$(el, '.menu').hidePopover()
	el.remove()
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
