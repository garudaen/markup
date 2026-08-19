import MarkdownIt from 'markdown-it'
import hljs from 'highlight.js'
import markdownItKatex from '@traptitech/markdown-it-katex'

import 'katex/dist/katex.min.css'
import hljsLightCss from 'highlight.js/styles/github.css?inline'
import hljsDarkCss from 'highlight.js/styles/github-dark.css?inline'

import { theme, type Theme } from './theme'

// --- highlight.js stylesheets, toggled per theme ---

const hljsStyles: Record<Theme, HTMLStyleElement> = {
  light: injectStyle(hljsLightCss),
  dark: injectStyle(hljsDarkCss),
}

function injectStyle(css: string): HTMLStyleElement {
  const el = document.createElement('style')
  el.textContent = css
  document.head.appendChild(el)
  return el
}

export function setHljsTheme(t: Theme): void {
  hljsStyles.light.disabled = t !== 'light'
  hljsStyles.dark.disabled = t !== 'dark'
}

// --- mermaid (lazy-loaded on first diagram to keep it out of the main chunk) ---

type MermaidApi = (typeof import('mermaid'))['default']

function mermaidTheme(t: Theme): 'neutral' | 'dark' {
  return t === 'dark' ? 'dark' : 'neutral'
}

let currentMermaidTheme: 'neutral' | 'dark' = mermaidTheme(theme.value)
let mermaidPromise: Promise<MermaidApi> | undefined

function loadMermaid(): Promise<MermaidApi> {
  if (!mermaidPromise) {
    mermaidPromise = import('mermaid').then((m) => {
      m.default.initialize({
        startOnLoad: false,
        theme: currentMermaidTheme,
        securityLevel: 'strict',
      })
      return m.default
    })
  }
  return mermaidPromise
}

/** Re-initialize mermaid with the theme-matching config (no-op until the
 * first diagram triggers the lazy load). */
export function setMermaidTheme(t: Theme): void {
  currentMermaidTheme = mermaidTheme(t)
  mermaidPromise?.then((m) =>
    m.initialize({ startOnLoad: false, theme: currentMermaidTheme, securityLevel: 'strict' }),
  )
}

setHljsTheme(theme.value)

// markdown-it v15's bundled types export the class as a value only;
// derive the instance type from the constructor.
type Md = InstanceType<typeof MarkdownIt>

export function createRenderer(): Md {
  const md = new MarkdownIt({
    // Content is the local user's own documents, so raw HTML is allowed.
    html: true,
    linkify: true,
    highlight(code: string, lang: string): string {
      if (lang && hljs.getLanguage(lang)) {
        try {
          return hljs.highlight(code, { language: lang }).value
        } catch {
          // fall through to unhighlighted output
        }
      }
      return ''
    },
  })

  md.use(markdownItKatex, {})

  // [TOC]: a paragraph consisting solely of "[TOC]" (case-insensitive,
  // surrounding whitespace allowed) becomes a nested list of the document's
  // h1-h3 headings. Inline occurrences and code blocks are left untouched
  // since only standalone paragraph tokens are matched.
  md.core.ruler.push('toc', (state) => {
    const headings: { level: number; text: string; index: number }[] = []
    for (let i = 0; i < state.tokens.length; i++) {
      const token = state.tokens[i]
      if (token.type !== 'heading_open') continue
      const level = Number(token.tag.slice(1))
      if (level < 1 || level > 3) continue
      headings.push({ level, text: state.tokens[i + 1]?.content ?? '', index: headings.length })
    }
    if (!headings.length) return

    const out = []
    for (let i = 0; i < state.tokens.length; i++) {
      const token = state.tokens[i]
      if (
        token.type === 'paragraph_open' &&
        state.tokens[i + 1]?.type === 'inline' &&
        /^\s*\[TOC\]\s*$/i.test(state.tokens[i + 1].content) &&
        state.tokens[i + 2]?.type === 'paragraph_close'
      ) {
        const toc = new state.Token('toc', '', 0)
        toc.meta = { headings }
        out.push(toc)
        i += 2
      } else {
        out.push(token)
      }
    }
    state.tokens = out
  })

  md.renderer.rules.toc = (tokens, idx) => {
    const { headings } = tokens[idx].meta as {
      headings: { level: number; text: string; index: number }[]
    }
    const min = Math.min(...headings.map((h) => h.level))
    const depth = (h: (typeof headings)[number]) => h.level - min + 1

    let html = '<nav class="toc-block"><ul>'
    headings.forEach((h, i) => {
      if (i > 0) {
        const d = depth(h) - depth(headings[i - 1])
        if (d > 0) html += '<ul>'.repeat(d)
        else html += '</li></ul>'.repeat(-d) + '</li>'
      }
      html += `<li><a href="#" data-toc-index="${h.index}">${md.utils.escapeHtml(h.text)}</a>`
    })
    html += '</li></ul>'.repeat(depth(headings[headings.length - 1]))
    return html + '</nav>\n'
  }

  // Relative image sources are served by the asset middleware from the
  // current document's directory under the /__local__/ prefix (see
  // localFileMiddleware in app.go). Absolute/scheme/data sources and
  // already-rewritten paths are left untouched. markdown-it image srcs are
  // already URL-normalized; srcs inside raw HTML are prefixed as-is and the
  // browser percent-encodes any raw non-ASCII/space characters.
  const rewriteLocalSrc = (src: string): string => {
    if (/^(\/|#|__local__\/)/.test(src)) return src
    if (/^[a-z][a-z0-9+.-]*:/i.test(src)) return src // http:, https:, data:, …
    return '/__local__/' + src
  }

  const rewriteHtmlImgSrc = (html: string): string =>
    html.replace(
      /(<img\b[^>]*?\bsrc\s*=\s*)(["'])(.*?)\2/gis,
      (_m, pre: string, quote: string, src: string) => pre + quote + rewriteLocalSrc(src.trim()) + quote,
    )

  const defaultImage =
    md.renderer.rules.image ??
    ((tokens, idx, options, _env, self) => self.renderToken(tokens, idx, options))
  md.renderer.rules.image = (tokens, idx, options, env, self) => {
    const token = tokens[idx]
    const srcIdx = token.attrIndex('src')
    if (srcIdx >= 0 && token.attrs) token.attrs[srcIdx][1] = rewriteLocalSrc(String(token.attrs[srcIdx][1]))
    return defaultImage(tokens, idx, options, env, self)
  }

  // Raw HTML blocks/inline tags are emitted verbatim; rewrite <img src> in them.
  md.core.ruler.push('local_img', (state) => {
    for (const token of state.tokens) {
      if (token.type === 'html_block') token.content = rewriteHtmlImgSrc(token.content)
      if (token.type !== 'inline' || !token.children) continue
      for (const child of token.children) {
        if (child.type === 'html_inline') child.content = rewriteHtmlImgSrc(child.content)
      }
    }
  })

  // Tag block-level elements with their source line (from the token map) so
  // the editor scroll position can be mapped into the preview. Opening
  // tokens render attributes via renderToken; fence/code_block emit
  // renderAttrs in the default rules. html_block is content-based and the
  // custom toc token has no attributes — both are skipped on purpose.
  md.core.ruler.push('source_line', (state) => {
    for (const token of state.tokens) {
      if (!token.map) continue
      if (token.nesting === 1 || token.type === 'fence' || token.type === 'code_block' || token.type === 'hr') {
        token.attrSet('data-source-line', String(token.map[0]))
      }
    }
  })

  // Render ```mermaid fences as placeholders; they are turned into SVG
  // after the HTML is mounted (see renderMermaidBlocks).
  const defaultFence = md.renderer.rules.fence!.bind(md.renderer)
  md.renderer.rules.fence = (tokens, idx, options, env, self) => {
    const token = tokens[idx]
    if (token.info.trim() === 'mermaid') {
      // Keep the source line on the placeholder; renderMermaidBlocks only
      // replaces its innerHTML, so the attribute survives SVG rendering.
      const line = token.map ? ` data-source-line="${token.map[0]}"` : ''
      return `<div class="mermaid-block"${line}>${md.utils.escapeHtml(token.content)}</div>\n`
    }
    return defaultFence(tokens, idx, options, env, self)
  }

  return md
}

export interface OutlineItem {
  level: number
  text: string
  /** Index among all h1-h3 headings in document order. */
  index: number
}

export function extractOutline(md: Md, src: string): OutlineItem[] {
  const tokens = md.parse(src, {})
  const items: OutlineItem[] = []
  for (let i = 0; i < tokens.length; i++) {
    const token = tokens[i]
    if (token.type !== 'heading_open') continue
    const level = Number(token.tag.slice(1))
    if (level < 1 || level > 3) continue
    const inline = tokens[i + 1]
    items.push({ level, text: inline?.content ?? '', index: items.length })
  }
  return items
}

let mermaidSeq = 0

/** Replace .mermaid-block placeholders inside container with rendered SVG. */
export async function renderMermaidBlocks(container: HTMLElement): Promise<void> {
  const blocks = Array.from(container.querySelectorAll<HTMLElement>('.mermaid-block'))
  if (!blocks.length) return
  const mermaid = await loadMermaid()
  // Re-apply the current theme right before rendering so every batch uses
  // it, regardless of when the lazy load or a theme switch happened.
  mermaid.initialize({ startOnLoad: false, theme: currentMermaidTheme, securityLevel: 'strict' })
  for (const el of blocks) {
    const code = el.textContent ?? ''
    try {
      const { svg } = await mermaid.render(`markup-mmd-${++mermaidSeq}`, code)
      el.innerHTML = svg
    } catch (err) {
      // mermaid may leave an orphan error element in <body>; remove it
      document.getElementById(`dmarkup-mmd-${mermaidSeq}`)?.remove()
      el.innerHTML = ''
      const pre = document.createElement('pre')
      pre.textContent = code
      const msg = document.createElement('div')
      msg.className = 'mermaid-error'
      msg.textContent = `Mermaid 渲染失败：${err instanceof Error ? err.message : String(err)}`
      el.append(pre, msg)
    }
  }
}

/** Scroll the container to the n-th h1-h3 heading in the rendered preview. */
export function scrollToHeading(container: HTMLElement, index: number): void {
  const headings = container.querySelectorAll('h1, h2, h3')
  headings[index]?.scrollIntoView({ behavior: 'smooth', block: 'start' })
}

/**
 * Scroll the preview to the element covering the given 0-based source line:
 * the element tagged with the greatest data-source-line <= line, with the
 * offset interpolated towards the next tagged element. Tagged elements are
 * in document order, which matches source order, so the scan can stop early.
 */
export function scrollPreviewToLine(container: HTMLElement, line: number): void {
  const tagged = Array.from(container.querySelectorAll<HTMLElement>('[data-source-line]'))
  let target: HTMLElement | null = null
  let targetLine = 0
  let next: HTMLElement | null = null
  let nextLine = 0
  for (const el of tagged) {
    const l = Number(el.dataset.sourceLine)
    if (l <= line) {
      if (!target || l >= targetLine) {
        target = el
        targetLine = l
      }
    } else {
      next = el
      nextLine = l
      break
    }
  }
  if (!target) return

  const containerRect = container.getBoundingClientRect()
  const offsetTop = (el: HTMLElement) => el.getBoundingClientRect().top - containerRect.top + container.scrollTop
  let top = offsetTop(target)
  if (next && nextLine > targetLine) {
    const frac = Math.min(1, (line - targetLine) / (nextLine - targetLine))
    top += frac * (offsetTop(next) - top)
  }
  container.scrollTo({ top: Math.max(0, top), behavior: 'auto' })
}
