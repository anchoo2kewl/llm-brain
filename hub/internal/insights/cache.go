package insights

import (
	"context"
	"sync"
	"time"

	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

// Cache keeps each range's report for TTL. Builds are serialized (the store has one connection anyway), and
// a waiting caller gets the report the first one built.
type Cache struct {
	Store *store.Store
	TTL   time.Duration // default 60s
	Loc   *time.Location
	Now   func() time.Time

	mu      sync.Mutex
	build   sync.Mutex
	reports map[string]*Report
}

func NewCache(st *store.Store) *Cache { return &Cache{Store: st} }

func (c *Cache) cached(key string, ttl time.Duration) *Report {
	c.mu.Lock()
	defer c.mu.Unlock()
	if r := c.reports[key]; r != nil && time.Since(r.Generated) < ttl {
		return r
	}
	return nil
}

// Get returns the report for r, building it when the cached one is older than TTL.
func (c *Cache) Get(ctx context.Context, r Range) (*Report, error) {
	ttl := c.TTL
	if ttl <= 0 {
		ttl = 60 * time.Second
	}
	if rep := c.cached(r.Key, ttl); rep != nil {
		return rep, nil
	}
	c.build.Lock()
	defer c.build.Unlock()
	if rep := c.cached(r.Key, ttl); rep != nil {
		return rep, nil
	}
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	loc := c.Loc
	if loc == nil {
		loc = time.Local
	}
	rep, err := Build(ctx, c.Store, r, now(), loc)
	if err != nil {
		return nil, err
	}
	// Generated is the build's wall clock (for TTL), not the report's notion of now.
	rep.Generated = time.Now()
	c.mu.Lock()
	if c.reports == nil {
		c.reports = map[string]*Report{}
	}
	c.reports[r.Key] = rep
	c.mu.Unlock()
	return rep, nil
}

// Invalidate drops every cached report.
func (c *Cache) Invalidate() {
	c.mu.Lock()
	c.reports = nil
	c.mu.Unlock()
}
