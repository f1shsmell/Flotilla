package daemon

import (
	"context"
	"time"

	"github.com/f1shsmell/flotilla/internal/registry"
)

// registryRefreshTimeout bounds one fetch, so an endpoint that stalls is left
// for the next tick instead of holding the loop.
const registryRefreshTimeout = time.Minute

// registryLoop keeps the agent registry cache fresh until ctx ends: a stale or
// unreadable cache is refreshed at startup, then one fetch per period.
func (d *Daemon) registryLoop(ctx context.Context) {
	if !d.registryUsable() {
		d.refreshRegistry(ctx)
	}

	ticker := time.NewTicker(d.cfg.RegistryRefresh)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.refreshRegistry(ctx)
		}
	}
}

// registryUsable is the startup question: a cache that decodes and is younger
// than the TTL needs no fetch.
func (d *Daemon) registryUsable() bool {
	if d.cfg.CacheDir == "" {
		return true
	}
	return registry.Usable(registry.CachePath(d.cfg.CacheDir), time.Now())
}

// refreshRegistry replaces the cache. A failed fetch leaves the last copy in
// place, which is what an offline build keeps serving; a shutdown is not a
// failure, so it is not reported as one.
func (d *Daemon) refreshRegistry(ctx context.Context) {
	if d.cfg.CacheDir == "" {
		return
	}

	fetchCtx, cancel := context.WithTimeout(ctx, registryRefreshTimeout)
	defer cancel()

	url := d.cfg.RegistryURL
	if url == "" {
		url = registry.URL
	}
	answer, err := registry.Refresh(fetchCtx, d.cfg.RegistryClient, registry.CachePath(d.cfg.CacheDir), url)
	if err != nil {
		if ctx.Err() == nil {
			d.cfg.Log.Warn("cannot refresh the agent registry", "err", err.Error())
		}
		return
	}
	d.cfg.Log.Debug("agent registry refreshed", "version", answer.Version, "agents", len(answer.Agents))
}
