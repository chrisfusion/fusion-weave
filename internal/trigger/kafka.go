// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package trigger

import (
	"context"
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
}

func newKafkaRunner(ns, name string, fireCh chan<- KafkaFireRequest, cfg KafkaRunnerConfig) *kafkaRunner {
	ctx, cancel := context.WithCancel(context.Background())
	r := &kafkaRunner{ns: ns, name: name, fireCh: fireCh, cfg: cfg, cancel: cancel}
	go r.run(ctx)
	return r
}

func (r *kafkaRunner) stop() { r.cancel() }

func (r *kafkaRunner) run(ctx context.Context) {
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
	mu      sync.Mutex
	runners map[string]*kafkaRunner
}

func NewKafkaConsumer(fireCh chan<- KafkaFireRequest) *KafkaConsumer {
	return &KafkaConsumer{
		fireCh:  fireCh,
		runners: make(map[string]*kafkaRunner),
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
	c.runners[key] = newKafkaRunner(ns, name, c.fireCh, cfg)
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
