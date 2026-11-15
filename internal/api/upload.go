package api

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

func extFor(contentType string) string {
	switch contentType {
	case "image/png":
		return ".png"
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
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload)
	if err := r.ParseMultipartForm(maxUpload); err != nil {
		writeErr(w, 400, "too_large", "file is missing or larger than 8MB")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeErr(w, 400, "missing_file", "field 'file' is required")
		return
	}
	defer file.Close()

	head := make([]byte, 512)
	n, _ := io.ReadFull(file, head)
	contentType := http.DetectContentType(head[:n])
	ext := extFor(contentType)
	if ext == "" {
		writeErr(w, 415, "unsupported_type", "only png, jpeg, gif and webp are accepted")
		return
	}

	if err := os.MkdirAll(s.FilesDir, 0o755); err != nil {
		writeErr(w, 500, "storage_error", err.Error())
		return
	}
	name := store.NewID() + ext
	dst, err := os.Create(filepath.Join(s.FilesDir, name))
	if err != nil {
		writeErr(w, 500, "storage_error", err.Error())
		return
	}
	defer dst.Close()
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
	name := chi.URLParam(r, "name")
	if !fileNameRe.MatchString(name) {
		writeErr(w, 404, "not_found", "no such file")
		return
	}
	f, err := os.Open(filepath.Join(s.FilesDir, name))
	if err != nil {
		writeErr(w, 404, "not_found", "no such file")
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		writeErr(w, 404, "not_found", "no such file")
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeContent(w, r, name, info.ModTime(), f)
}
