// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

// Package trigger manages cron and webhook activation sources for WeaveTriggers.
package trigger

import (
	"sync"

	"github.com/robfig/cron/v3"
)

// CronScheduler wraps robfig/cron and maps trigger names to entry IDs so
// entries can be updated or removed when a WeaveTrigger spec changes.
type CronScheduler struct {
	mu      sync.Mutex
	c       *cron.Cron
	entries map[string]cron.EntryID // triggerKey -> entry ID
}

// NewCronScheduler creates and starts a new CronScheduler.
// Use Stop() to shut it down gracefully.
func NewCronScheduler() *CronScheduler {
	s := &CronScheduler{
		c:       cron.New(cron.WithSeconds()),
		entries: make(map[string]cron.EntryID),
	}
	s.c.Start()
	return s
}

// Upsert registers or replaces the cron job for the given trigger key.
// The callback is invoked on each schedule tick.
func (s *CronScheduler) Upsert(key, schedule string, fn func()) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if id, ok := s.entries[key]; ok {
		s.c.Remove(id)
	}

	id, err := s.c.AddFunc(schedule, fn)
	if err != nil {
		return err
	}
	s.entries[key] = id
	return nil
}

// Remove unregisters the cron job for the given trigger key.
func (s *CronScheduler) Remove(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if id, ok := s.entries[key]; ok {
		s.c.Remove(id)
		delete(s.entries, key)
	}
}

// Stop halts the underlying cron scheduler.
func (s *CronScheduler) Stop() {
	s.c.Stop()
}
