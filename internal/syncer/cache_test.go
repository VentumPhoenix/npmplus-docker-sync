package syncer

import (
	"fmt"
	"sync"
	"testing"

	"github.com/VentumPhoenix/npmplus-docker-sync/internal/npm"
)

func TestCacheIsPerKind(t *testing.T) {
	t.Parallel()

	c := NewCache()
	// The same key in two collections must not collide: a domain can be a
	// proxy host and a 404 host at the same time.
	c.Set(npm.KindProxy, "app.example.com", Entry{ID: 1, Hash: "proxy"})
	c.Set(npm.KindDead, "app.example.com", Entry{ID: 2, Hash: "dead"})

	proxy, ok := c.Get(npm.KindProxy, "app.example.com")
	if !ok || proxy.ID != 1 || proxy.Hash != "proxy" {
		t.Fatalf("proxy entry = %+v, %v", proxy, ok)
	}
	dead, ok := c.Get(npm.KindDead, "app.example.com")
	if !ok || dead.ID != 2 || dead.Hash != "dead" {
		t.Fatalf("404 entry = %+v, %v", dead, ok)
	}
	if _, ok := c.Get(npm.KindStream, "app.example.com"); ok {
		t.Error("stream bucket must be empty")
	}
	if c.Len() != 2 {
		t.Errorf("Len() = %d, want 2", c.Len())
	}
	if c.LenKind(npm.KindProxy) != 1 {
		t.Errorf("LenKind(proxy) = %d, want 1", c.LenKind(npm.KindProxy))
	}
}

func TestCacheBasics(t *testing.T) {
	t.Parallel()

	c := NewCache()
	if _, ok := c.Get(npm.KindProxy, "missing"); ok {
		t.Error("Get() on an empty cache returned an entry")
	}

	c.Set(npm.KindProxy, "a.example.com", Entry{ID: 1, Hash: "h1", Container: "web"})
	c.Set(npm.KindProxy, "a.example.com", Entry{ID: 1, Hash: "h2"})
	if entry, _ := c.Get(npm.KindProxy, "a.example.com"); entry.Hash != "h2" {
		t.Errorf("Set() did not overwrite the entry: %+v", entry)
	}

	c.Delete(npm.KindProxy, "a.example.com")
	if c.Len() != 0 {
		t.Errorf("Len() = %d after Delete(), want 0", c.Len())
	}
}

func TestCacheReplaceKindIsIsolated(t *testing.T) {
	t.Parallel()

	c := NewCache()
	c.Set(npm.KindProxy, "a.example.com", Entry{ID: 1})
	c.Set(npm.KindStream, "5432", Entry{ID: 2})

	source := map[string]Entry{"b.example.com": {ID: 3}}
	c.ReplaceKind(npm.KindProxy, source)

	source["c.example.com"] = Entry{ID: 4} // must not leak into the cache
	if c.LenKind(npm.KindProxy) != 1 {
		t.Errorf("LenKind(proxy) = %d, want the cache to hold its own copy", c.LenKind(npm.KindProxy))
	}
	if _, ok := c.Get(npm.KindProxy, "a.example.com"); ok {
		t.Error("ReplaceKind() must drop the previous entries of that kind")
	}
	if _, ok := c.Get(npm.KindStream, "5432"); !ok {
		t.Error("ReplaceKind() must not touch other kinds")
	}

	snapshot := c.Snapshot()
	snapshot[npm.KindProxy]["injected"] = Entry{ID: 9}
	if c.LenKind(npm.KindProxy) != 1 {
		t.Error("Snapshot() shares state with the cache")
	}
}

func TestCacheCounts(t *testing.T) {
	t.Parallel()

	c := NewCache()
	c.Set(npm.KindProxy, "a", Entry{})
	c.Set(npm.KindProxy, "b", Entry{})
	c.Set(npm.KindRedirect, "c", Entry{})

	counts := c.Counts()
	if counts[npm.KindProxy] != 2 || counts[npm.KindRedirect] != 1 || counts[npm.KindStream] != 0 {
		t.Errorf("Counts() = %v", counts)
	}
}

// TestCacheConcurrentAccess is meaningful under `go test -race`: the cache is
// read by the status probe while the worker writes to it.
func TestCacheConcurrentAccess(t *testing.T) {
	t.Parallel()

	c := NewCache()
	var wg sync.WaitGroup
	const workers = 8

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				kind := npm.Kinds[j%len(npm.Kinds)]
				key := fmt.Sprintf("host-%d.example.com", j%16)
				c.Set(kind, key, Entry{ID: i*1000 + j, Hash: key})
				_, _ = c.Get(kind, key)
				_ = c.Len()
				_ = c.Counts()
				if j%32 == 0 {
					_ = c.Snapshot()
					c.Delete(kind, key)
					c.ReplaceKind(kind, map[string]Entry{key: {ID: j}})
				}
			}
		}(i)
	}
	wg.Wait()
}
