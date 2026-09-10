// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package trigger

import (
	"container/heap"
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// BatchFireRequest is sent from the BatchCronScheduler to the trigger reconciler
// for each individual job that is due to fire.
type BatchFireRequest struct {
	TriggerNamespace   string
	TriggerName        string
	JobID              string
	ParameterOverrides []corev1.EnvVar
}

// heapEntry is one item in the per-trigger min-heap.
type heapEntry struct {
	next time.Time
	job  *BatchJob
}

// jobHeap implements container/heap.Interface sorted by next fire time (min-heap).
type jobHeap []heapEntry

func (h jobHeap) Len() int            { return len(h) }
func (h jobHeap) Less(i, j int) bool { return h[i].next.Before(h[j].next) }
func (h jobHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }

func (h *jobHeap) Push(x interface{}) {
	*h = append(*h, x.(heapEntry))
}

func (h *jobHeap) Pop() interface{} {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}

// batchRunner is the per-trigger goroutine that drives a min-heap of scheduled jobs.
type batchRunner struct {
	ns, name string
	fireCh   chan<- BatchFireRequest
	// onPanic is invoked (in this goroutine) if run() recovers a panic, before the
	// goroutine exits. It must deregister the runner from the owning scheduler and
	// report the panic — after this, the whole trigger stops firing.
	onPanic func(rec interface{})

	heap     jobHeap
	updateCh chan []BatchJob // receives updated job lists from BatchCronScheduler.Upsert
	stopCh   chan struct{}
	// dead is closed when run() returns, for any reason (panic, stopCh, or a bug in
	// this loop) — see Upsert for why callers must check it before trusting updateCh.
	dead chan struct{}
}

// run drives the min-heap until stopCh closes or a panic is recovered. A panic here
// (e.g. from malformed job data) ends the goroutine entirely — same effect as Remove —
// rather than crashing the operator process, since this loop runs outside
// controller-runtime's per-Reconcile panic recovery.
func (b *batchRunner) run() {
	defer close(b.dead)
	defer func() {
		if rec := recover(); rec != nil && b.onPanic != nil {
			b.onPanic(rec)
		}
	}()
	heap.Init(&b.heap)
	for {
		var timer *time.Timer
		if b.heap.Len() > 0 {
			d := time.Until(b.heap[0].next)
			if d < 0 {
				d = 0
			}
			timer = time.NewTimer(d)
		} else {
			timer = time.NewTimer(time.Hour)
		}

		select {
		case <-timer.C:
			now := time.Now()
			for b.heap.Len() > 0 && !b.heap[0].next.After(now) {
				entry := heap.Pop(&b.heap).(heapEntry)
				select {
				case b.fireCh <- BatchFireRequest{
					TriggerNamespace:   b.ns,
					TriggerName:        b.name,
					JobID:              entry.job.ID,
					ParameterOverrides: entry.job.EnvVars,
				}:
				default:
					// Reconciler is lagging; drop this tick — the next cron tick will fire again.
				}
				next := entry.job.Schedule.Next(now)
				if !next.IsZero() {
					heap.Push(&b.heap, heapEntry{next: next, job: entry.job})
				}
			}

		case jobs := <-b.updateCh:
			timer.Stop()
			b.heap = b.heap[:0]
			now := time.Now()
			for i := range jobs {
				j := &jobs[i]
				base := now
				if !j.NotBefore.IsZero() && j.NotBefore.After(now) {
					base = j.NotBefore
				}
				next := j.Schedule.Next(base)
				if !next.IsZero() {
					heap.Push(&b.heap, heapEntry{next: next, job: j})
				}
			}
			heap.Init(&b.heap)

		case <-b.stopCh:
			timer.Stop()
			return
		}
	}
}

// BatchCronScheduler manages one goroutine per batch trigger. Each goroutine
// drives a min-heap of (nextFireTime, job) pairs and sends BatchFireRequests
// to fireCh when a job is due. It is completely isolated from CronScheduler.
type BatchCronScheduler struct {
	fireCh  chan<- BatchFireRequest
	panicCh chan<- TriggerPanic
	mu      sync.Mutex
	runners map[string]*batchRunner
}

// NewBatchCronScheduler creates a BatchCronScheduler that sends fire events to fireCh.
// A panic recovered from a trigger's runner goroutine is reported on panicCh
// (non-blocking); that trigger's runner is removed so it does not fire again until
// re-Upserted.
func NewBatchCronScheduler(fireCh chan<- BatchFireRequest, panicCh chan<- TriggerPanic) *BatchCronScheduler {
	return &BatchCronScheduler{
		fireCh:  fireCh,
		panicCh: panicCh,
		runners: make(map[string]*batchRunner),
	}
}

// reportPanic sends a non-blocking best-effort panic report; a full channel drops it
// rather than blocking the runner goroutine that is already exiting.
func (s *BatchCronScheduler) reportPanic(ns, name string, rec interface{}) {
	if s.panicCh == nil {
		return
	}
	select {
	case s.panicCh <- TriggerPanic{Namespace: ns, Name: name, Source: PanicSourceBatchCron, Reason: fmt.Sprintf("%v", rec)}:
	default:
	}
}

// Upsert registers or replaces the job list for the given trigger key.
// If a runner already exists its job list is replaced live without restarting.
func (s *BatchCronScheduler) Upsert(key, ns, name string, jobs []BatchJob) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if runner, ok := s.runners[key]; ok {
		alive := true
		select {
		case <-runner.dead:
			alive = false
		default:
		}
		if alive {
			// Replace job list in the existing runner without restarting the
			// goroutine. updateCh is buffered(1); drain any stale pending update
			// first. The dead check above closes almost all of the window where a
			// concurrently-panicking run() would otherwise never read this send
			// (its own select loop has already exited) — a few instructions of
			// residual race remain, inherent to signaling "the goroutine is about
			// to stop reading" via a channel it does not hold a lock across. If hit,
			// the update is silently lost here but not permanently: the next
			// reconcile of this trigger calls Upsert again with the full current
			// job list.
			select {
			case <-runner.updateCh:
			default:
			}
			runner.updateCh <- jobs
			return
		}
		// Runner already exited (panic or otherwise) but onPanic hasn't reaped it
		// from the map yet — fall through and start a fresh one below instead of
		// sending into a channel nothing will ever read again.
		delete(s.runners, key)
	}

	s.startRunnerLocked(key, ns, name, jobs)
}

// startRunnerLocked builds the initial heap and starts the goroutine for a new
// trigger key. Callers must hold s.mu.
func (s *BatchCronScheduler) startRunnerLocked(key, ns, name string, jobs []BatchJob) {
	now := time.Now()
	h := make(jobHeap, 0, len(jobs))
	for i := range jobs {
		j := &jobs[i]
		base := now
		if !j.NotBefore.IsZero() && j.NotBefore.After(now) {
			base = j.NotBefore
		}
		next := j.Schedule.Next(base)
		if !next.IsZero() {
			h = append(h, heapEntry{next: next, job: j})
		}
	}
	heap.Init(&h)

	runner := &batchRunner{
		ns:       ns,
		name:     name,
		fireCh:   s.fireCh,
		heap:     h,
		updateCh: make(chan []BatchJob, 1),
		stopCh:   make(chan struct{}),
		dead:     make(chan struct{}),
	}
	// onPanic closes over runner (not just key) so a late panic from a superseded
	// generation of this trigger's runner can never delete a newer, live one —
	// removeIfCurrent compares identity, not just the map key.
	runner.onPanic = func(rec interface{}) {
		s.removeIfCurrent(key, runner)
		s.reportPanic(ns, name, rec)
	}
	s.runners[key] = runner
	go runner.run()
}

// removeIfCurrent removes key's runner only if r is still the one registered for
// it — a no-op if a newer Upsert(key, ...) has already replaced it.
func (s *BatchCronScheduler) removeIfCurrent(key string, r *batchRunner) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur, ok := s.runners[key]; ok && cur == r {
		delete(s.runners, key)
	}
}

// Remove stops and removes the runner for the given trigger key.
func (s *BatchCronScheduler) Remove(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if runner, ok := s.runners[key]; ok {
		close(runner.stopCh)
		delete(s.runners, key)
	}
}

// Stop shuts down all runners. Called on operator shutdown.
func (s *BatchCronScheduler) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, runner := range s.runners {
		close(runner.stopCh)
		delete(s.runners, key)
	}
}
