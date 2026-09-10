// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package trigger

// PanicSource identifies which activation-source goroutine recovered a panic.
type PanicSource string

const (
	PanicSourceCron      PanicSource = "cron"
	PanicSourceBatchCron PanicSource = "batchCron"
	PanicSourceKafka     PanicSource = "kafka"
)

// TriggerPanic is reported when a trigger's activation-source goroutine recovers
// from a panic. The goroutine unregisters itself before reporting, so the trigger
// stops firing immediately; the reconciler is responsible for reflecting this as
// Status.Quarantined on the WeaveTrigger so it is visible and does not silently
// re-register on the next unrelated reconcile.
type TriggerPanic struct {
	Namespace string
	Name      string
	Source    PanicSource
	Reason    string
}
