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
		delete(s.entries, key)
	}

	// idBox is set to this invocation's own EntryID right after AddFunc registers
	// it below. wrapped only ever passes its ADDRESS around and never reads *idBox
	// itself outside s.mu (removeIfCurrent dereferences it after locking) — idBox's
	// write here and its read there are both inside s.mu's critical section, so the
	// mutex supplies the happens-before edge the Go memory model requires; without
	// that, this is a genuine data race (a plain read of idBox in wrapped, racing
	// this write, is exactly what `go test -race` flags). The point of tracking a
	// per-invocation ID at all: a panicking tick must remove exactly the entry IT
	// was registered as, never whatever a concurrent Upsert(key, ...) may have since
	// installed under the same key — removing by key alone would let a straggling
	// panic from a superseded schedule delete the new, valid entry instead, silently
	// stopping the trigger while Status.Active keeps reporting true.
	var idBox cron.EntryID
	wrapped := func() {
		defer func() {
			if rec := recover(); rec != nil {
				s.removeIfCurrent(key, &idBox)
				s.reportPanic(ns, name, rec)
			}
		}()
		fn()
	}

	id, err := s.c.AddFunc(schedule, wrapped)
	if err != nil {
		return err
	}
	idBox = id
	s.entries[key] = id
	return nil
}

// removeIfCurrent removes key's cron entry only if *idPtr is still the one
// registered for it — a no-op if a newer Upsert(key, ...) has already superseded
// it. Takes a pointer (dereferenced only here, under s.mu) rather than a value so
// the read of idBox in Upsert's wrapped closure is synchronized against Upsert's
// write to it, both inside s.mu's critical section.
func (s *CronScheduler) removeIfCurrent(key string, idPtr *cron.EntryID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := *idPtr
	if cur, ok := s.entries[key]; ok && cur == id {
		s.c.Remove(id)
		delete(s.entries, key)
	}
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
