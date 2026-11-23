package api

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

// Admins can replace Berth's logo (the icon and name in the sidebar and on
// the sign-in page) and the browser tab's favicon with their own images.

const brandMaxBytes = 1 << 20

var brandTypes = map[string]string{
	"png": "image/png", "jpg": "image/jpeg", "gif": "image/gif", "webp": "image/webp",
	"svg": "image/svg+xml", "ico": "image/x-icon",
}

// brandKinds lists what each image may be.
var brandKinds = map[string][]string{
	"logo":    {"png", "jpg", "gif", "webp", "svg"},
	"favicon": {"png", "ico", "svg", "gif"},
}

// sniffImage tells which image format the bytes are ("" for none we take).
func sniffImage(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return "png"
	case bytes.HasPrefix(b, []byte("\xff\xd8\xff")):
		return "jpg"
	case bytes.HasPrefix(b, []byte("GIF87a")), bytes.HasPrefix(b, []byte("GIF89a")):
		return "gif"
	case len(b) > 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return "webp"
	case bytes.HasPrefix(b, []byte{0, 0, 1, 0}):
		return "ico"
	}
	head := b
	if len(head) > 2048 {
		head = head[:2048]
	}
	if utf8.Valid(b) && bytes.Contains(bytes.ToLower(head), []byte("<svg")) {
		return "svg"
	}
	return ""
}

func (s *Server) brandDir() string { return filepath.Join(s.Cfg.DataDir, "brand") }

// brandAsset serves the logo or favicon. Anyone may fetch them: the sign-in
// page shows them.
func (s *Server) brandAsset(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	st, _ := s.Store.Settings(r.Context())
	name := map[string]string{"logo": st.Brand.Logo, "favicon": st.Brand.Favicon}[kind]
	if name == "" || strings.ContainsAny(name, `/\`) {
		writeErr(w, 404, "not set")
		return
	}
	b, err := os.ReadFile(filepath.Join(s.brandDir(), name))
	if err != nil {
		writeErr(w, 404, "not set")
		return
	}
	ext := strings.TrimPrefix(filepath.Ext(name), ".")
	w.Header().Set("Content-Type", brandTypes[ext])
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// An SVG opened on its own must not run scripts on the panel's origin.
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src data:; sandbox")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write(b)
}

func (s *Server) putBrandAsset(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	allowed, ok := brandKinds[kind]
	if !ok {
		writeErr(w, 404, "kind must be logo or favicon")
		return
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, brandMaxBytes+1))
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if len(b) > brandMaxBytes {
		writeErr(w, 400, "the image can be at most 1 MB")
		return
	}
	ext := sniffImage(b)
	okType := false
	for _, a := range allowed {
		okType = okType || a == ext
	}
	if !okType {
		writeErr(w, 400, fmt.Sprintf("the %s must be one of: %s", kind, strings.Join(allowed, ", ")))
		return
	}
	if err := os.MkdirAll(s.brandDir(), 0o755); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	name := fmt.Sprintf("%s-%d.%s", kind, time.Now().UnixNano(), ext)
	if err := os.WriteFile(filepath.Join(s.brandDir(), name), b, 0o644); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if err := s.setBrandAsset(r, kind, name); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{kind: name})
}

func (s *Server) deleteBrandAsset(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	if _, ok := brandKinds[kind]; !ok {
		writeErr(w, 404, "kind must be logo or favicon")
		return
	}
	if err := s.setBrandAsset(r, kind, ""); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// setBrandAsset records the new file and removes the old one.
func (s *Server) setBrandAsset(r *http.Request, kind, name string) error {
	st, err := s.Store.Settings(r.Context())
	if err != nil {
		return err
	}
	old := &st.Brand.Logo
	if kind == "favicon" {
		old = &st.Brand.Favicon
	}
	prev := *old
	*old = name
	if err := s.Store.SetSettings(r.Context(), st); err != nil {
		return err
	}
	if prev != "" && !strings.ContainsAny(prev, `/\`) {
		_ = os.Remove(filepath.Join(s.brandDir(), prev))
	}
	verb := "changed"
	if name == "" {
		verb = "removed"
	}
	s.Store.AddEvent(r.Context(), "info", "settings", "", currentUser(r).Username+" "+verb+" the "+kind)
	return nil
}
