// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package logsink

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/go-logr/logr"
	kafka "github.com/segmentio/kafka-go"
)

const bufferSize = 256

// KafkaSink publishes LogSnapshot events to a Kafka topic.
//
// Internally a buffered channel is drained by a single background goroutine so
// Publish never blocks the HTTP handler. Close() signals the goroutine to stop
// accepting new snapshots, flushes all buffered entries, then closes the writer.
type KafkaSink struct {
	writer *kafka.Writer
	ch     chan LogSnapshot
	stop   chan struct{} // closed by Close() to stop new Publish calls
	done   chan struct{} // closed by drainLoop when it exits
	logger logr.Logger
	once   sync.Once
}

// NewKafkaSink creates a KafkaSink that writes to the given brokers and topic.
// The background drain goroutine starts immediately.
func NewKafkaSink(brokers []string, topic string, logger logr.Logger) *KafkaSink {
	s := &KafkaSink{
		writer: &kafka.Writer{
			Addr:     kafka.TCP(brokers...),
			Topic:    topic,
			Balancer: &kafka.LeastBytes{},
		},
		ch:     make(chan LogSnapshot, bufferSize),
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
		logger: logger.WithName("kafka-sink"),
	}
	go s.drainLoop()
	return s
}

// Publish enqueues snap for asynchronous delivery. If the sink is shutting
// down or the internal buffer is full the snapshot is dropped.
func (s *KafkaSink) Publish(_ context.Context, snap LogSnapshot) error {
	// Fast path: check if shutdown has been signalled without blocking.
	select {
	case <-s.stop:
		return nil
	default:
	}
	// Enqueue or drop if buffer is full.
	select {
	case s.ch <- snap:
	case <-s.stop:
		// Shutdown raced with the enqueue attempt — drop the snapshot.
	default:
		s.logger.Info("buffer full, dropping log snapshot",
			"run", snap.RunName, "step", snap.StepName)
	}
	return nil
}

// Close signals the sink to stop accepting new snapshots, waits for the drain
// goroutine to flush all buffered entries and close the Kafka writer, then
// returns. Idempotent — safe to call more than once.
func (s *KafkaSink) Close() error {
	s.once.Do(func() { close(s.stop) })
	<-s.done // wait for drainLoop to finish flushing
	return nil
}

// drainLoop reads from the channel until a stop signal is received, then
// drains any remaining buffered items and closes the Kafka writer.
func (s *KafkaSink) drainLoop() {
	defer close(s.done)
	for {
		select {
		case snap := <-s.ch:
			s.writeOne(snap)
		case <-s.stop:
			// Drain remaining buffered items before exiting.
			for {
				select {
				case snap := <-s.ch:
					s.writeOne(snap)
				default:
					if err := s.writer.Close(); err != nil {
						s.logger.Error(err, "failed to close Kafka writer")
					}
					return
				}
			}
		}
	}
}

func (s *KafkaSink) writeOne(snap LogSnapshot) {
	data, err := json.Marshal(snap)
	if err != nil {
		s.logger.Error(err, "failed to marshal log snapshot")
		return
	}
	if err := s.writer.WriteMessages(context.Background(), kafka.Message{Value: data}); err != nil {
		s.logger.Error(err, "failed to write to Kafka",
			"run", snap.RunName, "step", snap.StepName)
	}
}
