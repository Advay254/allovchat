package main

import (
	"strings"
	"sync"
	"time"
)

type cacheItem struct {
	v   any
	exp time.Time
}

// Cache is a tiny in-memory TTL cache for hot, read-mostly lookups (sessions, public link info, stats).
// Anything that must be exact, like a write or a security decision on a sensitive action, goes to the database instead.
type Cache struct {
	mu sync.Mutex
	m  map[string]cacheItem
}

func newCache() *Cache { return &Cache{m: map[string]cacheItem{}} }

func (c *Cache) get(k string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	it, ok := c.m[k]
	if !ok || time.Now().After(it.exp) {
		delete(c.m, k)
		return nil, false
	}
	return it.v, true
}

func (c *Cache) set(k string, v any, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) > 5000 {
		now := time.Now()
		for kk, it := range c.m {
			if now.After(it.exp) {
				delete(c.m, kk)
			}
		}
		if len(c.m) > 5000 {
			c.m = map[string]cacheItem{}
		}
	}
	c.m[k] = cacheItem{v, time.Now().Add(ttl)}
}

func (c *Cache) del(k string) {
	c.mu.Lock()
	delete(c.m, k)
	c.mu.Unlock()
}

func (c *Cache) delPrefix(p string) {
	c.mu.Lock()
	for k := range c.m {
		if strings.HasPrefix(k, p) {
			delete(c.m, k)
		}
	}
	c.mu.Unlock()
}
