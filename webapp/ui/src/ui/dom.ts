/**
 * Plain DOM helpers. `h` makes one element. It keeps no state and binds
 * nothing: a component makes its elements once and changes them itself.
 *
 * THE RENDERING RULE, which the whole UI depends on: never rebuild a control
 * from data. A browser closes an open dropdown, drops a half-typed value and
 * resets a scroll position the moment the element is replaced. Build once, then
 * change text, attributes and properties in place. Use `syncKeyed` for a list,
 * so a row that stays keeps its element.
 */

export type Child = Node | string | number | null | undefined | false | Child[]

/**
 * Attributes of `h`:
 * - `onClick: fn` adds a listener ("click"). `onKeyDown` is "keydown".
 * - `.value: x` sets a property, not an attribute.
 * - `style: {color: "red", "--sw": c}` sets style properties.
 * - `class` and `className` set the class attribute.
 * - `true` sets an empty attribute; `false`, `null` and `undefined` set nothing.
 */
export type Attrs = Record<string, unknown>

const SVG_NS = 'http://www.w3.org/2000/svg'
const SVG_TAGS = new Set([
  'svg',
  'path',
  'circle',
  'rect',
  'g',
  'line',
  'polyline',
  'polygon',
  'ellipse',
  'text',
  'tspan',
  'defs',
  'linearGradient',
  'stop',
  'marker',
  'title',
])

export function h<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  attrs?: Attrs | null,
  ...children: Child[]
): HTMLElementTagNameMap[K]
export function h(tag: string, attrs?: Attrs | null, ...children: Child[]): HTMLElement
export function h(tag: string, attrs?: Attrs | null, ...children: Child[]): Element {
  const el = SVG_TAGS.has(tag) ? document.createElementNS(SVG_NS, tag) : document.createElement(tag)
  if (attrs) setAttrs(el, attrs)
  el.append(...nodes(children))
  return el
}

/** An SVG element. `title` is both an HTML and an SVG tag, so SVG callers use this. */
export function s(tag: string, attrs?: Attrs | null, ...children: Child[]): SVGElement {
  const el = document.createElementNS(SVG_NS, tag)
  if (attrs) setAttrs(el, attrs)
  el.append(...nodes(children))
  return el
}

export function setAttrs(el: Element, attrs: Attrs): void {
  for (const [name, value] of Object.entries(attrs)) {
    if (name.startsWith('.')) {
      ;(el as unknown as Record<string, unknown>)[name.slice(1)] = value
    } else if (/^on[A-Z]/.test(name)) {
      if (typeof value === 'function') el.addEventListener(name.slice(2).toLowerCase(), value as EventListener)
    } else if (name === 'style' && value && typeof value === 'object') {
      const style = (el as HTMLElement).style
      for (const [k, v] of Object.entries(value)) {
        const prop = k.startsWith('--') ? k : k.replace(/[A-Z]/g, (c) => `-${c.toLowerCase()}`)
        if (v === null || v === undefined || v === false) style.removeProperty(prop)
        else style.setProperty(prop, String(v))
      }
    } else {
      setAttr(el, name === 'className' ? 'class' : name, value)
    }
  }
}

/** Set, clear (false, null, undefined), or set empty (true). Only touches the DOM on a change. */
export function setAttr(el: Element, name: string, value: unknown): void {
  if (value === false || value === null || value === undefined) {
    if (el.hasAttribute(name)) el.removeAttribute(name)
    return
  }
  const text = value === true ? '' : String(value)
  if (el.getAttribute(name) !== text) el.setAttribute(name, text)
}

export function nodes(children: Child[]): Node[] {
  const out: Node[] = []
  const add = (c: Child): void => {
    if (Array.isArray(c)) c.forEach(add)
    else if (c === null || c === undefined || c === false) return
    else out.push(c instanceof Node ? c : document.createTextNode(String(c)))
  }
  children.forEach(add)
  return out
}

/** Replace the children of `el`. For static content only; never for controls. */
export function fill(el: Element, ...children: Child[]): void {
  el.replaceChildren(...nodes(children))
}

/** Show or hide an element. */
export function show(el: HTMLElement | SVGElement, visible: unknown): void {
  const hidden = !visible
  if (el instanceof HTMLElement) {
    if (el.hidden !== hidden) el.hidden = hidden
  } else {
    setAttr(el, 'display', hidden ? 'none' : null)
  }
}

/** Set text only when it changes, so a screen reader does not read it again. */
export function setText(el: Node, text: string): void {
  if (el.textContent !== text) el.textContent = text
}

/** Toggle a class only when the state changes. */
export function setClass(el: Element, name: string, on: boolean): void {
  if (el.classList.contains(name) !== on) el.classList.toggle(name, on)
}

/**
 * Keep the children of `parent` in the order of `items`, one element for each
 * key. An element for a key that stays is moved, not made again, so focus and
 * input state stay. `update` runs for each item, including on creation.
 *
 * `parent` must hold only keyed children (a `tbody`, a `ul`, a wrapper div).
 */
export function syncKeyed<T, E extends Element = HTMLElement>(
  parent: Element,
  items: readonly T[],
  key: (item: T) => string,
  create: (item: T) => E,
  update?: (el: E, item: T, index: number) => void,
): void {
  const old = new Map<string, E>()
  for (const child of [...parent.children]) {
    const k = (child as HTMLElement).dataset?.key
    if (k !== undefined) old.set(k, child as E)
  }
  let cursor: ChildNode | null = parent.firstChild
  items.forEach((item, i) => {
    const k = key(item)
    let el = old.get(k)
    if (el) old.delete(k)
    else {
      el = create(item)
      ;(el as unknown as HTMLElement).dataset.key = k
    }
    update?.(el, item, i)
    if (cursor === el) cursor = el.nextSibling
    else parent.insertBefore(el, cursor)
  })
  for (const el of old.values()) el.remove()
}

let idCount = 0
/** A unique element id. */
export function uid(prefix = 'rc'): string {
  idCount += 1
  return `${prefix}-${idCount}`
}
