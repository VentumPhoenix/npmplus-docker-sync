package syncer

import (
	"sync"

	"github.com/VentumPhoenix/npmplus-docker-sync/internal/npm"
)

// Entry is the cached state of one managed NPM resource.
type Entry struct {
	// ID is the id NPM assigned to the resource.
	ID int
	// Hash fingerprints the configuration last written to NPM.
	Hash string
	// Container is the container the resource was derived from.
	Container string
	// Index is the label index inside that container.
	Index int
	// Enabled is the enabled state last observed for the resource. It is
	// tracked separately from Hash because it is not part of the write
	// payload: both APIs toggle it through /enable and /disable.
	Enabled bool
	// Certificate is the certificate id attached to the resource, reported by
	// the /status endpoint.
	Certificate int
	// Running mirrors the container state at the time of the last run.
	Running bool
}

// Cache is the in-memory state cache guarding the API against redundant
// writes. Fingerprints are tracked per resource kind, so proxy hosts,
// redirections, streams and 404 hosts never collide even when they share a
// domain name.
//
// It is read by the reconcile loop and the readiness probe while the worker
// writes to it, hence the explicit RWMutex.
type Cache struct {
	mu      sync.RWMutex
	entries map[npm.Kind]map[string]Entry
}

// NewCache returns an empty cache with one bucket per resource kind.
func NewCache() *Cache {
	entries := make(map[npm.Kind]map[string]Entry, len(npm.Kinds))
	for _, kind := range npm.Kinds {
		entries[kind] = make(map[string]Entry)
	}
	return &Cache{entries: entries}
}

// Get returns the cached entry for a resource.
func (c *Cache) Get(kind npm.Kind, key string) (Entry, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, ok := c.entries[kind][key]
	return entry, ok
}

// Set stores or replaces a single entry.
func (c *Cache) Set(kind npm.Kind, key string, entry Entry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.bucket(kind)[key] = entry
}

// Delete removes a single entry.
func (c *Cache) Delete(kind npm.Kind, key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries[kind], key)
}

// Len returns the total number of cached resources across all kinds.
func (c *Cache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	total := 0
	for _, bucket := range c.entries {
		total += len(bucket)
	}
	return total
}

// LenKind returns the number of cached resources of one kind.
func (c *Cache) LenKind(kind npm.Kind) int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries[kind])
}

// Counts returns the number of managed resources per kind.
func (c *Cache) Counts() map[npm.Kind]int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[npm.Kind]int, len(c.entries))
	for kind, bucket := range c.entries {
		out[kind] = len(bucket)
	}
	return out
}

// Snapshot returns a deep copy of the cache contents.
func (c *Cache) Snapshot() map[npm.Kind]map[string]Entry {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[npm.Kind]map[string]Entry, len(c.entries))
	for kind, bucket := range c.entries {
		clone := make(map[string]Entry, len(bucket))
		for key, entry := range bucket {
			clone[key] = entry
		}
		out[kind] = clone
	}
	return out
}

// ReplaceKind swaps the contents of a single kind atomically. Other kinds are
// left untouched, so a failing collection does not discard known state.
func (c *Cache) ReplaceKind(kind npm.Kind, entries map[string]Entry) {
	next := make(map[string]Entry, len(entries))
	for key, entry := range entries {
		next[key] = entry
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[kind] = next
}

// bucket returns the map for a kind, creating it on demand. Callers must hold
// the write lock.
func (c *Cache) bucket(kind npm.Kind) map[string]Entry {
	if c.entries == nil {
		c.entries = make(map[npm.Kind]map[string]Entry, len(npm.Kinds))
	}
	if c.entries[kind] == nil {
		c.entries[kind] = make(map[string]Entry)
	}
	return c.entries[kind]
}
