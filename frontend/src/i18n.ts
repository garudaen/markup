export type Locale = 'zh' | 'en'

/**
 * UI language follows the OS language reported by the WebView
 * (navigator.language reflects the macOS system language). Chinese
 * (zh-*) gets the Chinese UI, everything else English. Not persisted.
 */
export const locale: Locale = navigator.language.toLowerCase().startsWith('zh') ? 'zh' : 'en'

const messages = {
  'toolbar.new': { zh: '新建', en: 'New' },
  'toolbar.open': { zh: '打开', en: 'Open' },
  'toolbar.close': { zh: '关闭', en: 'Close' },
  'toolbar.folder': { zh: '文件夹', en: 'Folder' },
  'toolbar.save': { zh: '保存', en: 'Save' },
  'toolbar.exportHtml': { zh: '导出 HTML', en: 'HTML' },
  'toolbar.exportPdf': { zh: '导出 PDF', en: 'PDF' },
  'toolbar.read': { zh: '阅读', en: 'Read' },
  'title.toggleSidebar': { zh: '切换侧栏 (⌘B)', en: 'Toggle sidebar (⌘B)' },
  'title.toggleReader': { zh: '切换阅读模式 (⌘⇧P)', en: 'Toggle reader mode (⌘⇧P)' },
  'title.exportHtml': { zh: '导出 HTML (⌘E)', en: 'Export HTML (⌘E)' },
  'title.exportPdf': { zh: '导出 PDF (⌘⇧E)', en: 'Export PDF (⌘⇧E)' },
  'title.toLight': { zh: '切换为浅色', en: 'Switch to light theme' },
  'title.toDark': { zh: '切换为深色', en: 'Switch to dark theme' },
  'title.refresh': { zh: '刷新', en: 'Refresh' },
  'title.github': { zh: 'GitHub 仓库', en: 'GitHub repository' },
  'title.closeFile': { zh: '关闭当前文件 (⌘W)', en: 'Close file (⌘W)' },
  'tab.files': { zh: '文件', en: 'Files' },
  'tab.outline': { zh: '大纲', en: 'Outline' },
  'hint.noMarkdown': { zh: '文件夹内没有 Markdown 文件', en: 'No Markdown files in this folder' },
  'hint.noFolder': { zh: '尚未打开文件夹', en: 'No folder opened' },
  'hint.noHeadings': { zh: '文档中还没有标题', en: 'No headings in the document' },
  'action.openFolder': { zh: '打开文件夹', en: 'Open Folder' },
  'ctx.newFile': { zh: '新建文件', en: 'New File' },
  'ctx.newDir': { zh: '新建文件夹', en: 'New Folder' },
  'ctx.rename': { zh: '重命名', en: 'Rename' },
  'ctx.delete': { zh: '删除', en: 'Delete' },
  'file.untitled': { zh: '未命名', en: 'Untitled' },
  'msg.linkOpenFailed': { zh: '链接目标不存在或无法打开:', en: 'Link target missing or cannot be opened:' },
  'msg.openFolderFailed': { zh: '打开文件夹失败:', en: 'Failed to open folder:' },
  'msg.refreshFolderFailed': { zh: '刷新文件夹失败:', en: 'Failed to refresh folder:' },
  'msg.openFileFailed': { zh: '打开文件失败:', en: 'Failed to open file:' },
  'msg.fileOpFailed': { zh: '文件操作失败:', en: 'File operation failed:' },
  'msg.deleteFailed': { zh: '删除失败:', en: 'Delete failed:' },
  'msg.saveDocFirst': {
    zh: '请先保存文档，再粘贴/拖入图片',
    en: 'Save the document before pasting or dropping images',
  },
  'msg.saveImageFailed': { zh: '保存图片失败:', en: 'Failed to save image:' },
  'msg.saveFileFailed': { zh: '保存文件失败:', en: 'Failed to save file:' },
  'msg.saveAsFailed': { zh: '另存为失败:', en: 'Failed to save as:' },
  'msg.exported': { zh: '已导出 HTML:', en: 'Exported HTML:' },
  'msg.exportedPdf': { zh: '已导出 PDF:', en: 'Exported PDF:' },
  'msg.exportFailed': { zh: '导出失败:', en: 'Export failed:' },
  'msg.fileMissing': {
    zh: '文件已被外部删除，编辑器内容保留',
    en: 'File was deleted externally; editor content kept',
  },
  'msg.fileChangedDirty': {
    zh: '文件已被外部修改，保存时将提示冲突',
    en: 'File changed externally; you will be warned on save',
  },
} as const

export type MessageKey = keyof typeof messages

/** Translate a UI message into the current locale. */
export function t(key: MessageKey): string {
  return messages[key][locale]
}
