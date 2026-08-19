import hljsLightCss from 'highlight.js/styles/github.css?inline'
import hljsDarkCss from 'highlight.js/styles/github-dark.css?inline'
import katexCss from 'katex/dist/katex.min.css?inline'
import markdownBodyCss from './markdown-body.css?inline'

import { ReadFileBase64, ResolvePath } from '../wailsjs/go/main/App'
import { theme } from './theme'

// CSS variables the exported document needs, resolved from the live app so
// the values always match style.css for the current theme (no duplication).
const THEME_VARS = [
  '--bg',
  '--bg-soft',
  '--bg-muted',
  '--bg-code',
  '--bg-hover',
  '--text',
  '--text-muted',
  '--text-faint',
  '--border',
  '--border-strong',
  '--btn-bg',
  '--quote-border',
  '--error-bg',
  '--error-border',
  '--error-text',
  '--scrollbar',
]

function themeVarsCss(): string {
  const styles = getComputedStyle(document.documentElement)
  const lines = THEME_VARS.map((v) => `  ${v}: ${styles.getPropertyValue(v).trim()};`)
  return `:root {\n${lines.join('\n')}\n  color-scheme: ${theme.value};\n}`
}

/** Minimal page chrome for the standalone file; fonts follow the app. */
function pageCss(): string {
  const fontFamily = getComputedStyle(document.body).fontFamily
  return `body {\n  margin: 0;\n  padding: 24px 16px;\n  font-family: ${fontFamily};\n  background: var(--bg);\n}\n` +
    `.markdown-body {\n  max-width: 860px;\n  margin: 0 auto;\n}`
}

const LOCAL_PREFIX = '/__local__/'

// KaTeX fonts as lazy base64 data-URI modules: only imported when exporting,
// so they never touch the main chunk. (In the app itself the plain
// katex.min.css import is processed by Vite, which emits the fonts as
// static assets — no 404 there. Only the exported file needs inlining.)
const katexFontModules = import.meta.glob('/node_modules/katex/dist/fonts/*.woff2', {
  query: '?inline',
  import: 'default',
}) as Record<string, () => Promise<string>>

/** Replace url(fonts/xxx.woff2) in the KaTeX CSS with data URIs so the
 * exported file renders math with the proper fonts offline. woff/ttf
 * fallback declarations are left in place but never reached. */
async function inlineKatexFonts(css: string): Promise<string> {
  const names = [...new Set([...css.matchAll(/url\(fonts\/([^)\s]+\.woff2)\)/g)].map((m) => m[1]))]
  const pairs = await Promise.all(
    names.map(async (name) => {
      const load = katexFontModules[`/node_modules/katex/dist/fonts/${name}`]
      return [name, load ? await load() : null] as const
    }),
  )
  let out = css
  for (const [name, dataUri] of pairs) {
    if (dataUri) out = out.split(`url(fonts/${name})`).join(`url(${dataUri})`)
  }
  return out
}

const MIME_BY_EXT: Record<string, string> = {
  png: 'image/png',
  jpg: 'image/jpeg',
  jpeg: 'image/jpeg',
  gif: 'image/gif',
  svg: 'image/svg+xml',
  webp: 'image/webp',
  avif: 'image/avif',
  ico: 'image/x-icon',
  bmp: 'image/bmp',
}

/**
 * Replace /__local__/ image sources with base64 data URLs so the exported
 * file is self-contained. Paths are resolved against the current document's
 * directory via the Go bindings; missing/unreadable files keep their
 * original src and never abort the export.
 */
async function inlineLocalImages(container: HTMLElement, currentFile: string): Promise<void> {
  if (!currentFile) return
  const imgs = Array.from(container.querySelectorAll('img'))
  await Promise.all(
    imgs.map(async (img) => {
      const src = img.getAttribute('src') ?? ''
      if (!src.startsWith(LOCAL_PREFIX)) return
      try {
        const abs = await ResolvePath(currentFile, src.slice(LOCAL_PREFIX.length))
        const b64 = await ReadFileBase64(abs)
        const ext = abs.split('.').pop()?.toLowerCase() ?? ''
        const mime = MIME_BY_EXT[ext] ?? 'application/octet-stream'
        img.setAttribute('src', `data:${mime};base64,${b64}`)
      } catch {
        // leave the original src
      }
    }),
  )
}

function escapeHtml(s: string): string {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;')
}

/**
 * Build a self-contained HTML document from the rendered preview DOM.
 * KaTeX output is already inline HTML (its web fonts are embedded as data
 * URIs by inlineKatexFonts), mermaid blocks are inline SVG, and local
 * images are inlined as data URLs, so the result needs no assets.
 */
export async function buildExportHtml(
  preview: HTMLElement,
  title: string,
  currentFile: string,
): Promise<string> {
  const container = preview.cloneNode(true) as HTMLElement
  await inlineLocalImages(container, currentFile)
  const css = [
    themeVarsCss(),
    pageCss(),
    markdownBodyCss,
    await inlineKatexFonts(katexCss),
    theme.value === 'dark' ? hljsDarkCss : hljsLightCss,
  ].join('\n')
  return `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>${escapeHtml(title)}</title>
<style>
${css}
</style>
</head>
<body>
<div class="markdown-body">
${container.innerHTML}
</div>
</body>
</html>
`
}
