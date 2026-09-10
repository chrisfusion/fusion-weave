// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

// Package trigger manages cron and webhook activation sources for WeaveTriggers.
package trigger

import (
	"fmt"
	"sync"

	"github.com/robfig/cron/v3"
)

// CronScheduler wraps robfig/cron and maps trigger names to entry IDs so
// entries can be updated or removed when a WeaveTrigger spec changes.
type CronScheduler struct {
	mu      sync.Mutex
	c       *cron.Cron
	entries map[string]cron.EntryID // triggerKey -> entry ID
	panicCh chan<- TriggerPanic
}

// NewCronScheduler creates and starts a new CronScheduler. A panic recovered from
// a trigger's own schedule callback is reported on panicCh (non-blocking) and that
// trigger's cron entry is removed so it does not fire again until re-Upserted.
// Use Stop() to shut it down gracefully.
func NewCronScheduler(panicCh chan<- TriggerPanic) *CronScheduler {
	s := &CronScheduler{
		c:       cron.New(cron.WithSeconds()),
		entries: make(map[string]cron.EntryID),
		panicCh: panicCh,
	}
	s.c.Start()
	return s
}

// Upsert registers or replaces the cron job for the given trigger key.
// The callback is invoked on each schedule tick.
func (s *CronScheduler) Upsert(key, ns, name, schedule string, fn func()) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if id, ok := s.entries[key]; ok {
		s.c.Remove(id)
	}

	wrapped := func() {
		defer func() {
			if rec := recover(); rec != nil {
				s.Remove(key)
				s.reportPanic(ns, name, rec)
			}
		}()
		fn()
	}

	id, err := s.c.AddFunc(schedule, wrapped)
	if err != nil {
		return err
	}
	s.entries[key] = id
	return nil
}

// reportPanic sends a non-blocking best-effort panic report; a full channel drops it
// rather than blocking the cron library's job goroutine.
func (s *CronScheduler) reportPanic(ns, name string, rec interface{}) {
	if s.panicCh == nil {
		return
	}
	select {
	case s.panicCh <- TriggerPanic{Namespace: ns, Name: name, Source: PanicSourceCron, Reason: fmt.Sprintf("%v", rec)}:
	default:
	}
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
