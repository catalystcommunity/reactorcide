import { h, s, setAttr, setText, show, syncKeyed, uid } from '~/ui/dom.ts'

/**
 * Small inline SVG charts for the Insights page. No chart library: the SPA
 * has no application framework.
 *
 * Rules (dataviz guidance):
 * - One y axis. Two measures of a different unit go in two charts.
 * - At most four series, in a fixed color order (`--insight-1` .. `-4`,
 *   validated for CVD and contrast in light and dark mode, insights.css). A
 *   fifth series is folded into "Other" by the caller.
 * - Identity is never color alone: with two or more series a legend is shown,
 *   and every series also has a dash pattern and a marker shape.
 * - Each point has a hit area with a tooltip (`<title>`) and is focusable.
 * - Each chart has a table view with the same numbers.
 *
 * A chart element is made one time; `update()` changes its content.
 */

export interface ChartPoint {
  /** The bucket start, RFC 3339. */
  x: string
  y: number | undefined
}

export interface ChartSeries {
  key: string
  label: string
  points: ChartPoint[]
}

export const MAX_SERIES = 4

const WIDTH = 640
const HEIGHT = 200
const PAD = { top: 12, right: 16, bottom: 26, left: 56 }

export function formatPercent(v: number | undefined, digits = 1): string {
  if (v === undefined || !Number.isFinite(v)) return '—'
  return `${(v * 100).toFixed(digits)}%`
}

export function formatDuration(v: number | undefined): string {
  if (v === undefined || !Number.isFinite(v)) return '—'
  if (v < 90) return `${Math.round(v)} s`
  if (v < 5400) return `${(v / 60).toFixed(1)} min`
  return `${(v / 3600).toFixed(1)} h`
}

export function formatNumber(v: number | undefined, digits = 1): string {
  if (v === undefined || !Number.isFinite(v)) return '—'
  if (Math.abs(v) >= 1000) return Math.round(v).toLocaleString('en-US')
  return Number.isInteger(v) ? String(v) : v.toFixed(digits)
}

export type ChartUnit = 'percent' | 'seconds' | 'number'

export function formatUnit(v: number | undefined, unit: ChartUnit): string {
  if (unit === 'percent') return formatPercent(v)
  if (unit === 'seconds') return formatDuration(v)
  return formatNumber(v)
}

function dayLabel(x: string): string {
  const d = new Date(x)
  return Number.isNaN(d.getTime()) ? x : d.toLocaleDateString('en-US', { month: 'short', day: 'numeric', timeZone: 'UTC' })
}

/** A tick top and steps for 0..max. */
export function axisTicks(max: number, unit: ChartUnit): number[] {
  if (unit === 'percent') return [0, 0.25, 0.5, 0.75, 1]
  if (!(max > 0)) return [0, 1]
  const raw = max / 4
  const magnitude = 10 ** Math.floor(Math.log10(raw))
  const step = ([1, 2, 2.5, 5, 10].find((f) => f * magnitude >= raw) ?? 10) * magnitude
  const ticks: number[] = []
  for (let v = 0; v <= max + step / 2 && ticks.length < 8; v += step) ticks.push(v)
  if (ticks[ticks.length - 1] < max) ticks.push(ticks[ticks.length - 1] + step)
  return ticks
}

const MARKERS = ['circle', 'square', 'diamond', 'triangle'] as const

function marker(kind: (typeof MARKERS)[number], cx: number, cy: number, cls: string): SVGElement {
  const r = 4.5
  switch (kind) {
    case 'square':
      return s('rect', { class: cls, x: cx - r, y: cy - r, width: r * 2, height: r * 2, rx: 1 })
    case 'diamond':
      return s('path', { class: cls, d: `M ${cx} ${cy - r - 1} L ${cx + r + 1} ${cy} L ${cx} ${cy + r + 1} L ${cx - r - 1} ${cy} Z` })
    case 'triangle':
      return s('path', { class: cls, d: `M ${cx} ${cy - r - 1} L ${cx + r + 1} ${cy + r} L ${cx - r - 1} ${cy + r} Z` })
  }
  return s('circle', { class: cls, cx, cy, r })
}

/** A line chart over daily buckets, with a legend and a table view. */
export class LineChart {
  readonly el: HTMLElement
  readonly #svg: SVGSVGElement
  readonly #titleId = uid('chart-title')
  readonly #desc: SVGElement
  readonly #legend: HTMLUListElement
  readonly #empty: HTMLElement
  readonly #tableHead: HTMLTableRowElement
  readonly #tableBody: HTMLTableSectionElement
  readonly #unit: ChartUnit
  readonly #caption: string

  constructor(options: { title: string; description: string; unit: ChartUnit }) {
    this.#unit = options.unit
    this.#caption = options.title
    this.#desc = s('desc')
    this.#svg = s('svg', {
      class: 'insight-chart',
      viewBox: `0 0 ${WIDTH} ${HEIGHT}`,
      role: 'img',
      'aria-labelledby': this.#titleId,
    }) as SVGSVGElement
    this.#legend = h('ul', { class: 'insight-legend', 'aria-label': 'Legend' })
    this.#empty = h('div', { class: 'insight-empty meta' }, 'No runs in this range.')
    this.#tableHead = h('tr')
    this.#tableBody = h('tbody')
    this.el = h(
      'figure',
      { class: 'insight-figure' },
      h('figcaption', null, h('span', { class: 'insight-chart-title', id: this.#titleId }, options.title), h('span', { class: 'meta' }, options.description)),
      this.#legend,
      h('div', { class: 'insight-chart-wrap' }, this.#svg),
      this.#empty,
      h(
        'details',
        { class: 'insight-table' },
        h('summary', null, 'Show as table'),
        h('div', { class: 'scroll-x' }, h('table', { class: 'table' }, h('thead', null, this.#tableHead), this.#tableBody)),
      ),
    )
  }

  update(input: ChartSeries[]): void {
    const series = input.slice(0, MAX_SERIES)
    const xs = series[0]?.points.map((p) => p.x) ?? []
    const values = series.flatMap((one) => one.points.map((p) => p.y)).filter((v): v is number => v !== undefined && Number.isFinite(v))
    const hasData = values.length > 0
    show(this.#empty, !hasData)
    show(this.#svg, hasData)
    setText(this.#desc, `${this.#caption}. ${series.map((one) => one.label).join(', ')}.`)

    // Legend: only for two or more series; a single series is named by the title.
    show(this.#legend, series.length > 1)
    syncKeyed(
      this.#legend,
      series,
      (one) => one.key,
      () => h('li', null, h('span', { class: 'insight-swatch', 'aria-hidden': 'true' }), h('span', { class: 'insight-legend-label' })),
      (el, one, i) => {
        el.className = `insight-legend-item insight-s${i + 1}`
        setText(el.querySelector('.insight-legend-label')!, one.label)
      },
    )

    // Table view.
    this.#tableHead.replaceChildren(h('th', null, 'Day'), ...series.map((one) => h('th', { class: 'numeric' }, one.label)))
    syncKeyed(
      this.#tableBody,
      xs,
      (x) => x,
      () => h('tr'),
      (row, x, i) => {
        row.replaceChildren(h('td', null, dayLabel(x)), ...series.map((one) => h('td', { class: 'numeric' }, formatUnit(one.points[i]?.y, this.#unit))))
      },
    )

    if (!hasData) {
      this.#svg.replaceChildren(this.#desc)
      return
    }
    const ticks = axisTicks(Math.max(...values), this.#unit)
    const top = ticks[ticks.length - 1] || 1
    const plotW = WIDTH - PAD.left - PAD.right
    const plotH = HEIGHT - PAD.top - PAD.bottom
    const x = (i: number) => PAD.left + (xs.length <= 1 ? plotW / 2 : (i / (xs.length - 1)) * plotW)
    const y = (v: number) => PAD.top + plotH - (v / top) * plotH
    const parts: SVGElement[] = [this.#desc]
    for (const tick of ticks) {
      parts.push(s('line', { class: 'insight-grid', x1: PAD.left, y1: y(tick), x2: WIDTH - PAD.right, y2: y(tick) }))
      parts.push(s('text', { class: 'insight-axis', x: PAD.left - 8, y: y(tick) + 4, 'text-anchor': 'end' }, formatUnit(tick, this.#unit)))
    }
    const labelAt = xs.length > 1 ? [0, Math.floor((xs.length - 1) / 2), xs.length - 1] : [0]
    for (const i of new Set(labelAt)) {
      const anchor = xs.length <= 1 ? 'middle' : i === 0 ? 'start' : i === xs.length - 1 ? 'end' : 'middle'
      parts.push(s('text', { class: 'insight-axis', x: x(i), y: HEIGHT - 8, 'text-anchor': anchor }, dayLabel(xs[i])))
    }
    series.forEach((one, si) => {
      const cls = `insight-s${si + 1}`
      // A gap in the data is a gap in the line.
      let d = ''
      let open = false
      one.points.forEach((p, i) => {
        if (p.y === undefined || !Number.isFinite(p.y)) {
          open = false
          return
        }
        d += `${open ? 'L' : 'M'} ${x(i).toFixed(1)} ${y(p.y).toFixed(1)} `
        open = true
      })
      const g = s('g', { class: `insight-series ${cls}` })
      g.append(s('path', { class: 'insight-line', d: d.trim() }))
      one.points.forEach((p, i) => {
        if (p.y === undefined || !Number.isFinite(p.y)) return
        const text = `${one.label}, ${dayLabel(p.x)}: ${formatUnit(p.y, this.#unit)}`
        const point = s('g', { class: 'insight-point', tabindex: '0', role: 'img', 'aria-label': text })
        point.append(
          s('title', null, text),
          s('circle', { class: 'insight-hit', cx: x(i), cy: y(p.y), r: 12 }),
          marker(MARKERS[si % MARKERS.length], x(i), y(p.y), 'insight-marker'),
        )
        g.append(point)
      })
      parts.push(g)
    })
    this.#svg.replaceChildren(...parts)
  }
}

/** A sparkline for a table cell. It is decoration: the cell text carries the value. */
export function sparkline(): SVGSVGElement {
  return s('svg', { class: 'insight-spark', viewBox: '0 0 80 20', 'aria-hidden': 'true', focusable: 'false' }) as SVGSVGElement
}

export function updateSparkline(svg: SVGSVGElement, values: (number | undefined)[], max?: number): void {
  const finite = values.filter((v): v is number => v !== undefined && Number.isFinite(v))
  if (finite.length === 0) {
    svg.replaceChildren()
    return
  }
  const top = max ?? (Math.max(...finite) || 1)
  let d = ''
  let open = false
  values.forEach((v, i) => {
    if (v === undefined || !Number.isFinite(v)) {
      open = false
      return
    }
    const px = values.length <= 1 ? 40 : 2 + (i / (values.length - 1)) * 76
    const py = 18 - (v / top) * 16
    d += `${open ? 'L' : 'M'} ${px.toFixed(1)} ${py.toFixed(1)} `
    open = true
  })
  svg.replaceChildren(s('path', { class: 'insight-spark-line', d: d.trim() }))
}

/** A horizontal bar in a table cell: the share of the largest value. */
export function barCell(): HTMLElement {
  return h('span', { class: 'insight-bar', 'aria-hidden': 'true' }, h('span', { class: 'insight-bar-fill' }))
}

export function updateBar(el: HTMLElement, value: number, max: number): void {
  const fill = el.firstElementChild as HTMLElement
  const pct = max > 0 ? Math.max(0, Math.min(100, (value / max) * 100)) : 0
  setAttr(fill, 'style', `width: ${pct.toFixed(1)}%`)
}
