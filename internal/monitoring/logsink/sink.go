// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

// Package logsink defines the interface for publishing log snapshots to
// external systems and provides a no-op implementation.
package logsink

import (
	"context"
	"time"
)

// LogSnapshot is a captured snapshot of pod log lines for one step execution.
type LogSnapshot struct {
	RunName   string    `json:"runName"`
	StepName  string    `json:"stepName"`
	PodName   string    `json:"podName"`
	Lines     []string  `json:"lines"`
	FetchedAt time.Time `json:"fetchedAt"`
}

// Sink publishes log snapshots to an external system asynchronously.
// Implementations must not block the caller; errors are expected to be
// handled internally (e.g. logged and dropped).
type Sink interface {
	Publish(ctx context.Context, snap LogSnapshot) error
}

// NoopSink discards all snapshots. Used when no sink is configured.
type NoopSink struct{}

func (NoopSink) Publish(_ context.Context, _ LogSnapshot) error { return nil }
