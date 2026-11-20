package engine

import (
	"context"
	"errors"

	"github.com/joshuadouch/berth/internal/model"
)

// syncHalyard creates or updates the site's host on Halyard when the
// integration is set up. Failures are recorded on the site, never fatal.
func (e *Engine) syncHalyard(ctx context.Context, s *model.Site, logf Logf) {}

// unlinkHalyard removes the site's host from Halyard.
func (e *Engine) unlinkHalyard(ctx context.Context, s *model.Site, logf Logf) {}

// HalyardInfo is shown on the Settings and site pages.
type HalyardInfo struct {
	Configured bool   `json:"configured"`
	Error      string `json:"error,omitempty"`
}

// HalyardInfo reports the integration's state.
func (e *Engine) HalyardInfo(ctx context.Context) HalyardInfo { return HalyardInfo{} }

// LinkHalyard publishes a site on Halyard.
func (e *Engine) LinkHalyard(ctx context.Context, name string, l model.HalyardLink) (model.Site, error) {
	return model.Site{}, errNotYet
}

// UnlinkHalyard removes a site from Halyard.
func (e *Engine) UnlinkHalyard(ctx context.Context, name string) (model.Site, error) {
	return model.Site{}, errNotYet
}

var errNotYet = errors.New("Halyard integration is not available yet")
