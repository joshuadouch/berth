// Package plugin is Berth's plugin system. Plugins are compiled into the
// binary and switched on from the Plugins page. Each one declares its
// settings (Berth stores them, hides secrets and draws the form) and may serve
// its own API under /api/p/<id>/ for a page of its own in the UI.
//
// To add a plugin: implement Plugin in a package under internal/plugins, add
// it to plugins.All, and (for a page of its own) add a component to the
// pluginPages map in web/src/pages/Plugins.tsx.
package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/joshuadouch/berth/internal/model"
	"github.com/joshuadouch/berth/internal/store"
)

// Field types for plugin settings.
const (
	Text     = "text"
	Secret   = "secret" // never sent back to the browser; blank keeps the saved value
	Bool     = "bool"
	Number   = "number"
	Select   = "select"
	TextArea = "textarea"
)

// Option is one choice of a Select field.
type Option struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// Field is one plugin setting.
type Field struct {
	Key         string   `json:"key"`
	Label       string   `json:"label"`
	Type        string   `json:"type"`
	Section     string   `json:"section,omitempty"` // fields are grouped by section
	Help        string   `json:"help,omitempty"`
	Placeholder string   `json:"placeholder,omitempty"`
	Default     any      `json:"default,omitempty"`
	Options     []Option `json:"options,omitempty"`
	Required    bool     `json:"required,omitempty"`
}

// Info describes a plugin.
type Info struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Version     string  `json:"version"`
	Icon        string  `json:"icon"`      // UI icon name
	NavLabel    string  `json:"nav_label"` // sidebar entry while enabled ("" = none)
	Fields      []Field `json:"fields"`
}

// Plugin is a Berth plugin.
type Plugin interface {
	Info() Info
	// Init is called once at start-up. The handler it returns serves
	// /api/p/<id>/... (with that prefix removed) while the plugin is enabled.
	Init(h *Host) http.Handler
}

// Tester is implemented by plugins that can check their settings (the "Test"
// button on the Plugins page).
type Tester interface {
	Test(ctx context.Context, c Config) (string, error)
}

// Validator is implemented by plugins that check settings before saving.
type Validator interface {
	Validate(c Config) error
}

// Config holds a plugin's settings.
type Config map[string]any

// Str returns a setting as trimmed text.
func (c Config) Str(k string) string {
	switch v := c[k].(type) {
	case string:
		return strings.TrimSpace(v)
	case nil:
		return ""
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	default:
		return fmt.Sprint(v)
	}
}

// Bool returns a setting as a boolean.
func (c Config) Bool(k string) bool {
	switch v := c[k].(type) {
	case bool:
		return v
	case string:
		b, _ := strconv.ParseBool(v)
		return b
	case float64:
		return v != 0
	}
	return false
}

// Int returns a setting as a whole number.
func (c Config) Int(k string) int {
	switch v := c[k].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(v))
		return n
	}
	return 0
}

// State is what Berth stores for each plugin.
type State struct {
	Enabled bool   `json:"enabled"`
	Config  Config `json:"config"`
}

// Host is what a plugin gets from Berth.
type Host struct {
	ID    string
	Store *store.Store
	Log   *slog.Logger
	reg   *Registry
}

// Config returns the plugin's saved settings, defaults filled in.
func (h *Host) Config(ctx context.Context) (Config, error) {
	st, err := h.reg.state(ctx, h.ID)
	return st.Config, err
}

// Enabled reports whether the plugin is switched on.
func (h *Host) Enabled(ctx context.Context) bool { return h.reg.Enabled(ctx, h.ID) }

// Event records an entry in Berth's activity log.
func (h *Host) Event(ctx context.Context, level, msg string) {
	h.Store.AddEvent(ctx, level, "plugin."+h.ID, "", msg)
}

// GetData loads plugin data saved with SetData (a missing key leaves v alone).
func (h *Host) GetData(ctx context.Context, key string, v any) error {
	return h.Store.GetKV(ctx, "plugin:"+h.ID+":data:"+key, v)
}

// SetData saves plugin data.
func (h *Host) SetData(ctx context.Context, key string, v any) error {
	return h.Store.SetKV(ctx, "plugin:"+h.ID+":data:"+key, v)
}

// Sites lists Berth's sites (for plugins that offer to set things up for them).
func (h *Host) Sites(ctx context.Context) ([]model.Site, error) {
	return h.Store.Sites(ctx)
}

// Registry holds the compiled-in plugins.
type Registry struct {
	st       *store.Store
	plugins  []Plugin
	handlers map[string]http.Handler
	mu       sync.Mutex // serialises Save
}

// NewRegistry initialises plugins.
func NewRegistry(st *store.Store, log *slog.Logger, ps ...Plugin) *Registry {
	r := &Registry{st: st, handlers: map[string]http.Handler{}}
	if log == nil {
		log = slog.Default()
	}
	for _, p := range ps {
		id := p.Info().ID
		r.plugins = append(r.plugins, p)
		r.handlers[id] = p.Init(&Host{ID: id, Store: st, Log: log.With("plugin", id), reg: r})
	}
	sort.Slice(r.plugins, func(i, j int) bool { return r.plugins[i].Info().Name < r.plugins[j].Info().Name })
	return r
}

// ErrUnknown is returned for a plugin ID that does not exist.
var ErrUnknown = errors.New("no such plugin")

func (r *Registry) find(id string) (Plugin, error) {
	for _, p := range r.plugins {
		if p.Info().ID == id {
			return p, nil
		}
	}
	return nil, ErrUnknown
}

func (r *Registry) state(ctx context.Context, id string) (State, error) {
	p, err := r.find(id)
	if err != nil {
		return State{}, err
	}
	st := State{Config: Config{}}
	if err := r.st.GetKV(ctx, "plugin:"+id, &st); err != nil {
		return st, err
	}
	if st.Config == nil {
		st.Config = Config{}
	}
	for _, f := range p.Info().Fields {
		if _, ok := st.Config[f.Key]; !ok && f.Default != nil {
			st.Config[f.Key] = normalizeDefault(f.Default)
		}
	}
	return st, nil
}

// normalizeDefault makes Go defaults look like decoded JSON (ints as float64).
func normalizeDefault(v any) any {
	switch n := v.(type) {
	case int:
		return float64(n)
	case int64:
		return float64(n)
	}
	return v
}

// View is a plugin as the API shows it: secrets are left out.
type View struct {
	Info
	Enabled    bool            `json:"enabled"`
	Config     Config          `json:"config"`
	SecretsSet map[string]bool `json:"secrets_set"`
	CanTest    bool            `json:"can_test"`
}

// List returns every plugin with its settings (secrets hidden).
func (r *Registry) List(ctx context.Context) ([]View, error) {
	out := []View{}
	for _, p := range r.plugins {
		v, err := r.View(ctx, p.Info().ID)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// View returns one plugin with its settings (secrets hidden).
func (r *Registry) View(ctx context.Context, id string) (View, error) {
	p, err := r.find(id)
	if err != nil {
		return View{}, err
	}
	st, err := r.state(ctx, id)
	if err != nil {
		return View{}, err
	}
	_, canTest := p.(Tester)
	v := View{Info: p.Info(), Enabled: st.Enabled, Config: Config{}, SecretsSet: map[string]bool{}, CanTest: canTest}
	for _, f := range v.Fields {
		if f.Type == Secret {
			v.SecretsSet[f.Key] = st.Config.Str(f.Key) != ""
			continue
		}
		if val, ok := st.Config[f.Key]; ok {
			v.Config[f.Key] = val
		}
	}
	return v, nil
}

// merge applies submitted settings on top of the saved ones: unknown keys are
// dropped, a blank secret keeps the saved value and "-" clears it.
func (r *Registry) merge(ctx context.Context, id string, in Config) (Plugin, State, error) {
	p, err := r.find(id)
	if err != nil {
		return nil, State{}, err
	}
	cur, err := r.state(ctx, id)
	if err != nil {
		return nil, State{}, err
	}
	out := Config{}
	for _, f := range p.Info().Fields {
		val, given := in[f.Key]
		if !given {
			if v, ok := cur.Config[f.Key]; ok {
				out[f.Key] = v
			}
			continue
		}
		switch f.Type {
		case Secret:
			s := strings.TrimSpace(fmt.Sprint(val))
			if val == nil || s == "" {
				s = cur.Config.Str(f.Key)
			} else if s == "-" {
				s = ""
			}
			out[f.Key] = s
		case Bool:
			out[f.Key] = Config{"v": val}.Bool("v")
		case Number:
			out[f.Key] = float64(Config{"v": val}.Int("v"))
		case Select:
			s := Config{"v": val}.Str("v")
			ok := false
			for _, o := range f.Options {
				ok = ok || o.Value == s
			}
			if !ok {
				return nil, State{}, fmt.Errorf("%s: %q is not one of the choices", f.Label, s)
			}
			out[f.Key] = s
		default:
			out[f.Key] = Config{"v": val}.Str("v")
		}
	}
	cur.Config = out
	return p, cur, nil
}

// Save stores a plugin's settings and whether it is on. Turning a plugin on
// checks required settings.
func (r *Registry) Save(ctx context.Context, id string, enabled bool, in Config) (View, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, st, err := r.merge(ctx, id, in)
	if err != nil {
		return View{}, err
	}
	st.Enabled = enabled
	if enabled {
		for _, f := range p.Info().Fields {
			if f.Required && st.Config.Str(f.Key) == "" {
				return View{}, fmt.Errorf("%s is required", f.Label)
			}
		}
		if v, ok := p.(Validator); ok {
			if err := v.Validate(st.Config); err != nil {
				return View{}, err
			}
		}
	}
	if err := r.st.SetKV(ctx, "plugin:"+id, st); err != nil {
		return View{}, err
	}
	return r.View(ctx, id)
}

// Test runs a plugin's own check with the submitted settings (secrets left
// blank use the saved ones).
func (r *Registry) Test(ctx context.Context, id string, in Config) (string, error) {
	p, st, err := r.merge(ctx, id, in)
	if err != nil {
		return "", err
	}
	t, ok := p.(Tester)
	if !ok {
		return "", errors.New("this plugin has nothing to test")
	}
	return t.Test(ctx, st.Config)
}

// Enabled reports whether a plugin is switched on.
func (r *Registry) Enabled(ctx context.Context, id string) bool {
	st, err := r.state(ctx, id)
	return err == nil && st.Enabled
}

// ServeHTTP dispatches /api/p/{id}/... to the plugin's handler.
func (r *Registry) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	id := req.PathValue("id")
	h := r.handlers[id]
	if h == nil {
		WriteErr(w, 404, "no such plugin")
		return
	}
	if !r.Enabled(req.Context(), id) {
		WriteErr(w, 409, "the "+id+" plugin is turned off")
		return
	}
	http.StripPrefix("/api/p/"+id, h).ServeHTTP(w, req)
}

// WriteJSON writes a JSON response.
func WriteJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// WriteErr writes {"error": msg}.
func WriteErr(w http.ResponseWriter, code int, msg string) {
	WriteJSON(w, code, map[string]string{"error": msg})
}

// ReadJSON decodes a request body.
func ReadJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	if err := dec.Decode(v); err != nil {
		return errors.New("invalid JSON: " + err.Error())
	}
	return nil
}
