// Package plugins lists the plugins compiled into Berth.
package plugins

import (
	"github.com/joshuadouch/berth/internal/plugin"
	"github.com/joshuadouch/berth/internal/plugins/mailcow"
)

// All returns a fresh instance of every plugin.
func All() []plugin.Plugin {
	return []plugin.Plugin{mailcow.New()}
}
