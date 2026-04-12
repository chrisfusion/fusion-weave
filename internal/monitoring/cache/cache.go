// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

// Package cache provides a generic, goroutine-safe in-memory TTL cache.
package cache

import (
	"sync"
	"time"
)

type entry[V any] struct {
	value     V
	expiresAt time.Time
}

// TTLCache is a generic in-memory cache with per-entry TTL expiry.
// Expired entries are lazily evicted on Get. Goroutine-safe.
type TTLCache[K comparable, V any] struct {
	mu    sync.RWMutex
	items map[K]entry[V]
	ttl   time.Duration
}

// New creates an empty TTLCache with the given TTL applied to all Set calls.
func New[K comparable, V any](ttl time.Duration) *TTLCache[K, V] {
	return &TTLCache[K, V]{
		items: make(map[K]entry[V]),
		ttl:   ttl,
	}
}

// Get returns the cached value for key and true, or the zero value and false
// if the entry is absent or expired. Expired entries are lazily removed.
func (c *TTLCache[K, V]) Get(key K) (V, bool) {
	// Fast path: read-lock.
	c.mu.RLock()
	e, ok := c.items[key]
	c.mu.RUnlock()

	if !ok {
		var zero V
		return zero, false
	}
	if time.Now().After(e.expiresAt) {
		// Lazily evict the expired entry under write-lock.
		c.mu.Lock()
		if e2, ok2 := c.items[key]; ok2 && time.Now().After(e2.expiresAt) {
			delete(c.items, key)
		}
		c.mu.Unlock()
		var zero V
		return zero, false
	}
	return e.value, true
}

// Set stores value under key, overwriting any existing entry.
func (c *TTLCache[K, V]) Set(key K, value V) {
	c.mu.Lock()
	c.items[key] = entry[V]{value: value, expiresAt: time.Now().Add(c.ttl)}
	c.mu.Unlock()
}

// Delete removes key from the cache. No-op if the key does not exist.
func (c *TTLCache[K, V]) Delete(key K) {
	c.mu.Lock()
	delete(c.items, key)
	c.mu.Unlock()
}
