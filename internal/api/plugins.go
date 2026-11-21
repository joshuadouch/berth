package api

import (
	"errors"
	"net/http"

	"github.com/joshuadouch/berth/internal/plugin"
)

func pluginErr(w http.ResponseWriter, err error) {
	if errors.Is(err, plugin.ErrUnknown) {
		writeErr(w, 404, err.Error())
		return
	}
	writeErr(w, 400, err.Error())
}

func (s *Server) listPlugins(w http.ResponseWriter, r *http.Request) {
	list, err := s.Plugins.List(r.Context())
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, list)
}

func (s *Server) savePlugin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool          `json:"enabled"`
		Config  plugin.Config `json:"config"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	id := r.PathValue("id")
	before := s.Plugins.Enabled(r.Context(), id)
	v, err := s.Plugins.Save(r.Context(), id, req.Enabled, req.Config)
	if err != nil {
		pluginErr(w, err)
		return
	}
	switch {
	case req.Enabled && !before:
		s.Store.AddEvent(r.Context(), "info", "plugin", "", "Turned on the "+v.Name+" plugin")
	case !req.Enabled && before:
		s.Store.AddEvent(r.Context(), "info", "plugin", "", "Turned off the "+v.Name+" plugin")
	default:
		s.Store.AddEvent(r.Context(), "info", "plugin", "", "Changed the "+v.Name+" plugin settings")
	}
	writeJSON(w, 200, v)
}

func (s *Server) testPlugin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Config plugin.Config `json:"config"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	msg, err := s.Plugins.Test(r.Context(), r.PathValue("id"), req.Config)
	if err != nil {
		pluginErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"message": msg})
}
