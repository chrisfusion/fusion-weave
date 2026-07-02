// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package trigger

import (
	"container/heap"
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

	heap     jobHeap
	updateCh chan []BatchJob // receives updated job lists from BatchCronScheduler.Upsert
	stopCh   chan struct{}
}

func (b *batchRunner) run() {
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
	mu      sync.Mutex
	runners map[string]*batchRunner
}

// NewBatchCronScheduler creates a BatchCronScheduler that sends fire events to fireCh.
func NewBatchCronScheduler(fireCh chan<- BatchFireRequest) *BatchCronScheduler {
	return &BatchCronScheduler{
		fireCh:  fireCh,
		runners: make(map[string]*batchRunner),
	}
}

// Upsert registers or replaces the job list for the given trigger key.
// If a runner already exists its job list is replaced live without restarting.
func (s *BatchCronScheduler) Upsert(key, ns, name string, jobs []BatchJob) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if runner, ok := s.runners[key]; ok {
		// Replace job list in the existing runner without restarting the goroutine.
		// updateCh is buffered(1); drain any stale pending update first.
		select {
		case <-runner.updateCh:
		default:
		}
		runner.updateCh <- jobs
		return
	}

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
	}
	s.runners[key] = runner
	go runner.run()
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
