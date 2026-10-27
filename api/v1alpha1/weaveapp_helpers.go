// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package v1alpha1

// WeaveAppFireAnnotation on a job WeaveApp fires the named entrypoint once on
// demand; the reconciler consumes (removes) it. Shared by the operator and the
// API's POST /apps/{name}/fire.
const WeaveAppFireAnnotation = "fusion-platform.io/fire"

// HasEntrypoint reports whether the app declares an entrypoint of that name.
func (a *WeaveApp) HasEntrypoint(name string) bool {
	for _, ep := range a.Spec.Entrypoints {
		if ep.Name == name {
			return true
		}
	}
	return false
}

// HasRunningExecution reports whether an execution of the entrypoint is still
// running; a fire for it is then skipped.
func (a *WeaveApp) HasRunningExecution(entrypoint string) bool {
	for _, e := range a.Status.Executions {
		if e.Entrypoint == entrypoint && e.Phase == WeaveAppExecutionRunning {
			return true
		}
	}
	return false
}
