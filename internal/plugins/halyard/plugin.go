// Package halyard is the Halyard plugin. It switches publishing on Halyard,
// the load balancer, on for chosen accounts. The connection itself (URL,
// token, pool and defaults) is set on the plugin's page and kept with the
// panel settings; the engine does the publishing.
package halyard

import (
	"net/http"

	"github.com/joshuadouch/berth/internal/plugin"
)

// ID is the plugin's ID.
const ID = "halyard"

// Plugin is the Halyard plugin.
type Plugin struct{}

// New returns the plugin.
func New() *Plugin { return &Plugin{} }

// Info implements plugin.Plugin.
func (p *Plugin) Info() plugin.Info {
	return plugin.Info{
		ID:          ID,
		Name:        "Halyard",
		Description: "Publish sites on your Halyard load balancer: a host per site on a VIP, certificates, and Cloudflare DNS through Halyard. For your own accounts only: it is never offered to hosting customers.",
		Version:     "1.0",
		Icon:        "pool",
		NavLabel:    "Halyard",
		PerAccount:  true,
		AdminOnly:   true,
	}
}

// Init implements plugin.Plugin. Halyard's API lives under /api/halyard.
func (p *Plugin) Init(h *plugin.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { plugin.WriteErr(w, 404, "no such endpoint") })
}
