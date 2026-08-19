package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// fileStamp identifies a file version by modification time and size.
type fileStamp struct {
	modTime time.Time
	size    int64
}

// App struct
type App struct {
	ctx context.Context
	// docDir is the directory of the currently open document, used by the
	// asset middleware to resolve relative image paths ("" for a new,
	// never-saved document). rootDir is the currently opened folder; all
	// file-tree mutations are restricted to it. curFile/curStamp track the
	// last read or written version of the open document for external
	// change detection. All guarded by docDirMu: bindings run on the main
	// goroutine while asset requests come from HTTP handler goroutines.
	docDirMu sync.RWMutex
	docDir   string
	rootDir  string
	lang     string
	curFile  string
	curStamp fileStamp
}

// NewApp creates a new App application struct
func NewApp() *App {
	return &App{lang: "zh"}
}

// SetLanguage records the UI language ("zh" or "en") detected by the
// frontend; native dialogs then use matching strings.
func (a *App) SetLanguage(lang string) {
	a.docDirMu.Lock()
	defer a.docDirMu.Unlock()
	if lang == "zh" || lang == "en" {
		a.lang = lang
	}
}

// tr picks the dialog string for the current UI language.
func (a *App) tr(zh, en string) string {
	a.docDirMu.RLock()
	defer a.docDirMu.RUnlock()
	if a.lang == "en" {
		return en
	}
	return zh
}

// startup is called when the app starts. The context is saved
// so we can call the runtime methods
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
}

// OpenedFile is the result of a successful OpenFile call.
// Both fields are empty when the user cancels the dialog.
type OpenedFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// OpenFile shows a macOS open dialog for Markdown/text files and returns the
// chosen file's path and content. It returns an empty OpenedFile when the
// user cancels the dialog.
func (a *App) OpenFile() (OpenedFile, error) {
	path, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title: a.tr("打开文件", "Open File"),
		Filters: []runtime.FileFilter{
			{DisplayName: a.tr("Markdown 文件 (*.md, *.markdown, *.txt)", "Markdown Files (*.md, *.markdown, *.txt)"), Pattern: "*.md;*.markdown;*.txt"},
		},
	})
	if err != nil || path == "" {
		return OpenedFile{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return OpenedFile{}, err
	}
	a.recordStamp(path)
	return OpenedFile{Path: path, Content: string(data)}, nil
}

// recordStamp stores the current on-disk version of path so later saves can
// detect external modifications.
func (a *App) recordStamp(path string) {
	info, err := os.Stat(path)
	if err != nil {
		return
	}
	a.docDirMu.Lock()
	a.curFile = path
	a.curStamp = fileStamp{modTime: info.ModTime(), size: info.Size()}
	a.docDirMu.Unlock()
}

// SaveToPath writes content to an existing path without showing a dialog.
// If the file changed on disk since it was last read or saved, the user is
// asked first; canceling returns ErrOverwriteCanceled and nothing is written.
func (a *App) SaveToPath(path string, content string) error {
	if path == "" {
		return nil
	}
	a.docDirMu.RLock()
	tracked := a.curFile == path
	stamp := a.curStamp
	a.docDirMu.RUnlock()
	if tracked {
		if info, err := os.Stat(path); err == nil &&
			(info.ModTime() != stamp.modTime || info.Size() != stamp.size) {
			ok, err := a.ConfirmOverwrite()
			if err != nil {
				return err
			}
			if !ok {
				return ErrOverwriteCanceled
			}
		}
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return err
	}
	a.recordStamp(path)
	return nil
}

// ErrOverwriteCanceled marks a save canceled at the external-change warning.
var ErrOverwriteCanceled = errors.New("overwrite canceled")

// ConfirmOverwrite warns that the file on disk was changed externally and
// asks whether to overwrite it.
func (a *App) ConfirmOverwrite() (bool, error) {
	choice, err := runtime.MessageDialog(a.ctx, runtime.MessageDialogOptions{
		Type:          runtime.WarningDialog,
		Title:         a.tr("文件已被外部修改", "File Changed Externally"),
		Message:       a.tr("磁盘上的文件已被外部修改，覆盖将丢失那些改动。确定要覆盖吗？", "The file on disk was changed externally. Overwriting will lose those changes. Overwrite anyway?"),
		Buttons:       []string{a.tr("覆盖", "Overwrite"), a.tr("取消", "Cancel")},
		DefaultButton: a.tr("取消", "Cancel"),
		CancelButton:  a.tr("取消", "Cancel"),
	})
	if err != nil {
		return false, err
	}
	return choice == a.tr("覆盖", "Overwrite"), nil
}

// CheckExternalChange reports whether the current document changed on disk
// since it was last read or saved: "unchanged", "changed", or "missing".
func (a *App) CheckExternalChange() (string, error) {
	a.docDirMu.RLock()
	path, stamp := a.curFile, a.curStamp
	a.docDirMu.RUnlock()
	if path == "" {
		return "unchanged", nil
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "missing", nil
		}
		return "", err
	}
	if info.ModTime() != stamp.modTime || info.Size() != stamp.size {
		return "changed", nil
	}
	return "unchanged", nil
}

// TreeNode is a directory or Markdown file in the sidebar folder tree.
type TreeNode struct {
	Name     string     `json:"name"`
	Path     string     `json:"path"`
	IsDir    bool       `json:"isDir"`
	Children []TreeNode `json:"children,omitempty"`
}

// FolderTree is the scanned Markdown file tree of an opened folder.
// All fields are empty when the user cancels the dialog.
type FolderTree struct {
	Name     string     `json:"name"`
	Path     string     `json:"path"`
	Children []TreeNode `json:"children"`
}

// isMarkdownExt reports whether name has a Markdown extension.
func isMarkdownExt(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".md") || strings.HasSuffix(lower, ".markdown")
}

// scanDir builds the tree for dir: Markdown files plus subdirectories that
// contain at least one. Hidden entries and node_modules are skipped;
// directories sort before files, each by (case-insensitive) name.
func scanDir(dir string) ([]TreeNode, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var nodes []TreeNode
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") || name == "node_modules" {
			continue
		}
		path := filepath.Join(dir, name)
		if entry.IsDir() {
			children, err := scanDir(path)
			if err != nil || len(children) == 0 {
				continue
			}
			nodes = append(nodes, TreeNode{Name: name, Path: path, IsDir: true, Children: children})
		} else if isMarkdownExt(name) {
			nodes = append(nodes, TreeNode{Name: name, Path: path})
		}
	}
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].IsDir != nodes[j].IsDir {
			return nodes[i].IsDir
		}
		li, lj := strings.ToLower(nodes[i].Name), strings.ToLower(nodes[j].Name)
		if li != lj {
			return li < lj
		}
		return nodes[i].Name < nodes[j].Name
	})
	return nodes, nil
}

// OpenFolder shows a native directory dialog and scans the chosen folder for
// Markdown files. It returns an empty FolderTree when the user cancels.
func (a *App) OpenFolder() (FolderTree, error) {
	dir, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title: a.tr("打开文件夹", "Open Folder"),
	})
	if err != nil || dir == "" {
		return FolderTree{}, err
	}
	return a.scanFolder(dir)
}

// RefreshFolder re-scans an already opened folder (files may have changed
// externally).
func (a *App) RefreshFolder(path string) (FolderTree, error) {
	if path == "" {
		return FolderTree{}, nil
	}
	return a.scanFolder(path)
}

func (a *App) scanFolder(dir string) (FolderTree, error) {
	children, err := scanDir(dir)
	if err != nil {
		return FolderTree{}, err
	}
	a.docDirMu.Lock()
	a.rootDir = dir
	a.docDirMu.Unlock()
	return FolderTree{Name: filepath.Base(dir), Path: dir, Children: children}, nil
}

func (a *App) currentRoot() string {
	a.docDirMu.RLock()
	defer a.docDirMu.RUnlock()
	return a.rootDir
}

// dirInRoot returns dir cleaned, requiring it to be inside the currently
// opened folder so tree operations cannot escape it.
func (a *App) dirInRoot(dir string) (string, error) {
	root := a.currentRoot()
	if root == "" {
		return "", fmt.Errorf("no folder opened")
	}
	abs := filepath.Clean(dir)
	if abs != root && !strings.HasPrefix(abs, root+string(os.PathSeparator)) {
		return "", fmt.Errorf("path outside opened folder: %s", dir)
	}
	return abs, nil
}

// validateTreeName rejects empty names, "." / ".." and path separators.
func validateTreeName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		return "", fmt.Errorf("invalid name: %q", name)
	}
	if strings.ContainsAny(name, "/\\") {
		return "", fmt.Errorf("name must not contain path separators: %q", name)
	}
	return name, nil
}

// CreateFile creates an empty Markdown file in dir and returns its path.
// The .md extension is appended when missing; an existing file is an error.
func (a *App) CreateFile(dir, name string) (string, error) {
	name, err := validateTreeName(name)
	if err != nil {
		return "", err
	}
	if !isMarkdownExt(name) {
		name += ".md"
	}
	absDir, err := a.dirInRoot(dir)
	if err != nil {
		return "", err
	}
	path := filepath.Join(absDir, name)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return "", fmt.Errorf("同名文件已存在: %s", name)
		}
		return "", err
	}
	return path, f.Close()
}

// CreateDir creates a subdirectory in dir and returns its path.
func (a *App) CreateDir(dir, name string) (string, error) {
	name, err := validateTreeName(name)
	if err != nil {
		return "", err
	}
	absDir, err := a.dirInRoot(dir)
	if err != nil {
		return "", err
	}
	path := filepath.Join(absDir, name)
	if err := os.Mkdir(path, 0o755); err != nil {
		if os.IsExist(err) {
			return "", fmt.Errorf("同名文件夹已存在: %s", name)
		}
		return "", err
	}
	return path, nil
}

// RenamePath renames oldPath to newName within the same directory and
// returns the new path. The opened folder root itself cannot be renamed.
func (a *App) RenamePath(oldPath, newName string) (string, error) {
	newName, err := validateTreeName(newName)
	if err != nil {
		return "", err
	}
	abs, err := a.dirInRoot(oldPath)
	if err != nil {
		return "", err
	}
	if abs == a.currentRoot() {
		return "", fmt.Errorf("cannot rename the opened folder")
	}
	newPath := filepath.Join(filepath.Dir(abs), newName)
	if newPath == abs {
		return abs, nil
	}
	if _, err := os.Stat(newPath); err == nil {
		return "", fmt.Errorf("同名条目已存在: %s", newName)
	}
	if err := os.Rename(abs, newPath); err != nil {
		return "", err
	}
	return newPath, nil
}

// DeletePath deletes a file or an empty directory. Non-empty directories
// are refused, and the opened folder root cannot be deleted.
func (a *App) DeletePath(path string) error {
	abs, err := a.dirInRoot(path)
	if err != nil {
		return err
	}
	if abs == a.currentRoot() {
		return fmt.Errorf("cannot delete the opened folder")
	}
	info, err := os.Stat(abs)
	if err != nil {
		return err
	}
	if info.IsDir() {
		entries, err := os.ReadDir(abs)
		if err != nil {
			return err
		}
		if len(entries) > 0 {
			return fmt.Errorf("文件夹非空，无法删除: %s", info.Name())
		}
	}
	return os.Remove(abs)
}

// ConfirmRestoreBackup asks whether the unsaved content found in
// localStorage from a previous session should be restored.
func (a *App) ConfirmRestoreBackup() (bool, error) {
	choice, err := runtime.MessageDialog(a.ctx, runtime.MessageDialogOptions{
		Type:          runtime.QuestionDialog,
		Title:         a.tr("恢复未保存的内容", "Restore Unsaved Content"),
		Message:       a.tr("检测到上次退出前有未保存的修改，是否恢复？", "Unsaved changes from the previous session were detected. Restore them?"),
		Buttons:       []string{a.tr("恢复", "Restore"), a.tr("丢弃", "Discard")},
		DefaultButton: a.tr("恢复", "Restore"),
		CancelButton:  a.tr("丢弃", "Discard"),
	})
	if err != nil {
		return false, err
	}
	return choice == a.tr("恢复", "Restore"), nil
}

// ConfirmDelete shows a native dialog asking whether the entry may be
// deleted (window.confirm is not supported inside the WKWebView).
func (a *App) ConfirmDelete(name string, isDir bool) (bool, error) {
	kind := a.tr("文件", "file")
	if isDir {
		kind = a.tr("文件夹", "folder")
	}
	choice, err := runtime.MessageDialog(a.ctx, runtime.MessageDialogOptions{
		Type:          runtime.WarningDialog,
		Title:         a.tr("删除", "Delete ")+kind,
		Message:       fmt.Sprintf(a.tr("确定要删除%s“%s”吗？此操作不可撤销。", "Delete the %s “%s”? This cannot be undone."), kind, name),
		Buttons:       []string{a.tr("删除", "Delete"), a.tr("取消", "Cancel")},
		DefaultButton: a.tr("取消", "Cancel"),
		CancelButton:  a.tr("取消", "Cancel"),
	})
	if err != nil {
		return false, err
	}
	return choice == a.tr("删除", "Delete"), nil
}

// ReadFile returns the content of the file at path.
func (a *App) ReadFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	a.recordStamp(path)
	return string(data), nil
}

// ReadFileBase64 returns the base64-encoded content of the file at path,
// used to inline local images into exported HTML.
func (a *App) ReadFileBase64(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(data), nil
}

// imageExt picks a file extension by sniffing the image bytes (clipboard
// images are usually PNG; dragged files keep their real format).
func imageExt(data []byte) string {
	ct, _, _ := strings.Cut(http.DetectContentType(data), ";")
	switch ct {
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/bmp":
		return ".bmp"
	case "image/svg+xml":
		return ".svg"
	default:
		return ".png"
	}
}

// writeImageAsset writes data as a uniquely named file into the "assets"
// directory next to docPath (created if missing) and returns the path
// relative to the document, e.g. "assets/img-20260819-153045.png".
func writeImageAsset(docPath string, data []byte, ext string) (string, error) {
	if docPath == "" {
		return "", fmt.Errorf("document has no path; save it first")
	}
	assetsDir := filepath.Join(filepath.Dir(docPath), "assets")
	if err := os.MkdirAll(assetsDir, 0o755); err != nil {
		return "", err
	}
	stamp := time.Now().Format("20060102-150405")
	for i := 0; ; i++ {
		name := fmt.Sprintf("img-%s", stamp)
		if i > 0 {
			name = fmt.Sprintf("%s-%d", name, i)
		}
		name += ext
		abs := filepath.Join(assetsDir, name)
		if _, err := os.Stat(abs); err == nil {
			continue // same-second collision: bump the suffix
		}
		if err := os.WriteFile(abs, data, 0o644); err != nil {
			return "", err
		}
		return "assets/" + name, nil
	}
}

// SaveImage decodes base64Data and writes it as a uniquely named file into
// the "assets" directory next to docPath. It returns the path relative to
// the document, e.g. "assets/img-20260819-153045.png".
func (a *App) SaveImage(docPath, base64Data string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(base64Data)
	if err != nil {
		return "", err
	}
	return writeImageAsset(docPath, data, imageExt(data))
}

// SaveImageFile copies the image at srcPath into the assets directory next
// to docPath (used for files dropped onto the window, where the native drop
// callback provides absolute paths). It returns the document-relative path.
func (a *App) SaveImageFile(docPath, srcPath string) (string, error) {
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return "", err
	}
	ext := strings.ToLower(filepath.Ext(srcPath))
	if ext == "" || len(ext) > 6 {
		ext = imageExt(data)
	}
	return writeImageAsset(docPath, data, ext)
}

// ExportHTML shows a save dialog with an .html filter and writes content.
// It returns the saved path, or an empty string when the user cancels.
func (a *App) ExportHTML(defaultName, content string) (string, error) {
	path, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		Title:           a.tr("导出 HTML", "Export HTML"),
		DefaultFilename: defaultName,
		Filters: []runtime.FileFilter{
			{DisplayName: a.tr("HTML 文件 (*.html)", "HTML Files (*.html)"), Pattern: "*.html"},
		},
	})
	if err != nil || path == "" {
		return "", err
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// ResolvePath resolves a relative Markdown link against the directory of
// baseFile and returns the absolute path if the target exists and is a file.
// It handles URL-encoded characters (markdown-it percent-encodes non-ASCII
// hrefs, e.g. CJK file names and spaces as %20) and ".." segments.
func (a *App) ResolvePath(baseFile, rel string) (string, error) {
	decoded, err := url.PathUnescape(rel)
	if err != nil {
		return "", err
	}
	abs := filepath.Clean(filepath.Join(filepath.Dir(baseFile), decoded))
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("not a file: %s", abs)
	}
	return abs, nil
}

// SetCurrentFile records the directory of the currently open document so the
// asset middleware can resolve relative image paths against it. An empty path
// clears it (new unsaved document or file closed), along with the external
// change tracking of the previous file.
func (a *App) SetCurrentFile(path string) {
	a.docDirMu.Lock()
	defer a.docDirMu.Unlock()
	if path == "" {
		a.docDir = ""
		a.curFile = ""
		a.curStamp = fileStamp{}
		return
	}
	a.docDir = filepath.Dir(path)
}

func (a *App) currentDocDir() string {
	a.docDirMu.RLock()
	defer a.docDirMu.RUnlock()
	return a.docDir
}

// localFileMiddleware serves files under "/__local__/" from the current
// document's directory; all other requests fall through to the embedded
// frontend assets.
func (a *App) localFileMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/__local__/") {
			next.ServeHTTP(w, r)
			return
		}
		dir := a.currentDocDir()
		if dir == "" {
			http.NotFound(w, r)
			return
		}
		// r.URL.Path is already percent-decoded by net/http. Cleaning under a
		// virtual root keeps ".." segments from escaping the document dir.
		rel := filepath.Clean("/" + strings.TrimPrefix(r.URL.Path, "/__local__/"))
		abs := filepath.Join(dir, rel)
		if abs != dir && !strings.HasPrefix(abs, dir+string(os.PathSeparator)) {
			http.NotFound(w, r)
			return
		}
		f, err := os.Open(abs)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil || info.IsDir() {
			http.NotFound(w, r)
			return
		}
		// ServeContent sniffs Content-Type from the extension and sends
		// Last-Modified, so changed files are revalidated on reload.
		http.ServeContent(w, r, info.Name(), info.ModTime(), f)
	})
}

// ConfirmDiscard shows a native dialog asking whether unsaved changes may be
// discarded (window.confirm is not supported inside the WKWebView).
func (a *App) ConfirmDiscard() (bool, error) {
	choice, err := runtime.MessageDialog(a.ctx, runtime.MessageDialogOptions{
		Type:          runtime.WarningDialog,
		Title:         a.tr("未保存的修改", "Unsaved Changes"),
		Message:       a.tr("当前文档有未保存的修改，确定要放弃吗？", "The current document has unsaved changes. Discard them?"),
		Buttons:       []string{a.tr("放弃修改", "Discard"), a.tr("取消", "Cancel")},
		DefaultButton: a.tr("取消", "Cancel"),
		CancelButton:  a.tr("取消", "Cancel"),
	})
	if err != nil {
		return false, err
	}
	return choice == a.tr("放弃修改", "Discard"), nil
}

// SaveFile shows a macOS save dialog and writes content to the chosen path.
// It returns the saved path, or an empty string when the user cancels.
func (a *App) SaveFile(content string) (string, error) {
	path, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		Title:           a.tr("保存文件", "Save File"),
		DefaultFilename: "untitled.md",
		Filters: []runtime.FileFilter{
			{DisplayName: a.tr("Markdown 文件 (*.md)", "Markdown Files (*.md)"), Pattern: "*.md"},
		},
	})
	if err != nil || path == "" {
		return "", err
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	a.recordStamp(path)
	return path, nil
}

// ExportPDF shows a save dialog with a .pdf filter, then converts the given
// self-contained HTML to PDF at the chosen path. Conversion prefers a
// headless Chrome/Chromium/Edge installation, falling back to wkhtmltopdf,
// weasyprint or pandoc from PATH. It returns the saved path, or an empty
// string when the user cancels.
func (a *App) ExportPDF(defaultName, html string) (string, error) {
	path, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		Title:           a.tr("导出 PDF", "Export PDF"),
		DefaultFilename: defaultName,
		Filters: []runtime.FileFilter{
			{DisplayName: a.tr("PDF 文件 (*.pdf)", "PDF Files (*.pdf)"), Pattern: "*.pdf"},
		},
	})
	if err != nil || path == "" {
		return "", err
	}
	tool, err := findPDFTool()
	if err != nil {
		return "", errors.New(a.tr(
			"未找到 PDF 转换工具，请安装 Chrome，或先导出 HTML 后用浏览器打印为 PDF",
			"No PDF converter found. Install Chrome, or export HTML and print to PDF from a browser."))
	}
	tmp, err := os.CreateTemp("", "markup-export-*.html")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(html); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	htmlURL := (&url.URL{Scheme: "file", Path: tmp.Name()}).String()
	lower := strings.ToLower(filepath.Base(tool))
	var cmd *exec.Cmd
	switch {
	case strings.Contains(lower, "chrome") || strings.Contains(lower, "chromium") || strings.Contains(lower, "edge"):
		cmd = exec.CommandContext(ctx, tool, "--headless", "--disable-gpu", "--no-pdf-header-footer", "--print-to-pdf="+path, htmlURL)
	case strings.Contains(lower, "pandoc"):
		cmd = exec.CommandContext(ctx, tool, tmp.Name(), "-o", path)
	default: // wkhtmltopdf, weasyprint
		cmd = exec.CommandContext(ctx, tool, tmp.Name(), path)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("pdf conversion failed: %w: %s", err, out)
	}
	return path, nil
}

// findPDFTool locates an HTML-to-PDF converter: a browser in the usual macOS
// application paths first, then command-line tools from PATH.
func findPDFTool() (string, error) {
	for _, p := range []string{
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
		"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
	} {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	for _, name := range []string{"wkhtmltopdf", "weasyprint", "pandoc"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", errors.New("no pdf converter found")
}
