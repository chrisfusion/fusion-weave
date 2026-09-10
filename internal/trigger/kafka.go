// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package trigger

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	kafka "github.com/segmentio/kafka-go"
	"github.com/segmentio/kafka-go/sasl/plain"
	"github.com/segmentio/kafka-go/sasl/scram"
	corev1 "k8s.io/api/core/v1"
)

// KafkaFireRequest carries a single Kafka message that passed event/bucket filters,
// ready to be turned into a WeaveRun by the trigger reconciler.
type KafkaFireRequest struct {
	TriggerNamespace string
	TriggerName      string
	EnvVars          []corev1.EnvVar
}

// KafkaRunnerConfig holds the resolved configuration for one Kafka consumer goroutine.
// SASL credentials are pre-resolved by the reconciler (which has k8s client access).
type KafkaRunnerConfig struct {
	Brokers       []string
	Topic         string
	ConsumerGroup string
	EventFilter   []string
	BucketFilter  []string
	// SASL — empty username means no authentication.
	SASLUsername  string
	SASLPassword  string
	SASLMechanism string // "PLAIN" | "SCRAM-SHA-256" | "SCRAM-SHA-512"; defaults to PLAIN
}

// kafkaRunner owns one Kafka consumer goroutine for a single WeaveTrigger.
type kafkaRunner struct {
	ns, name string
	fireCh   chan<- KafkaFireRequest
	cfg      KafkaRunnerConfig
	cancel   context.CancelFunc
	// onPanic is invoked (in this goroutine) if run() recovers a panic, before the
	// goroutine exits. It must deregister the runner from the owning consumer and
	// report the panic — after this, the whole trigger stops firing.
	onPanic func(rec interface{})
}

// newKafkaRunner builds onPanic itself (rather than accepting one as a parameter)
// so it can close over the runner it creates and pass that exact pointer to
// removeIfCurrent — closing the construction fully, including onPanic, before
// starting the goroutine guarantees run() can only ever observe it in its finished
// state (the goroutine-creation happens-before edge), with no window for a data
// race on which runner instance a late panic is allowed to deregister. Comparing by
// identity (not just by key) matters because Upsert can replace c.runners[key] with
// a newer runner while this one's goroutine is still unwinding from a panic; without
// the identity check, that unwind would delete the newer, live runner instead.
func newKafkaRunner(c *KafkaConsumer, key, ns, name string, fireCh chan<- KafkaFireRequest, cfg KafkaRunnerConfig) *kafkaRunner {
	ctx, cancel := context.WithCancel(context.Background())
	r := &kafkaRunner{ns: ns, name: name, fireCh: fireCh, cfg: cfg, cancel: cancel}
	r.onPanic = func(rec interface{}) {
		c.removeIfCurrent(key, r)
		c.reportPanic(ns, name, rec)
	}
	go r.run(ctx)
	return r
}

func (r *kafkaRunner) stop() { r.cancel() }

// run consumes until ctx is cancelled or a panic is recovered. A panic here (e.g.
// malformed message handling) ends the goroutine entirely rather than crashing the
// operator process, since this loop runs outside controller-runtime's per-Reconcile
// panic recovery.
func (r *kafkaRunner) run(ctx context.Context) {
	defer func() {
		if rec := recover(); rec != nil && r.onPanic != nil {
			r.onPanic(rec)
		}
	}()
	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers:        r.cfg.Brokers,
		Topic:          r.cfg.Topic,
		GroupID:        r.cfg.ConsumerGroup,
		Dialer:         buildDialer(r.cfg),
		CommitInterval: 0, // manual commit
		// Retry quickly on transient errors; avoid log spam on context cancel.
		MaxWait:     500 * time.Millisecond,
		ErrorLogger: kafka.LoggerFunc(func(string, ...interface{}) {}),
	})
	defer reader.Close()

	for {
		msg, err := reader.FetchMessage(ctx)
		if err != nil {
			// Context cancelled (stop) or unrecoverable reader error.
			return
		}

		envVars, ok := parseS3EventEnvVars(msg.Value, r.cfg.EventFilter, r.cfg.BucketFilter)
		if !ok {
			_ = reader.CommitMessages(ctx, msg)
			continue
		}

		select {
		case r.fireCh <- KafkaFireRequest{
			TriggerNamespace: r.ns,
			TriggerName:      r.name,
			EnvVars:          envVars,
		}:
		default:
			// Fire channel full — skip per policy; offset is committed below.
		}

		// Always commit: skip semantics apply both to filtered and throttled events.
		_ = reader.CommitMessages(ctx, msg)
	}
}

// buildDialer returns a Dialer configured with SASL if credentials are present.
func buildDialer(cfg KafkaRunnerConfig) *kafka.Dialer {
	if cfg.SASLUsername == "" {
		return kafka.DefaultDialer
	}

	dialer := &kafka.Dialer{
		Timeout:   10 * time.Second,
		DualStack: true,
	}

	switch strings.ToUpper(cfg.SASLMechanism) {
	case "SCRAM-SHA-256":
		if m, err := scram.Mechanism(scram.SHA256, cfg.SASLUsername, cfg.SASLPassword); err == nil {
			dialer.SASLMechanism = m
		}
	case "SCRAM-SHA-512":
		if m, err := scram.Mechanism(scram.SHA512, cfg.SASLUsername, cfg.SASLPassword); err == nil {
			dialer.SASLMechanism = m
		}
	default: // "PLAIN" or empty
		dialer.SASLMechanism = plain.Mechanism{
			Username: cfg.SASLUsername,
			Password: cfg.SASLPassword,
		}
	}

	return dialer
}

// KafkaConsumer manages one kafkaRunner goroutine per WeaveTrigger.
// It is safe for concurrent use.
type KafkaConsumer struct {
	fireCh  chan<- KafkaFireRequest
	panicCh chan<- TriggerPanic
	mu      sync.Mutex
	runners map[string]*kafkaRunner
}

// NewKafkaConsumer creates a KafkaConsumer. A panic recovered from a trigger's
// runner goroutine is reported on panicCh (non-blocking); that trigger's runner is
// removed so it does not consume again until re-Upserted.
func NewKafkaConsumer(fireCh chan<- KafkaFireRequest, panicCh chan<- TriggerPanic) *KafkaConsumer {
	return &KafkaConsumer{
		fireCh:  fireCh,
		panicCh: panicCh,
		runners: make(map[string]*kafkaRunner),
	}
}

// reportPanic sends a non-blocking best-effort panic report; a full channel drops it
// rather than blocking the runner goroutine that is already exiting.
func (c *KafkaConsumer) reportPanic(ns, name string, rec interface{}) {
	if c.panicCh == nil {
		return
	}
	select {
	case c.panicCh <- TriggerPanic{Namespace: ns, Name: name, Source: PanicSourceKafka, Reason: fmt.Sprintf("%v", rec)}:
	default:
	}
}

// Upsert starts or replaces the consumer goroutine for key.
// If a runner already exists for key, it is stopped before the new one starts.
func (c *KafkaConsumer) Upsert(key, ns, name string, cfg KafkaRunnerConfig) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if r, ok := c.runners[key]; ok {
		r.stop()
	}
	c.runners[key] = newKafkaRunner(c, key, ns, name, c.fireCh, cfg)
}

// removeIfCurrent removes key's runner only if r is still the one registered for
// it — a no-op if a newer Upsert(key, ...) has already replaced it (see
// newKafkaRunner for why this identity check, not a plain delete-by-key, is needed).
func (c *KafkaConsumer) removeIfCurrent(key string, r *kafkaRunner) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cur, ok := c.runners[key]; ok && cur == r {
		delete(c.runners, key)
	}
}

// Remove stops and removes the consumer goroutine for key.
func (c *KafkaConsumer) Remove(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if r, ok := c.runners[key]; ok {
		r.stop()
		delete(c.runners, key)
	}
}

// Stop shuts down all consumer goroutines.
func (c *KafkaConsumer) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, r := range c.runners {
		r.stop()
	}
	c.runners = make(map[string]*kafkaRunner)
}
