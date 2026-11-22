package syncer

import (
	"log/slog"

	"github.com/VentumPhoenix/npmplus-docker-sync/internal/docker"
	"github.com/VentumPhoenix/npmplus-docker-sync/internal/npm"
)

// withNginx records in a cache entry what the server last said about the nginx
// configuration of the resource. A disabled resource is never offline: NPMplus
// neither renders nor serves it, whatever it reported before.
func withNginx(entry Entry, r npm.Resource) Entry {
	if r == nil || !entry.Enabled {
		return entry
	}
	online, message, reported := npm.NginxStatus(r)
	if reported && !online {
		entry.Offline, entry.NginxError = true, message
	}
	return entry
}

// reportOffline logs a write NPMplus accepted but nginx refused: the host then
// exists, carries the configuration the labels asked for, and is not served.
func (w *Worker) reportOffline(log *slog.Logger, t *docker.Target, key string, written npm.Resource, enabled bool) {
	if written == nil || !enabled {
		return
	}
	online, message, reported := npm.NginxStatus(written)
	if !reported || online {
		return
	}
	log.Error("npm stored the host but nginx refused its configuration, so it is offline",
		slog.String("key", key), slog.Int("id", written.ResourceID()),
		slog.String("container", t.ContainerName), slog.String("nginx_error", message))
}

// offlineAttempt reports whether an offline resource has not yet been
// rewritten under this auth status, and records that it now is.
func (w *Worker) offlineAttempt(kind npm.Kind, key, state string) bool {
	w.refMu.Lock()
	defer w.refMu.Unlock()
	id := backoffKey(kind, key)
	if tried, ok := w.offlineTried[id]; ok && tried == state {
		return false
	}
	w.offlineTried[id] = state
	return true
}

// clearOffline forgets the rewrite of a resource that is online again.
func (w *Worker) clearOffline(kind npm.Kind, key string) {
	w.refMu.Lock()
	defer w.refMu.Unlock()
	delete(w.offlineTried, backoffKey(kind, key))
}
