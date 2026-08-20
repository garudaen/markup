<script lang="ts" setup>
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Compartment, EditorState, Prec } from '@codemirror/state'
import { EditorView, keymap } from '@codemirror/view'
import { defaultKeymap, history, historyKeymap, indentWithTab } from '@codemirror/commands'
import { markdown } from '@codemirror/lang-markdown'
import { defaultHighlightStyle, syntaxHighlighting } from '@codemirror/language'
import { search, searchKeymap } from '@codemirror/search'
import { oneDark } from '@codemirror/theme-one-dark'
import { CreateDir, CreateFile, DeletePath, CheckExternalChange, ExportHTML, ExportPDF, OpenFile, OpenFolder, ReadFile, RefreshFolder, RenamePath, ResolvePath, SaveFile, SaveImage, SaveImageFile, SaveToPath, SaveToPathForce, SetCurrentFile, SetLanguage } from '../wailsjs/go/main/App'
import { BrowserOpenURL, OnFileDrop, OnFileDropOff, WindowGetPosition, WindowGetSize, WindowSetPosition, WindowSetSize } from '../wailsjs/runtime'
import { buildExportHtml } from './exportHtml'
import { locale, t } from './i18n'
import { main } from '../wailsjs/go/models'
import {
  createRenderer,
  extractOutline,
  renderMermaidBlocks,
  scrollPreviewToLine,
  scrollToHeading,
  setHljsTheme,
  setMermaidTheme,
  type OutlineItem,
} from './markdown'
import { applyTheme, theme, toggleTheme, type Theme } from './theme'
// Preview typography lives in its own stylesheet so the exported HTML can
// inline the exact same CSS (see exportHtml.ts).
import './markdown-body.css'

const INITIAL_DOC = `# markup

极简 Markdown 编辑器。支持 **代码高亮**、数学公式与 Mermaid 图表。

## 代码

\`\`\`go
func main() {
	fmt.Println("hello")
}
\`\`\`

## 数学公式

行内公式 $E = mc^2$，块级公式：

$$
\\int_{-\\infty}^{\\infty} e^{-x^2} \\, dx = \\sqrt{\\pi}
$$

## 图表

\`\`\`mermaid
graph LR
  A[编辑] --> B[预览]
\`\`\`
`

const md = createRenderer()

const editorEl = ref<HTMLElement>()
const previewEl = ref<HTMLElement>()
const renderedHtml = ref('')
const outline = ref<OutlineItem[]>([])
const filePath = ref('')
const editorPct = ref(50)
const dirty = ref(false)

// --- sidebar: file tree + outline tabs ---

type SidebarTab = 'files' | 'outline'
const sidebarVisible = ref(false)
const sidebarTab = ref<SidebarTab>('files')
const folder = ref<main.FolderTree | null>(null)
const expanded = ref(new Set<string>())
// Reader mode hides the editor (CSS-only, so CodeMirror state survives);
// intentionally not persisted — every launch starts in edit mode.
const readerMode = ref(false)

function toggleReaderMode() {
  readerMode.value = !readerMode.value
}

interface TreeRow {
  node?: main.TreeNode
  depth: number
  /** True for the inline input row used by new/rename operations. */
  edit?: boolean
}

/** Flatten the folder tree to visible rows (expanded dirs only), injecting
 * the inline edit row for new/rename operations. */
const treeRows = computed<TreeRow[]>(() => {
  const rows: TreeRow[] = []
  if (!folder.value) return rows
  const edit = editing.value
  if (edit && edit.mode !== 'rename' && edit.parentPath === folder.value.path) {
    rows.push({ depth: 0, edit: true })
  }
  const walk = (nodes: main.TreeNode[], depth: number) => {
    for (const node of nodes) {
      if (edit?.mode === 'rename' && node.path === edit.oldPath) {
        rows.push({ node, depth, edit: true })
        continue
      }
      rows.push({ node, depth })
      if (node.isDir && expanded.value.has(node.path)) {
        if (edit && edit.mode !== 'rename' && node.path === edit.parentPath) {
          rows.push({ depth: depth + 1, edit: true })
        }
        walk(node.children ?? [], depth + 1)
      }
    }
  }
  walk(folder.value.children, 0)
  return rows
})

// --- session persistence (localStorage, keys share the markup- prefix) ---

const STORAGE = {
  folderPath: 'markup-folder-path',
  filePath: 'markup-file-path',
  sidebarVisible: 'markup-sidebar-visible',
  sidebarTab: 'markup-sidebar-tab',
  expandedDirs: 'markup-expanded-dirs',
  window: 'markup-window',
  backup: 'markup-backup',
  recentFiles: 'markup-recent-files',
  recentFolders: 'markup-recent-folders',
} as const

/** Write a value to localStorage; empty string removes the key. */
function persist(key: string, value: string) {
  try {
    if (value) localStorage.setItem(key, value)
    else localStorage.removeItem(key)
  } catch {
    // storage full/blocked: persistence is best-effort
  }
}

// --- recent files / folders (MRU, max 10 each) ---

function loadRecent(key: string): string[] {
  try {
    const v: unknown = JSON.parse(localStorage.getItem(key) ?? '[]')
    return Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : []
  } catch {
    return []
  }
}

const recentFiles = ref<string[]>(loadRecent(STORAGE.recentFiles))
const recentFolders = ref<string[]>(loadRecent(STORAGE.recentFolders))

function addRecent(list: typeof recentFiles, key: string, path: string) {
  const next = [path, ...list.value.filter((p) => p !== path)].slice(0, 10)
  list.value = next
  persist(key, JSON.stringify(next))
}

function removeRecent(list: typeof recentFiles, key: string, path: string) {
  const next = list.value.filter((p) => p !== path)
  list.value = next
  persist(key, JSON.stringify(next))
}

/** Template-friendly wrappers (refs auto-unwrap in templates). */
function removeRecentFile(path: string) {
  removeRecent(recentFiles, STORAGE.recentFiles, path)
}

function removeRecentFolder(path: string) {
  removeRecent(recentFolders, STORAGE.recentFolders, path)
}

function baseName(path: string): string {
  return path.split('/').pop() ?? path
}

// The welcome panel shows only in a true empty state (untitled + blank
// document), e.g. after Cmd+W. docIsEmpty tracks the editor via the update
// listener; a session restore that loads a file never shows it.
// welcomeDismissed lets 新建 leave the panel for a blank editor (reset by
// closeFile so Cmd+W keeps showing the recent list).
const docIsEmpty = ref(false)
const welcomeDismissed = ref(false)
const showWelcome = computed(() => !filePath.value && docIsEmpty.value && !welcomeDismissed.value)

// --- window geometry persistence ---

let winSaveTimer: ReturnType<typeof setTimeout> | undefined

async function saveWindowState() {
  try {
    const [size, pos] = await Promise.all([WindowGetSize(), WindowGetPosition()])
    persist(STORAGE.window, JSON.stringify({ x: pos.x, y: pos.y, w: size.w, h: size.h }))
  } catch {
    // runtime not ready / unloading: best effort
  }
}

function scheduleWindowSave() {
  if (winSaveTimer) clearTimeout(winSaveTimer)
  winSaveTimer = setTimeout(saveWindowState, 1000)
}

/**
 * Restore the last window size/position from localStorage. Sanity checks
 * reject implausible values (e.g. an external monitor was unplugged and the
 * saved position is now far off-screen).
 */
async function restoreWindowState() {
  try {
    const s: unknown = JSON.parse(localStorage.getItem(STORAGE.window) ?? 'null')
    if (!s || typeof s !== 'object') return
    const { x, y, w, h } = s as Record<string, unknown>
    if (typeof w !== 'number' || typeof h !== 'number' || w < 480 || h < 320) return
    WindowSetSize(Math.round(w), Math.round(h))
    if (typeof x === 'number' && typeof y === 'number' && x > -2000 && y > -2000 && x < 20000 && y < 20000) {
      WindowSetPosition(Math.round(x), Math.round(y))
    }
  } catch {
    // corrupted state or runtime not ready: keep defaults
  }
}

// --- crash backup (unsaved content survives a crash/force-quit) ---

const BACKUP_LIMIT = 5 * 1024 * 1024 // don't risk the localStorage quota
let backupTimer: ReturnType<typeof setTimeout> | undefined

/** Debounced: snapshot the dirty document ~2s after the last edit. */
function scheduleBackup() {
  if (backupTimer) clearTimeout(backupTimer)
  backupTimer = setTimeout(writeBackup, 2000)
}

function writeBackup() {
  if (!dirty.value) return
  const content = currentDoc()
  if (content.length > BACKUP_LIMIT) return
  persist(STORAGE.backup, JSON.stringify({ filePath: filePath.value, content, updatedAt: Date.now() }))
}

function clearBackup() {
  try {
    localStorage.removeItem(STORAGE.backup)
  } catch {
    // best effort
  }
}

/**
 * Runs after the session file restore: if a backup exists and differs from
 * what is now in the editor (i.e. from the disk content, or the file is
 * gone), ask whether to restore it. A backup equal to the current document
 * means the user did save after all — drop it silently.
 */
async function checkBackup() {
  const raw = localStorage.getItem(STORAGE.backup)
  if (!raw) return
  let backup: { filePath?: unknown; content?: unknown }
  try {
    backup = JSON.parse(raw)
  } catch {
    clearBackup()
    return
  }
  if (typeof backup.content !== 'string' || backup.content === currentDoc()) {
    clearBackup()
    return
  }
  const restore = await confirmDialog(t('dlg.restoreTitle'), t('dlg.restoreMsg'), t('dlg.restoreOk'), t('dlg.restoreDiscard'))
  if (restore) {
    filePath.value = typeof backup.filePath === 'string' ? backup.filePath : ''
    setEditorContent(backup.content)
    // setEditorContent flips dirty via the update listener anyway, but be explicit
    dirty.value = true
    // keep the backup: the content is still unsaved
  } else {
    clearBackup()
  }
}

/**
 * Restore the previous session: sidebar UI state synchronously from
 * localStorage, then the folder tree (RefreshFolder re-scans it) and the
 * open document (ReadFile). Startup restore skips confirmIfDirty and must
 * never fail the app: vanished folders/files are silently dropped.
 */
async function restoreState() {
  try {
    restoreWindowState()
    sidebarVisible.value = localStorage.getItem(STORAGE.sidebarVisible) === '1'
    const tab = localStorage.getItem(STORAGE.sidebarTab)
    if (tab === 'files' || tab === 'outline') sidebarTab.value = tab
    const dirs: unknown = JSON.parse(localStorage.getItem(STORAGE.expandedDirs) ?? '[]')
    if (Array.isArray(dirs)) {
      expanded.value = new Set(dirs.filter((d): d is string => typeof d === 'string'))
    }
  } catch {
    // corrupted state: fall back to defaults
  }

  const folderPath = localStorage.getItem(STORAGE.folderPath)
  if (folderPath) {
    try {
      const tree = await RefreshFolder(folderPath)
      if (tree.path) folder.value = tree
      else persist(STORAGE.folderPath, '')
    } catch {
      persist(STORAGE.folderPath, '')
    }
  }

  // Restore after the folder so the tree highlights the current file.
  const savedFile = localStorage.getItem(STORAGE.filePath)
  if (savedFile) {
    try {
      const content = await ReadFile(savedFile)
      filePath.value = savedFile
      setEditorContent(content)
      // setEditorContent's dispatch flips dirty via the update listener;
      // this is a startup restore, not an edit.
      dirty.value = false
    } catch {
      persist(STORAGE.filePath, '')
    }
  }

  // Runs last: compares the backup against the restored session document.
  await checkBackup()
}

const fileName = computed(() => {
  if (!filePath.value) return t('file.untitled')
  return filePath.value.split('/').pop() ?? filePath.value
})

let view: EditorView | undefined
let renderTimer: ReturnType<typeof setTimeout> | undefined

/** CodeMirror extensions that depend on the theme (swappable via compartment). */
const cmThemeCompartment = new Compartment()

function cmThemeExtensions(t: Theme) {
  return t === 'dark' ? [oneDark] : [syntaxHighlighting(defaultHighlightStyle)]
}

function currentDoc(): string {
  return view?.state.doc.toString() ?? ''
}

function render(src: string) {
  renderedHtml.value = md.render(src)
  outline.value = extractOutline(md, src)
}

function scheduleRender() {
  if (renderTimer) clearTimeout(renderTimer)
  renderTimer = setTimeout(() => render(currentDoc()), 150)
}

onMounted(() => {
  applyTheme(theme.value)
  view = new EditorView({
    state: EditorState.create({
      doc: INITIAL_DOC,
      extensions: [
        // App shortcuts with highest precedence so the editor's own
        // keymaps never swallow them; the global handler below skips
        // events already handled here (defaultPrevented).
        Prec.highest(
          keymap.of([
            { key: 'Mod-s', run: () => (save(), true) },
            { key: 'Mod-Shift-s', run: () => (saveAs(), true) },
            { key: 'Mod-o', run: () => (openFile(), true) },
            { key: 'Mod-n', run: () => (newFile(), true) },
            { key: 'Mod-w', run: () => (closeFile(), true) },
            { key: 'Mod-b', run: () => (toggleSidebar(), true) },
            { key: 'Mod-e', run: () => (exportHtml(), true) },
            { key: 'Mod-Shift-e', run: () => (exportPdf(), true) },
            { key: 'Mod-Shift-p', run: () => (toggleReaderMode(), true) },
          ]),
        ),
        history(),
        keymap.of([...defaultKeymap, ...historyKeymap, ...searchKeymap, indentWithTab]),
        markdown(),
        search({ top: true }),
        // Chinese search panel labels via the phrases localization mechanism;
        // English is CodeMirror's built-in default and needs no override.
        ...(locale === 'zh'
          ? [
              EditorState.phrases.of({
                'Find': '查找',
                'Replace': '替换',
                'next': '下一个',
                'previous': '上一个',
                'all': '全部',
                'match case': '区分大小写',
                'by word': '全词匹配',
                'regexp': '正则',
                'replace': '替换',
                'replace all': '全部替换',
                'close': '关闭',
              }),
            ]
          : []),
        editorDomHandlers,
        cmThemeCompartment.of(cmThemeExtensions(theme.value)),
        EditorView.lineWrapping,
        EditorView.theme({
          '&': { height: '100%', fontSize: '14px' },
          '.cm-scroller': { fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace' },
          '.cm-content': { padding: '12px 0' },
          '&.cm-focused': { outline: 'none' },
        }),
        EditorView.updateListener.of((update) => {
          if (update.docChanged) {
            docIsEmpty.value = update.state.doc.length === 0
            dirty.value = true
            scheduleRender()
            scheduleBackup()
          }
        }),
      ],
    }),
    parent: editorEl.value!,
  })
  view.scrollDOM.addEventListener('scroll', onEditorScroll, { passive: true })
  render(INITIAL_DOC)
  window.addEventListener('keydown', onGlobalKeydown)
  window.addEventListener('resize', scheduleWindowSave)
  window.addEventListener('beforeunload', saveWindowState)
  window.addEventListener('focus', onWindowFocus)
  OnFileDrop(onNativeFileDrop, false)
  // Tell Go the UI language so native dialogs match.
  SetLanguage(locale)
  // Wails bindings are injected before the frontend loads, so they are safe
  // to call here. restoreState handles its own errors; a failure must never
  // leave the window blank.
  restoreState()
})

function onGlobalKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape') {
    closeCtxMenu()
    return
  }
  if (confirmState.value) return // modal owns the keyboard while open
  if (!(e.metaKey || e.ctrlKey) || e.defaultPrevented) return
  const key = e.key.toLowerCase()
  if (key === 's') {
    e.preventDefault()
    if (e.shiftKey) saveAs()
    else save()
  } else if (key === 'o') {
    e.preventDefault()
    openFile()
  } else if (key === 'n') {
    e.preventDefault()
    newFile()
  } else if (key === 'w') {
    e.preventDefault()
    closeFile()
  } else if (key === 'b') {
    e.preventDefault()
    toggleSidebar()
  } else if (key === 'e') {
    e.preventDefault()
    if (e.shiftKey) exportPdf()
    else exportHtml()
  } else if (key === 'p' && e.shiftKey) {
    e.preventDefault()
    toggleReaderMode()
  }
}

onBeforeUnmount(() => {
  if (renderTimer) clearTimeout(renderTimer)
  if (winSaveTimer) clearTimeout(winSaveTimer)
  if (backupTimer) clearTimeout(backupTimer)
  if (editorScrollRaf) cancelAnimationFrame(editorScrollRaf)
  view?.scrollDOM.removeEventListener('scroll', onEditorScroll)
  window.removeEventListener('keydown', onGlobalKeydown)
  window.removeEventListener('resize', scheduleWindowSave)
  window.removeEventListener('beforeunload', saveWindowState)
  window.removeEventListener('focus', onWindowFocus)
  OnFileDropOff()
  view?.destroy()
})

watch(renderedHtml, async () => {
  await nextTick()
  if (previewEl.value) await renderMermaidBlocks(previewEl.value)
})

// Theme switch: CSS variables follow data-theme automatically; CodeMirror,
// highlight.js and mermaid need explicit updates. For mermaid, re-rendering
// the markdown is NOT enough: the HTML string is identical, so v-html skips
// patching and the watch below never fires — the old-theme SVG would stay.
// Force the preview DOM back to placeholders and re-render them explicitly.
// Editor content, dirty state and outline are untouched.
watch(theme, async (t) => {
  applyTheme(t)
  view?.dispatch({ effects: cmThemeCompartment.reconfigure(cmThemeExtensions(t)) })
  setHljsTheme(t)
  setMermaidTheme(t)
  render(currentDoc())
  await nextTick()
  if (previewEl.value) {
    previewEl.value.innerHTML = renderedHtml.value
    await renderMermaidBlocks(previewEl.value)
  }
})

// Keep Go informed of the current document path so the /__local__/ asset
// middleware can resolve relative image paths against its directory. Covers
// every filePath change: open dialog, tree click, relative-link jump,
// save-as, and new (unsaved) document.
watch(filePath, (p) => {
  SetCurrentFile(p)
  persist(STORAGE.filePath, p)
})

watch(folder, (f) => persist(STORAGE.folderPath, f?.path ?? ''))
watch(sidebarVisible, (v) => persist(STORAGE.sidebarVisible, v ? '1' : '0'))
watch(sidebarTab, (t) => persist(STORAGE.sidebarTab, t))
watch(expanded, (e) => persist(STORAGE.expandedDirs, JSON.stringify([...e])))

function onOutlineClick(item: OutlineItem) {
  if (previewEl.value) scrollToHeading(previewEl.value, item.index)
}

// --- editor -> preview scroll sync (one-way, so no loop guard is needed:
// programmatic preview scrolling never fires editor scroll events) ---

let editorScrollRaf = 0

function onEditorScroll() {
  if (editorScrollRaf) return // rAF throttle (~one sync per frame)
  editorScrollRaf = requestAnimationFrame(() => {
    editorScrollRaf = 0
    if (!view || !previewEl.value) return
    const block = view.lineBlockAtHeight(view.scrollDOM.scrollTop)
    scrollPreviewToLine(previewEl.value, view.state.doc.lineAt(block.from).number - 1)
  })
}

/**
 * Unified preview link handling. Always preventDefault: the WebView must
 * never navigate itself. Uses getAttribute('href') for the raw markdown-it
 * href, since a.href would be resolved against the WebView origin.
 */
async function onPreviewClick(event: MouseEvent) {
  const link = (event.target as HTMLElement).closest('a')
  if (!link || !previewEl.value) return

  // TOC links carry the heading's ordinal; reuse scrollToHeading for them.
  const tocIndex = link.getAttribute('data-toc-index')
  if (tocIndex !== null) {
    event.preventDefault()
    scrollToHeading(previewEl.value, Number(tocIndex))
    return
  }

  const href = link.getAttribute('href')
  if (!href) return
  event.preventDefault()

  // External links open in the system browser.
  if (/^(https?:\/\/|mailto:)/i.test(href)) {
    BrowserOpenURL(href)
    return
  }
  // Anchors (no heading ids yet) and non-file schemes: ignore.
  if (href.startsWith('#') || /^[a-z][a-z0-9+.-]*:/i.test(href)) return

  // Relative link: only .md/.markdown files are opened in the editor.
  const target = href.split('#')[0].split('?')[0]
  if (!/\.(md|markdown)$/i.test(target)) return
  if (!filePath.value) return // unsaved new document: nowhere to resolve from
  try {
    const abs = await ResolvePath(filePath.value, target)
    await openTreeFile(abs) // handles same-file, dirty confirm, read + open
  } catch (err) {
    console.warn(t('msg.linkOpenFailed'), href, err)
  }
}

function toggleSidebar() {
  sidebarVisible.value = !sidebarVisible.value
}

function toggleDir(path: string) {
  const next = new Set(expanded.value)
  if (next.has(path)) next.delete(path)
  else next.add(path)
  expanded.value = next
}

async function openFolder() {
  try {
    const tree = await OpenFolder()
    if (!tree.path) return
    folder.value = tree
    expanded.value = new Set()
    sidebarVisible.value = true
    sidebarTab.value = 'files'
    addRecent(recentFolders, STORAGE.recentFolders, tree.path)
  } catch (err) {
    console.error(t('msg.openFolderFailed'), err)
  }
}

/** Re-scan the current folder; files may have changed externally. */
async function refreshFolder() {
  if (!folder.value) return
  try {
    folder.value = await RefreshFolder(folder.value.path)
  } catch (err) {
    console.error(t('msg.refreshFolderFailed'), err)
  }
}

/** Open a Markdown file by path (tree click, relative link, native drop,
 * recent list). Returns 'ok' | 'cancel' | 'error' so callers can react. */
async function openTreeFile(path: string): Promise<'ok' | 'cancel' | 'error'> {
  if (path === filePath.value) return 'ok'
  if (!(await confirmIfDirty())) return 'cancel'
  try {
    const content = await ReadFile(path)
    filePath.value = path
    setEditorContent(content)
    dirty.value = false
    clearBackup()
    addRecent(recentFiles, STORAGE.recentFiles, path)
    return 'ok'
  } catch (err) {
    console.error(t('msg.openFileFailed'), err)
    return 'error'
  }
}

/** Recent list entries may be stale: a vanished file is dropped from the
 * list silently (console note only). */
async function openRecentFile(path: string) {
  if ((await openTreeFile(path)) === 'error') {
    removeRecent(recentFiles, STORAGE.recentFiles, path)
    console.warn(t('msg.recentMissing'), path)
  }
}

async function openRecentFolder(path: string) {
  try {
    const tree = await RefreshFolder(path)
    if (!tree.path) throw new Error('folder gone')
    folder.value = tree
    expanded.value = new Set()
    sidebarVisible.value = true
    sidebarTab.value = 'files'
    addRecent(recentFolders, STORAGE.recentFolders, path)
  } catch {
    removeRecent(recentFolders, STORAGE.recentFolders, path)
    console.warn(t('msg.recentMissing'), path)
  }
}

function onTreeRowClick(node: main.TreeNode) {
  if (node.isDir) toggleDir(node.path)
  else openTreeFile(node.path)
}

// --- file tree context menu & inline editing ---

type EditMode = 'new-file' | 'new-dir' | 'rename'

const ctxMenu = ref<{ x: number; y: number; node: main.TreeNode | null } | null>(null)
const editing = ref<{ mode: EditMode; parentPath: string; oldPath?: string } | null>(null)
const editName = ref('')

const ctxItems = computed(() => {
  const m = ctxMenu.value
  if (!m) return [] as { label: string; action: () => void }[]
  const items: { label: string; action: () => void }[] = []
  if (!m.node || m.node.isDir) {
    const dirPath = m.node ? m.node.path : folder.value?.path ?? ''
    items.push({ label: t('ctx.newFile'), action: () => startEdit('new-file', dirPath) })
    items.push({ label: t('ctx.newDir'), action: () => startEdit('new-dir', dirPath) })
  }
  if (m.node) {
    const node = m.node
    items.push({ label: t('ctx.rename'), action: () => startEdit('rename', '', node) })
    items.push({ label: t('ctx.delete'), action: () => deleteTreeNode(node) })
  }
  return items
})

function onRowContextMenu(row: TreeRow, event: MouseEvent) {
  if (row.edit || !row.node) return
  event.preventDefault()
  event.stopPropagation()
  ctxMenu.value = { x: event.clientX, y: event.clientY, node: row.node }
}

/** Blank area of the files pane: offer root-level new file/folder only. */
function onTreeBlankContextMenu(event: MouseEvent) {
  if (!folder.value) return
  event.preventDefault()
  ctxMenu.value = { x: event.clientX, y: event.clientY, node: null }
}

function closeCtxMenu() {
  ctxMenu.value = null
}

function startEdit(mode: EditMode, parentPath: string, node?: main.TreeNode) {
  closeCtxMenu()
  if (mode === 'rename' && node) {
    editing.value = { mode, parentPath: '', oldPath: node.path }
    editName.value = node.name
  } else {
    // make sure the target directory is expanded so the input row shows
    if (parentPath && parentPath !== folder.value?.path && !expanded.value.has(parentPath)) {
      const next = new Set(expanded.value)
      next.add(parentPath)
      expanded.value = next
    }
    editing.value = { mode, parentPath }
    editName.value = ''
  }
  nextTick(() => document.querySelector<HTMLInputElement>('.tree-edit-input')?.focus())
}

function cancelEdit() {
  editing.value = null
}

/** Rewrite p if it equals or sits under a renamed path prefix. */
function rewritePathPrefix(p: string, oldPrefix: string, newPrefix: string): string {
  if (p === oldPrefix) return newPrefix
  if (p.startsWith(oldPrefix + '/')) return newPrefix + p.slice(oldPrefix.length)
  return p
}

async function commitEdit() {
  const edit = editing.value
  if (!edit) return
  const name = editName.value.trim()
  if (!name) {
    cancelEdit()
    return
  }
  try {
    if (edit.mode === 'rename' && edit.oldPath) {
      const newPath = await RenamePath(edit.oldPath, name)
      // keep expansion state and the open file pointed at the new path
      const next = new Set<string>()
      for (const p of expanded.value) next.add(rewritePathPrefix(p, edit.oldPath, newPath))
      expanded.value = next
      if (filePath.value) filePath.value = rewritePathPrefix(filePath.value, edit.oldPath, newPath)
    } else if (edit.mode === 'new-file') {
      await CreateFile(edit.parentPath, name)
    } else {
      await CreateDir(edit.parentPath, name)
    }
    editing.value = null
    await refreshFolder()
  } catch (err) {
    // keep the input open so the name can be fixed
    console.error(t('msg.fileOpFailed'), err)
  }
}

async function deleteTreeNode(node: main.TreeNode) {
  closeCtxMenu()
  try {
    const ok = await confirmDialog(
      t(node.isDir ? 'dlg.deleteTitleDir' : 'dlg.deleteTitleFile'),
      t('dlg.deleteMsg', { name: node.name }),
      t('dlg.deleteOk'),
      t('dlg.cancel'),
    )
    if (!ok) return
    await DeletePath(node.path)
    if (filePath.value && (filePath.value === node.path || filePath.value.startsWith(node.path + '/'))) {
      // the open document was deleted: reset to an untitled state
      filePath.value = ''
      setEditorContent('')
      dirty.value = false
      welcomeDismissed.value = false
    }
    await refreshFolder()
  } catch (err) {
    console.error(t('msg.deleteFailed'), err)
  }
}

// --- paste / drop images (Typora-style) ---

function fileToBase64(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => resolve(String(reader.result).split(',')[1] ?? '')
    reader.onerror = () => reject(reader.error)
    reader.readAsDataURL(file)
  })
}

/** Insert a relative ![](...) link to rel at pos. The existing /__local__/
 * middleware serves it in the preview on the next render. */
function insertImageLink(rel: string, pos: number) {
  const insert = `![](${rel})`
  view?.dispatch({
    changes: { from: pos, insert },
    selection: { anchor: pos + insert.length },
  })
}

/** Save a pasted image into assets/ next to the document, then link it. */
async function insertImageFile(file: File, pos: number) {
  if (!filePath.value) {
    console.warn(t('msg.saveDocFirst'))
    return
  }
  try {
    const b64 = await fileToBase64(file)
    insertImageLink(await SaveImage(filePath.value, b64), pos)
  } catch (err) {
    console.error(t('msg.saveImageFailed'), err)
  }
}

/** Copy a dropped image file (absolute path from the native drop callback)
 * into assets/ next to the document, then link it. */
async function insertImageFromPath(srcPath: string, pos: number) {
  if (!filePath.value) {
    console.warn(t('msg.saveDocFirst'))
    return
  }
  try {
    insertImageLink(await SaveImageFile(filePath.value, srcPath), pos)
  } catch (err) {
    console.error(t('msg.saveImageFailed'), err)
  }
}

/**
 * Native file drop (wails DragAndDrop): the callback receives absolute
 * paths, which WKWebView's File objects lack. Images are copied into
 * assets/ and linked at the drop position; the first .md/.markdown file is
 * opened (openTreeFile handles the dirty confirm); anything else is
 * ignored. The webview's own drop handling is disabled natively
 * (DisableWebViewDrop), so this is the only drop path.
 */
function onNativeFileDrop(x: number, y: number, paths: string[]) {
  const pos = view?.posAtCoords({ x, y }) ?? view?.state.selection.main.head ?? 0
  let mdOpened = false
  for (const p of paths) {
    if (/\.(png|jpe?g|gif|webp|svg|bmp|avif)$/i.test(p)) {
      insertImageFromPath(p, pos)
    } else if (/\.(md|markdown)$/i.test(p)) {
      if (mdOpened) continue
      mdOpened = true
      openTreeFile(p)
    }
  }
}

const editorDomHandlers = EditorView.domEventHandlers({
  paste(event, view) {
    const items = event.clipboardData?.items
    if (!items) return // fall through to default paste
    for (const item of Array.from(items)) {
      if (!item.type.startsWith('image/')) continue
      const file = item.getAsFile()
      if (!file) continue
      event.preventDefault()
      insertImageFile(file, view.state.selection.main.head)
      return true
    }
    // text-only clipboard: default paste
  },
})

function setEditorContent(content: string) {
  view?.dispatch({
    changes: { from: 0, to: view.state.doc.length, insert: content },
  })
}

// --- modal confirm dialog ---
// Native runtime.MessageDialog is a Win32 MessageBox on Windows: custom
// button labels are ignored and a warning dialog shows a single "OK" whose
// result never matches our labels. An HTML modal behaves identically on
// every platform, so all confirm-style prompts go through confirmDialog.
// (System file open/save dialogs are unaffected and stay native.)

const confirmState = ref<{
  title: string
  message: string
  confirmText: string
  cancelText: string
  resolve: (ok: boolean) => void
} | null>(null)
const confirmBoxEl = ref<HTMLElement>()

function confirmDialog(title: string, message: string, confirmText: string, cancelText: string): Promise<boolean> {
  if (confirmState.value) return Promise.resolve(false) // one at a time
  return new Promise((resolve) => {
    confirmState.value = { title, message, confirmText, cancelText, resolve }
    nextTick(() => confirmBoxEl.value?.focus())
  })
}

function settleConfirm(ok: boolean) {
  confirmState.value?.resolve(ok)
  confirmState.value = null
}

/** Ask the user before discarding unsaved changes; true = proceed. */
async function confirmIfDirty(): Promise<boolean> {
  if (!dirty.value) return true
  return confirmDialog(t('dlg.discardTitle'), t('dlg.discardMsg'), t('dlg.discardOk'), t('dlg.cancel'))
}

// Guards against opening a second native dialog while one is showing.
let dialogBusy = false

async function openFile() {
  if (dialogBusy || !(await confirmIfDirty())) return
  dialogBusy = true
  try {
    const file = await OpenFile()
    if (!file.path) return
    filePath.value = file.path
    setEditorContent(file.content)
    dirty.value = false
    clearBackup()
    addRecent(recentFiles, STORAGE.recentFiles, file.path)
  } catch (err) {
    console.error(t('msg.openFileFailed'), err)
  } finally {
    dialogBusy = false
  }
}

function newFile() {
  confirmIfDirty().then((proceed) => {
    if (!proceed) return
    filePath.value = ''
    setEditorContent('')
    dirty.value = false
    clearBackup()
    // Leave the welcome panel and hand the user a focused blank editor.
    welcomeDismissed.value = true
    view?.focus()
  })
}

/** Close the current file, back to an untouched untitled document. No-op
 * when already there. The filePath watcher notifies Go (SetCurrentFile("")),
 * and the tree highlight follows automatically. */
async function closeFile() {
  if (!filePath.value && !dirty.value) return
  if (!(await confirmIfDirty())) return
  filePath.value = ''
  setEditorContent('')
  dirty.value = false
  clearBackup()
  welcomeDismissed.value = false // empty state again: show the recent list
}

/** Save to the current path if known, otherwise fall back to the dialog. */
async function save() {
  try {
    if (filePath.value) {
      try {
        await SaveToPath(filePath.value, currentDoc())
      } catch (err) {
        if (!String(err).includes('external conflict')) throw err
        // The file changed on disk: ask before overwriting. Canceling
        // leaves the document dirty and nothing is written.
        const ok = await confirmDialog(t('dlg.overwriteTitle'), t('dlg.overwriteMsg'), t('dlg.overwriteOk'), t('dlg.cancel'))
        if (!ok) return
        await SaveToPathForce(filePath.value, currentDoc())
      }
    } else {
      const path = await SaveFile(currentDoc())
      if (!path) return
      filePath.value = path
      addRecent(recentFiles, STORAGE.recentFiles, path)
    }
    dirty.value = false
    clearBackup()
  } catch (err) {
    console.error(t('msg.saveFileFailed'), err)
  }
}

/** Save As: always show the save dialog. */
async function saveAs() {
  if (dialogBusy) return
  dialogBusy = true
  try {
    const path = await SaveFile(currentDoc())
    if (!path) return
    filePath.value = path
    dirty.value = false
    clearBackup()
    addRecent(recentFiles, STORAGE.recentFiles, path)
  } catch (err) {
    console.error(t('msg.saveAsFailed'), err)
  } finally {
    dialogBusy = false
  }
}

// --- export HTML / PDF ---

/** Default export file name: current document with the given suffix. */
function exportFileName(ext: 'html' | 'pdf'): string {
  if (!filePath.value) return `untitled.${ext}`
  const base = (filePath.value.split('/').pop() ?? 'untitled').replace(/\.(md|markdown)$/i, '')
  return `${base}.${ext}`
}

async function exportHtml() {
  if (dialogBusy || !previewEl.value) return
  dialogBusy = true
  try {
    const name = exportFileName('html')
    const html = await buildExportHtml(previewEl.value, name.replace(/\.html$/i, ''), filePath.value)
    const saved = await ExportHTML(name, html)
    if (saved) console.log(t('msg.exported'), saved)
  } catch (err) {
    console.error(t('msg.exportFailed'), err)
  } finally {
    dialogBusy = false
  }
}

/** Same self-contained HTML as exportHtml, converted to PDF on the Go side
 * (headless Chrome preferred). */
async function exportPdf() {
  if (dialogBusy || !previewEl.value) return
  dialogBusy = true
  try {
    const name = exportFileName('pdf')
    const html = await buildExportHtml(previewEl.value, name.replace(/\.pdf$/i, ''), filePath.value)
    const saved = await ExportPDF(name, html)
    if (saved) console.log(t('msg.exportedPdf'), saved)
  } catch (err) {
    console.error(t('msg.exportFailed'), err)
  } finally {
    dialogBusy = false
  }
}

// --- external change detection ---

/** On window focus: reload the file if it changed on disk while the editor
 * is clean. A dirty editor is left alone (save() has a conflict dialog). */
async function onWindowFocus() {
  if (!filePath.value) return
  try {
    const status = await CheckExternalChange()
    if (status === 'missing') {
      if (!dirty.value) console.warn(t('msg.fileMissing'))
      return
    }
    if (status !== 'changed') return
    if (dirty.value) {
      console.warn(t('msg.fileChangedDirty'))
      return
    }
    const content = await ReadFile(filePath.value) // also refreshes the stamp
    setEditorContent(content)
    dirty.value = false
    clearBackup()
  } catch {
    // detection is best effort
  }
}

// --- draggable splitter ---

function startDrag(event: MouseEvent) {
  event.preventDefault()
  const container = (event.target as HTMLElement).parentElement
  if (!container) return
  const onMove = (e: MouseEvent) => {
    const rect = container.getBoundingClientRect()
    const pct = ((e.clientX - rect.left) / rect.width) * 100
    editorPct.value = Math.min(80, Math.max(20, pct))
  }
  const onUp = () => {
    window.removeEventListener('mousemove', onMove)
    window.removeEventListener('mouseup', onUp)
  }
  window.addEventListener('mousemove', onMove)
  window.addEventListener('mouseup', onUp)
}
</script>

<template>
  <div class="app">
    <header class="toolbar">
      <button class="theme-toggle" :class="{ 'sidebar-on': sidebarVisible }" :title="t('title.toggleSidebar')" @click="toggleSidebar">☰</button>
      <button :class="{ 'sidebar-on': readerMode }" :title="t('title.toggleReader')" @click="toggleReaderMode">{{ readerMode ? t('toolbar.edit') : t('toolbar.read') }}</button>
      <button @click="newFile">{{ t('toolbar.new') }}</button>
      <button @click="openFile">{{ t('toolbar.open') }}</button>
      <button :title="t('title.closeFile')" @click="closeFile">{{ t('toolbar.close') }}</button>
      <button @click="openFolder">{{ t('toolbar.folder') }}</button>
      <button @click="save">{{ t('toolbar.save') }}</button>
      <button :title="t('title.exportHtml')" @click="exportHtml">{{ t('toolbar.exportHtml') }}</button>
      <button :title="t('title.exportPdf')" @click="exportPdf">{{ t('toolbar.exportPdf') }}</button>
      <button class="theme-toggle" :title="theme === 'dark' ? t('title.toLight') : t('title.toDark')" @click="toggleTheme">
        {{ theme === 'dark' ? '☀' : '☾' }}
      </button>
      <span class="filename" :title="filePath">{{ fileName }}<span v-if="dirty" class="dirty-dot"> ●</span></span>
      <button class="theme-toggle github-link" :title="t('title.github')" @click="BrowserOpenURL('https://github.com/garudaen/markup')">
        <svg viewBox="0 0 16 16" width="16" height="16" fill="currentColor" aria-hidden="true">
          <path d="M8 0c4.42 0 8 3.58 8 8a8.013 8.013 0 0 1-5.45 7.59c-.4.08-.55-.17-.55-.38 0-.27.01-1.13.01-2.2 0-.75-.25-1.23-.54-1.48 1.78-.2 3.65-.88 3.65-3.95 0-.88-.31-1.59-.82-2.15.08-.2.36-1.02-.08-2.12 0 0-.67-.22-2.2.82-.64-.18-1.32-.27-2-.27-.68 0-1.36.09-2 .27-1.53-1.03-2.2-.82-2.2-.82-.44 1.1-.16 1.92-.08 2.12-.51.56-.82 1.28-.82 2.15 0 3.06 1.86 3.75 3.64 3.95-.23.2-.44.55-.51 1.07-.46.21-1.61.55-2.33-.66-.15-.24-.6-.83-1.23-.82-.67.01-.27.38.01.53.34.19.73.9.82 1.13.16.45.68 1.31 2.69.94 0 .67.01 1.3.01 1.49 0 .21-.15.45-.55.38A7.995 7.995 0 0 1 0 8c0-4.42 3.58-8 8-8Z" />
        </svg>
      </button>
    </header>
    <div class="main">
      <aside v-if="sidebarVisible" class="sidebar">
        <div class="sidebar-tabs">
          <button :class="{ active: sidebarTab === 'files' }" @click="sidebarTab = 'files'">{{ t('tab.files') }}</button>
          <button :class="{ active: sidebarTab === 'outline' }" @click="sidebarTab = 'outline'">{{ t('tab.outline') }}</button>
        </div>
        <div v-if="sidebarTab === 'files'" class="sidebar-body" @contextmenu="onTreeBlankContextMenu">
          <template v-if="folder">
            <div class="folder-header">
              <span class="folder-name" :title="folder.path">{{ folder.name }}</span>
              <button class="folder-refresh" :title="t('title.refresh')" @click="refreshFolder">⟳</button>
            </div>
            <div v-if="treeRows.length" class="file-tree">
              <div
                v-for="row in treeRows"
                :key="row.edit ? '__edit__' : row.node!.path"
                class="tree-row"
                :class="{ 'tree-active': row.node && !row.node.isDir && row.node.path === filePath }"
                :style="{ paddingLeft: 6 + row.depth * 14 + 'px' }"
                @click="row.node && onTreeRowClick(row.node)"
                @contextmenu="onRowContextMenu(row, $event)"
              >
                <template v-if="row.edit">
                  <span class="tree-arrow"></span>
                  <input
                    v-model="editName"
                    class="tree-edit-input"
                    @keydown.enter.prevent="commitEdit"
                    @keydown.esc.prevent.stop="cancelEdit"
                    @blur="cancelEdit"
                    @click.stop
                  />
                </template>
                <template v-else>
                  <span class="tree-arrow">{{ row.node!.isDir ? (expanded.has(row.node!.path) ? '▾' : '▸') : '' }}</span>
                  <span class="tree-name">{{ row.node!.name }}</span>
                </template>
              </div>
            </div>
            <div v-else class="sidebar-hint">{{ t('hint.noMarkdown') }}</div>
          </template>
          <div v-else class="sidebar-hint">
            <p>{{ t('hint.noFolder') }}</p>
            <button class="open-folder-btn" @click="openFolder">{{ t('action.openFolder') }}</button>
          </div>
        </div>
        <div v-else class="sidebar-body">
          <template v-if="outline.length">
            <div
              v-for="item in outline"
              :key="item.index"
              class="outline-item"
              :class="`outline-h${item.level}`"
              @click="onOutlineClick(item)"
            >
              {{ item.text }}
            </div>
          </template>
          <div v-else class="sidebar-hint">{{ t('hint.noHeadings') }}</div>
        </div>
      </aside>
      <div v-show="!readerMode" class="editor" :style="{ flexBasis: editorPct + '%' }">
        <div v-show="!showWelcome" ref="editorEl" class="editor-host"></div>
        <div v-if="showWelcome" class="welcome">
          <section v-if="recentFiles.length">
            <h3>{{ t('recent.files') }}</h3>
            <div v-for="p in recentFiles" :key="p" class="recent-row" @click="openRecentFile(p)">
              <span class="recent-name">{{ baseName(p) }}</span>
              <span class="recent-path" :title="p">{{ p }}</span>
              <button
                class="recent-remove"
                :title="t('recent.remove')"
                @click.stop="removeRecentFile(p)"
              >×</button>
            </div>
          </section>
          <section v-if="recentFolders.length">
            <h3>{{ t('recent.folders') }}</h3>
            <div v-for="p in recentFolders" :key="p" class="recent-row" @click="openRecentFolder(p)">
              <span class="recent-name">{{ baseName(p) }}</span>
              <span class="recent-path" :title="p">{{ p }}</span>
              <button
                class="recent-remove"
                :title="t('recent.remove')"
                @click.stop="removeRecentFolder(p)"
              >×</button>
            </div>
          </section>
          <p v-if="!recentFiles.length && !recentFolders.length" class="welcome-hint">{{ t('recent.empty') }}</p>
        </div>
      </div>
      <div v-show="!readerMode" class="divider" @mousedown="startDrag"></div>
      <div ref="previewEl" class="preview markdown-body" v-html="renderedHtml" @click="onPreviewClick"></div>
    </div>
    <div v-if="ctxMenu" class="ctx-overlay" @click="closeCtxMenu" @contextmenu.prevent="closeCtxMenu">
      <div class="ctx-menu" :style="{ left: ctxMenu.x + 'px', top: ctxMenu.y + 'px' }" @click.stop>
        <button v-for="item in ctxItems" :key="item.label" @click="item.action">
          {{ item.label }}
        </button>
      </div>
    </div>
    <div v-if="confirmState" class="modal-overlay" @click.self="settleConfirm(false)">
      <div
        ref="confirmBoxEl"
        class="modal"
        tabindex="-1"
        @keydown.enter.prevent="settleConfirm(true)"
        @keydown.esc.prevent.stop="settleConfirm(false)"
      >
        <h3 class="modal-title">{{ confirmState.title }}</h3>
        <p class="modal-message">{{ confirmState.message }}</p>
        <div class="modal-actions">
          <button class="modal-btn" @click="settleConfirm(false)">{{ confirmState.cancelText }}</button>
          <button class="modal-btn modal-btn-primary" @click="settleConfirm(true)">{{ confirmState.confirmText }}</button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.app {
  display: flex;
  flex-direction: column;
  height: 100vh;
  color: var(--text);
}

.toolbar {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 12px;
  border-bottom: 1px solid var(--border);
  background: var(--bg-soft);
  flex: none;
}

.toolbar button {
  padding: 4px 12px;
  font-size: 13px;
  border: 1px solid var(--border-strong);
  border-radius: 6px;
  background: var(--btn-bg);
  color: var(--text);
  cursor: pointer;
}

.toolbar button:hover {
  background: var(--bg-hover);
}

.theme-toggle {
  min-width: 32px;
}

/* push the GitHub link to the far right of the toolbar */
.github-link {
  margin-left: auto;
  display: inline-flex;
  align-items: center;
  justify-content: center;
}

.filename {
  font-size: 13px;
  color: var(--text-muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.dirty-dot {
  color: #d97706;
}

.main {
  display: flex;
  flex: 1;
  min-height: 0;
}

.sidebar {
  flex: 0 0 200px;
  display: flex;
  flex-direction: column;
  min-height: 0;
  border-right: 1px solid var(--border);
  font-size: 13px;
}

.sidebar-tabs {
  display: flex;
  gap: 4px;
  padding: 6px 8px;
  border-bottom: 1px solid var(--border);
  flex: none;
}

.sidebar-tabs button {
  flex: 1;
  padding: 3px 0;
  font-size: 12px;
  border: none;
  border-radius: 4px;
  background: transparent;
  color: var(--text-muted);
  cursor: pointer;
}

.sidebar-tabs button:hover {
  background: var(--bg-hover);
}

.sidebar-tabs button.active {
  background: var(--bg-hover);
  color: var(--text);
  font-weight: 600;
}

.sidebar-body {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  padding: 4px 0 12px;
}

.folder-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 6px 8px 4px;
  font-weight: 600;
  color: var(--text);
}

.folder-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.folder-refresh {
  flex: none;
  padding: 0 4px;
  border: none;
  border-radius: 4px;
  background: transparent;
  color: var(--text-muted);
  font-size: 13px;
  cursor: pointer;
}

.folder-refresh:hover {
  background: var(--bg-hover);
  color: var(--text);
}

.tree-row {
  display: flex;
  align-items: center;
  gap: 2px;
  padding: 3px 8px;
  cursor: pointer;
  color: var(--text-muted);
  white-space: nowrap;
  overflow: hidden;
}

.tree-row:hover {
  background: var(--bg-hover);
  color: var(--text);
}

.tree-row.tree-active {
  background: var(--bg-hover);
  color: var(--text);
  font-weight: 600;
}

.tree-arrow {
  flex: none;
  width: 14px;
  text-align: center;
  font-size: 10px;
}

.tree-name {
  overflow: hidden;
  text-overflow: ellipsis;
}

.tree-edit-input {
  flex: 1;
  min-width: 0;
  padding: 1px 4px;
  border: 1px solid var(--border-strong);
  border-radius: 3px;
  background: var(--bg);
  color: var(--text);
  font-size: 13px;
  outline: none;
}

.ctx-overlay {
  position: fixed;
  inset: 0;
  z-index: 100;
}

.ctx-menu {
  position: fixed;
  min-width: 120px;
  padding: 4px;
  border: 1px solid var(--border);
  border-radius: 6px;
  background: var(--bg-soft);
  box-shadow: 0 4px 16px rgba(0, 0, 0, 0.18);
  display: flex;
  flex-direction: column;
}

.ctx-menu button {
  padding: 4px 12px;
  border: none;
  border-radius: 4px;
  background: transparent;
  color: var(--text);
  font-size: 13px;
  text-align: left;
  cursor: pointer;
  white-space: nowrap;
}

.ctx-menu button:hover {
  background: var(--bg-hover);
}

/* modal confirm dialog */
.modal-overlay {
  position: fixed;
  inset: 0;
  z-index: 200;
  display: flex;
  align-items: center;
  justify-content: center;
  background: rgba(0, 0, 0, 0.4);
}

.modal {
  min-width: 320px;
  max-width: 440px;
  padding: 18px 20px 14px;
  border: 1px solid var(--border);
  border-radius: 8px;
  background: var(--bg);
  color: var(--text);
  box-shadow: 0 8px 32px rgba(0, 0, 0, 0.28);
  outline: none;
}

.modal-title {
  margin: 0 0 8px;
  font-size: 15px;
  font-weight: 600;
}

.modal-message {
  margin: 0 0 16px;
  font-size: 13px;
  line-height: 1.6;
  color: var(--text-muted);
  word-wrap: break-word;
}

.modal-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}

.modal-btn {
  padding: 4px 14px;
  font-size: 13px;
  border: 1px solid var(--border-strong);
  border-radius: 6px;
  background: var(--btn-bg);
  color: var(--text);
  cursor: pointer;
}

.modal-btn:hover {
  background: var(--bg-hover);
}

.modal-btn-primary {
  background: var(--border-strong);
}

.modal-btn-primary:hover {
  background: var(--text-faint);
}

.sidebar-hint {
  padding: 16px 12px;
  color: var(--text-faint);
  text-align: center;
}

.sidebar-hint p {
  margin: 0 0 10px;
}

.open-folder-btn {
  padding: 4px 12px;
  font-size: 13px;
  border: 1px solid var(--border-strong);
  border-radius: 6px;
  background: var(--btn-bg);
  color: var(--text);
  cursor: pointer;
}

.open-folder-btn:hover {
  background: var(--bg-hover);
}

.sidebar-on {
  background: var(--bg-hover);
}

.outline-item {
  padding: 3px 8px;
  border-radius: 4px;
  cursor: pointer;
  color: var(--text-muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.outline-item:hover {
  background: var(--bg-hover);
  color: var(--text);
}

.outline-h2 { padding-left: 20px; }
.outline-h3 { padding-left: 32px; }

.editor {
  flex: 0 0 50%;
  min-width: 0;
  overflow: hidden;
}

.editor-host {
  height: 100%;
}

/* welcome / recent panel, shown in the empty untitled state */
.welcome {
  height: 100%;
  overflow-y: auto;
  padding: 32px 28px;
  background: var(--bg);
}

.welcome h3 {
  margin: 0 0 6px;
  font-size: 12px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  color: var(--text-faint);
}

.welcome section + section {
  margin-top: 20px;
}

.recent-row {
  display: flex;
  align-items: baseline;
  gap: 10px;
  padding: 5px 8px;
  border-radius: 5px;
  cursor: pointer;
}

.recent-row:hover {
  background: var(--bg-hover);
}

.recent-name {
  flex: none;
  font-size: 14px;
  color: var(--text);
}

.recent-path {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  direction: rtl; /* truncate long paths at the front */
  text-align: left;
  font-size: 12px;
  color: var(--text-faint);
}

.recent-remove {
  flex: none;
  visibility: hidden;
  padding: 0 6px;
  border: none;
  border-radius: 4px;
  background: transparent;
  color: var(--text-muted);
  font-size: 14px;
  line-height: 1.4;
  cursor: pointer;
}

.recent-row:hover .recent-remove {
  visibility: visible;
}

.recent-remove:hover {
  background: var(--bg-hover);
  color: var(--text);
}

.welcome-hint {
  margin: 0;
  color: var(--text-faint);
  font-size: 14px;
}

.editor-host :deep(.cm-editor) {
  height: 100%;
  background: var(--bg);
  color: var(--text);
}

/* search panel (Cmd+F): follow app theme variables */
.editor-host :deep(.cm-panels) {
  background: var(--bg-soft);
  color: var(--text);
}

.editor-host :deep(.cm-panels-top) {
  border-bottom: 1px solid var(--border);
}

.editor-host :deep(.cm-panel.cm-search) {
  padding: 6px 8px;
  font-size: 13px;
}

.editor-host :deep(.cm-panel.cm-search input),
.editor-host :deep(.cm-panel.cm-search button) {
  border: 1px solid var(--border-strong);
  border-radius: 4px;
  background: var(--btn-bg);
  color: var(--text);
  font-size: 12px;
}

.editor-host :deep(.cm-panel.cm-search button) {
  cursor: pointer;
}

.editor-host :deep(.cm-panel.cm-search input:focus) {
  outline: none;
  border-color: var(--text-muted);
}

.editor-host :deep(.cm-searchMatch) {
  background: rgba(255, 200, 0, 0.35);
}

.editor-host :deep(.cm-searchMatch-selected) {
  background: rgba(255, 150, 0, 0.5);
}

.divider {
  flex: 0 0 5px;
  cursor: col-resize;
  background: var(--border);
}

.divider:hover {
  background: var(--border-strong);
}

.preview {
  flex: 1;
  min-width: 0;
  overflow-y: auto;
  padding: 16px 24px;
  background: var(--bg);
}
</style>
