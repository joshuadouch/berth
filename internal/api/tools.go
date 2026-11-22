package api

import (
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/joshuadouch/berth/internal/engine"
)

// File manager -----------------------------------------------------------------

func fileErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		writeErr(w, 404, "not found")
	case errors.Is(err, fs.ErrExist):
		writeErr(w, 409, "that name is taken")
	default:
		msg := err.Error()
		// Hide absolute paths from os.Root errors.
		var pe *fs.PathError
		if errors.As(err, &pe) {
			msg = pe.Op + " " + pe.Path + ": " + pe.Err.Error()
		}
		writeErr(w, 400, msg)
	}
}

func (s *Server) listFiles(w http.ResponseWriter, r *http.Request) {
	site, ok := s.site(w, r)
	if !ok {
		return
	}
	ents, err := s.Engine.ListFiles(site, r.URL.Query().Get("path"))
	if err != nil {
		fileErr(w, err)
		return
	}
	writeJSON(w, 200, ents)
}

func (s *Server) readFile(w http.ResponseWriter, r *http.Request) {
	site, ok := s.site(w, r)
	if !ok {
		return
	}
	text, err := s.Engine.ReadTextFile(site, r.URL.Query().Get("path"))
	if err != nil {
		fileErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"content": text})
}

func (s *Server) downloadFile(w http.ResponseWriter, r *http.Request) {
	site, ok := s.site(w, r)
	if !ok {
		return
	}
	f, st, err := s.Engine.OpenFile(site, r.URL.Query().Get("path"))
	if err != nil {
		fileErr(w, err)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", `attachment; filename="`+strings.ReplaceAll(st.Name(), `"`, "")+`"`)
	http.ServeContent(w, r, st.Name(), st.ModTime(), f)
}

func (s *Server) fileAction(w http.ResponseWriter, r *http.Request) {
	site, ok := s.site(w, r)
	if !ok {
		return
	}
	var req struct {
		Action  string   `json:"action"` // save, mkdir, rename, delete, chmod, unzip
		Path    string   `json:"path"`
		To      string   `json:"to"`
		Paths   []string `json:"paths"`
		Content string   `json:"content"`
		Mode    string   `json:"mode"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	var err error
	out := map[string]any{"ok": true}
	switch req.Action {
	case "save":
		err = s.Engine.WriteTextFile(site, req.Path, req.Content)
	case "mkdir":
		err = s.Engine.Mkdir(site, req.Path)
	case "rename":
		err = s.Engine.RenameFile(site, req.Path, req.To)
	case "delete":
		err = s.Engine.DeleteFiles(site, req.Paths)
		if err == nil {
			s.Store.AddEvent(r.Context(), "warn", "files", site.Name, currentUser(r).Username+" deleted "+strings.Join(req.Paths, ", "))
		}
	case "chmod":
		var m uint64
		m, err = strconv.ParseUint(req.Mode, 8, 32)
		if err != nil {
			err = errors.New("mode should look like 644 or 755")
		} else {
			err = s.Engine.ChmodFile(site, req.Path, os.FileMode(m))
		}
	case "unzip":
		var n int
		n, err = s.Engine.ExtractZip(site, req.Path)
		out["files"] = n
	default:
		err = errors.New("unknown action")
	}
	if err != nil {
		fileErr(w, err)
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) uploadFiles(w http.ResponseWriter, r *http.Request) {
	site, ok := s.site(w, r)
	if !ok {
		return
	}
	dir := r.URL.Query().Get("path")
	if err := s.Engine.OverQuota(r.Context(), site.AccountID); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	longUpload(w)
	r.Body = http.MaxBytesReader(w, r.Body, 4<<30)
	mr, err := r.MultipartReader()
	if err != nil {
		writeErr(w, 400, "expected a file upload")
		return
	}
	n := 0
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		if part.FileName() == "" {
			continue
		}
		// Folder uploads send "folder/file" as the name.
		name := strings.ReplaceAll(part.FileName(), "\\", "/")
		sub := path.Dir(name)
		target := dir
		if sub != "." {
			target = path.Join(dir, sub)
			if err := s.Engine.MkdirAll(site, target); err != nil {
				fileErr(w, err)
				return
			}
		}
		if err := s.Engine.SaveUpload(site, target, path.Base(name), part); err != nil {
			fileErr(w, err)
			return
		}
		n++
	}
	writeJSON(w, 200, map[string]any{"ok": true, "files": n})
}

// Database editor --------------------------------------------------------------

func (s *Server) dbTables(w http.ResponseWriter, r *http.Request) {
	site, ok := s.site(w, r)
	if !ok {
		return
	}
	ts, err := s.Engine.DBTables(r.Context(), site)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, ts)
}

func (s *Server) dbBrowse(w http.ResponseWriter, r *http.Request) {
	site, ok := s.site(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	size, _ := strconv.Atoi(q.Get("size"))
	rows, err := s.Engine.DBBrowse(r.Context(), site, r.PathValue("table"), engine.DBQuery{
		Page: page, Size: size, Sort: q.Get("sort"), Desc: q.Get("desc") == "1", Search: q.Get("q")})
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, rows)
}

func (s *Server) dbChange(op string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		site, ok := s.site(w, r)
		if !ok {
			return
		}
		var ch engine.DBRowChange
		if err := readJSON(r, &ch); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		n, err := s.Engine.DBChange(r.Context(), site, r.PathValue("table"), op, ch)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"affected": n})
	}
}

func (s *Server) dbSQL(w http.ResponseWriter, r *http.Request) {
	site, ok := s.site(w, r)
	if !ok {
		return
	}
	var req struct {
		SQL string `json:"sql"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	res, err := s.Engine.DBRunSQL(r.Context(), site, req.SQL)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, res)
}

// Site imports -----------------------------------------------------------------

func (s *Server) uploadImport(w http.ResponseWriter, r *http.Request) {
	if err := s.Engine.OverQuota(r.Context(), currentUser(r).AccountID); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	longUpload(w)
	r.Body = http.MaxBytesReader(w, r.Body, 20<<30)
	mr, err := r.MultipartReader()
	if err != nil {
		writeErr(w, 400, "expected a file upload")
		return
	}
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		if part.FileName() == "" {
			continue
		}
		info, err := s.Engine.SaveImport(part.FileName(), part)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		writeJSON(w, 200, info)
		return
	}
	writeErr(w, 400, "no file in the upload")
}

func (s *Server) deleteImport(w http.ResponseWriter, r *http.Request) {
	if err := s.Engine.DeleteImport(r.PathValue("id")); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// longUpload lifts the server's short read timeout for big uploads.
func longUpload(w http.ResponseWriter) {
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(6 * time.Hour))
	_ = rc.SetWriteDeadline(time.Now().Add(6 * time.Hour))
}
