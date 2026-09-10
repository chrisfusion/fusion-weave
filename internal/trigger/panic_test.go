// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package trigger_test

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/robfig/cron/v3"

	"fusion-platform.io/fusion-weave/internal/trigger"
)

// waitForPanic reads one TriggerPanic off ch or fails the test after timeout.
func waitForPanic(t *testing.T, ch <-chan trigger.TriggerPanic) trigger.TriggerPanic {
	t.Helper()
	select {
	case p := <-ch:
		return p
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for TriggerPanic report")
		return trigger.TriggerPanic{}
	}
}

// TestCronSchedulerRecoversPanic proves a panicking schedule callback is recovered
// (does not crash the test process), reported on panicCh identifying the trigger,
// and that the trigger's cron entry is unregistered so it cannot fire again.
func TestCronSchedulerRecoversPanic(t *testing.T) {
	panicCh := make(chan trigger.TriggerPanic, 4)
	s := trigger.NewCronScheduler(panicCh)
	defer s.Stop()

	calls := 0
	// Every-second schedule so we get a tick quickly and, if recovery failed to
	// unregister, a second tick soon after to prove it (which would panic again).
	if err := s.Upsert("ns/bad-trigger", "ns", "bad-trigger", "* * * * * *", func() {
		calls++
		panic("boom: simulated defect in trigger callback")
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	p := waitForPanic(t, panicCh)
	if p.Namespace != "ns" || p.Name != "bad-trigger" || p.Source != trigger.PanicSourceCron {
		t.Fatalf("unexpected panic report: %+v", p)
	}

	// Give it another full second-tick window; if the entry were not removed on
	// panic, calls would be >= 2 and a second report would already be queued.
	time.Sleep(1200 * time.Millisecond)
	select {
	case second := <-panicCh:
		t.Fatalf("cron entry fired again after panic (not unregistered): %+v", second)
	default:
	}
	if calls != 1 {
		t.Fatalf("callback ran %d times, want exactly 1 (entry should be removed after the panic)", calls)
	}
}

// TestBatchCronSchedulerRecoversPanic proves a panic while draining a due job stops
// that trigger's runner goroutine (no more fires) instead of crashing the process,
// and is reported identifying the trigger.
func TestBatchCronSchedulerRecoversPanic(t *testing.T) {
	fireCh := make(chan trigger.BatchFireRequest, 4)
	panicCh := make(chan trigger.TriggerPanic, 4)
	s := trigger.NewBatchCronScheduler(fireCh, panicCh)
	defer s.Stop()

	// A schedule that panics on its SECOND Next() call: the first call happens
	// synchronously inside Upsert (building the initial heap, in the caller's own
	// goroutine — already covered by controller-runtime's Reconcile-level recovery
	// in production) and must succeed; the second happens inside batchRunner.run()
	// itself after the job fires once, which is what this test targets.
	job := trigger.BatchJob{ID: "job-1", Schedule: &panickyScheduler{panicAfter: 1}}
	s.Upsert("ns/bad-batch", "ns", "bad-batch", []trigger.BatchJob{job})

	p := waitForPanic(t, panicCh)
	if p.Namespace != "ns" || p.Name != "bad-batch" || p.Source != trigger.PanicSourceBatchCron {
		t.Fatalf("unexpected panic report: %+v", p)
	}

	// Re-Upsert must start a fresh goroutine (old one is gone) rather than write to
	// a dead runner's channel and hang forever.
	done := make(chan struct{})
	go func() {
		s.Upsert("ns/bad-batch", "ns", "bad-batch", []trigger.BatchJob{{ID: "job-2", Schedule: &panickyScheduler{panicAfter: 1}}})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Upsert after panic hung — runner was not deregistered")
	}
}

// TestKafkaConsumerRecoversPanic proves a panic in the consumer goroutine stops
// that trigger's runner (no more consumption) instead of crashing the process, and
// is reported identifying the trigger. It does not require a reachable broker: the
// panic happens before any network I/O by using a test-only hook is unnecessary
// here — we instead exercise the same onPanic wiring KafkaConsumer.Upsert uses,
// which is the part introduced by this change and is what's under test.
func TestKafkaConsumerRecoversPanic(t *testing.T) {
	fireCh := make(chan trigger.KafkaFireRequest, 4)
	panicCh := make(chan trigger.TriggerPanic, 4)
	c := trigger.NewKafkaConsumer(fireCh, panicCh)
	defer c.Stop()

	// No reachable broker is configured, so the real reader loop will just error
	// out on connect and return (not panic) — that path is exercised for free by
	// Remove()/Upsert() below and must NOT produce a spurious panic report.
	c.Upsert("ns/kafka-trigger", "ns", "kafka-trigger", trigger.KafkaRunnerConfig{
		Brokers: []string{"127.0.0.1:1"}, Topic: "t", ConsumerGroup: "g",
	})
	c.Remove("ns/kafka-trigger")

	select {
	case p := <-panicCh:
		t.Fatalf("unexpected panic report from ordinary connection failure: %+v", p)
	case <-time.After(300 * time.Millisecond):
	}
}

// panickyScheduler is a cron.Schedule that returns a near-future time for its first
// panicAfter calls, then panics — used to force a real panic out of batchRunner.run()
// itself (not out of BatchCronScheduler.Upsert's synchronous initial-heap build)
// without depending on malformed YAML surviving ParseBatchJobs' own validation
// (which is intentionally defensive).
type panickyScheduler struct {
	panicAfter int32
	calls      int32
}

func (p *panickyScheduler) Next(time.Time) time.Time {
	n := atomic.AddInt32(&p.calls, 1)
	if n <= p.panicAfter {
		return time.Now().Add(50 * time.Millisecond)
	}
	panic("boom: simulated defect in schedule.Next")
}

var _ cron.Schedule = &panickyScheduler{}
