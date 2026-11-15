package api

// ── 阅读导览 · 旁路章（上传）─────────────────────────────
// 主线见 docs/READING.md；这一支不参与消息的一生，但消息会指向它的产物。

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"

	"github.com/go-chi/chi/v5"

	"imchat/internal/store"
)

const maxUpload = 8 << 20

var fileNameRe = regexp.MustCompile(`^[a-f0-9]{32}\.(png|jpg|jpeg|gif|webp)$`)

// extFor is the allow-list: a content type maps to an extension or to nothing.
// Returning "" is how "rejected" is expressed, so callers have one check.
func extFor(contentType string) string {
	switch contentType {
	case "image/png":
		return ".png"
	// 四种格式各给一个规范扩展名；同一类型永远落成同一个后缀，方便后面按名取。
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	}
	return ""
}

// handleUpload stores the bytes on disk and returns an opaque name. The message
// row only ever carries that name, so the database stays small and text-shaped.
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if _, ok := r.Context().Value(userKey).(*store.User); !ok {
		writeErr(w, 401, "unauthorized", "login required")
		return
	}
	// 先给请求体套一个硬上限：超过 8MB 的连接会被直接掐断，不会先读完再拒绝。
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload)
	if err := r.ParseMultipartForm(maxUpload); err != nil {
		writeErr(w, 400, "too_large", "file is missing or larger than 8MB")
		return
	}
	// 取表单里的 file 字段；缺了就是 400，不是 500——这是调用方的问题。
	file, header, err := r.FormFile("file")
	if err != nil {
		writeErr(w, 400, "missing_file", "field 'file' is required")
		return
	}
	defer file.Close()

	// 认文件类型靠内容而不是扩展名：前 512 字节足以让标准库判断真实格式，
	// 客户端说是 png 不算数。
	head := make([]byte, 512)
	n, _ := io.ReadFull(file, head)
	contentType := http.DetectContentType(head[:n])
	ext := extFor(contentType)
	// 白名单之外的类型直接 415：不猜、不转换、不落盘。
	if ext == "" {
		writeErr(w, 415, "unsupported_type", "only png, jpeg, gif and webp are accepted")
		return
	}

	if err := os.MkdirAll(s.FilesDir, 0o755); err != nil {
		writeErr(w, 500, "storage_error", err.Error())
		return
	}
	// 落盘名是自己生成的随机名 + 白名单扩展名，客户端给的文件名只回显、不入路径。
	name := store.NewID() + ext
	// 目录首次上传时才建；文件名是自己生成的，天然不会重复。
	dst, err := os.Create(filepath.Join(s.FilesDir, name))
	if err != nil {
		writeErr(w, 500, "storage_error", err.Error())
		return
	}
	defer dst.Close()
	// 前面已经读掉的那 512 字节要拼回去，否则文件头会丢。
	written, err := io.Copy(dst, io.MultiReader(bytes.NewReader(head[:n]), file))
	if err != nil {
		writeErr(w, 500, "storage_error", err.Error())
		return
	}

	writeJSON(w, 201, map[string]any{
		"id": name, "url": "/files/" + name, "bytes": written,
		"original": header.Filename, "content_type": contentType,
	})
}

// handleFile serves an uploaded blob. The name is validated against a strict
// pattern first, so no traversal is possible even though the path is joined.
func (s *Server) handleFile(w http.ResponseWriter, r *http.Request) {
	// 名字必须先过严格白名单：32 位十六进制 + 扩展名。
	// 过了这一关，后面的 filepath.Join 才不可能被 ../ 穿透。
	name := chi.URLParam(r, "name")
	if !fileNameRe.MatchString(name) {
		writeErr(w, 404, "not_found", "no such file")
		return
	}
	// 名字已经过了白名单，所以这里的 Join 不可能被 ../ 穿透。
	f, err := os.Open(filepath.Join(s.FilesDir, name))
	if err != nil {
		writeErr(w, 404, "not_found", "no such file")
		return
	}
	defer f.Close()
	// ServeContent 需要修改时间与长度来做条件请求与 Range。
	info, err := f.Stat()
	if err != nil {
		writeErr(w, 404, "not_found", "no such file")
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeContent(w, r, name, info.ModTime(), f)
}
