// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package dag

// StepDecision is the action the executor recommends for a step this cycle.
type StepDecision string

const (
	// DecisionStart means the step is ready and should be launched.
	DecisionStart StepDecision = "Start"
	// DecisionSkip means the step's condition was not met; skip it.
	DecisionSkip StepDecision = "Skip"
	// DecisionWait means the step is waiting for dependencies.
	DecisionWait StepDecision = "Wait"
	// DecisionTerminal means the step is already in a terminal state.
	DecisionTerminal StepDecision = "Terminal"
)

// StepPhase mirrors the API type without importing k8s packages.
type StepPhase string

const (
	StepPhasePending   StepPhase = "Pending"
	StepPhaseRunning   StepPhase = "Running"
	StepPhaseSucceeded StepPhase = "Succeeded"
	StepPhaseFailed    StepPhase = "Failed"
	StepPhaseSkipped   StepPhase = "Skipped"
	StepPhaseRetrying  StepPhase = "Retrying"
)

// FailurePolicy mirrors the API type.
type FailurePolicy string

const (
	FailurePolicyStopAll        FailurePolicy = "StopAll"
	FailurePolicyContinueOthers FailurePolicy = "ContinueOthers"
	FailurePolicyRetryFailed    FailurePolicy = "RetryFailed"
)

// Advancement is the result of one executor cycle.
type Advancement struct {
	// Decisions maps each step name to the recommended action.
	Decisions map[string]StepDecision

	// RunComplete is true when every step has reached a terminal decision.
	RunComplete bool

	// RunSucceeded is true when RunComplete and no required step failed.
	RunSucceeded bool
}

// Advance computes the next set of decisions for all steps in the graph
// given their current phases and the chain's failure policy.
// It is a pure function with no side effects.
func Advance(
	graph *Graph,
	states map[string]StepPhase,
	policy FailurePolicy,
) Advancement {
	decisions := make(map[string]StepDecision, len(graph.nodes))

	// Determine if StopAll is in effect.
	stopAll := policy == FailurePolicyStopAll && anyTerminalFailure(states)

	for _, node := range graph.Nodes() {
		current := states[node.Name]

		switch current {
		case StepPhaseSucceeded, StepPhaseFailed, StepPhaseSkipped:
			decisions[node.Name] = DecisionTerminal
			continue
		case StepPhaseRunning, StepPhaseRetrying:
			decisions[node.Name] = DecisionWait
			continue
		}

		// Step is Pending (or unset).
		if stopAll {
			decisions[node.Name] = DecisionSkip
			continue
		}

		// Check if all dependencies have reached a terminal phase.
		if !allDepsTerminal(node.DependsOn, states) {
			decisions[node.Name] = DecisionWait
			continue
		}

		// Evaluate the step's run condition.
		if conditionMet(node, states) {
			decisions[node.Name] = DecisionStart
		} else {
			decisions[node.Name] = DecisionSkip
		}
	}

	// Determine overall run completion.
	complete := true
	for _, d := range decisions {
		if d == DecisionWait {
			complete = false
			break
		}
	}

	succeeded := false
	if complete {
		succeeded = !anyTerminalFailure(states)
	}

	return Advancement{
		Decisions:    decisions,
		RunComplete:  complete,
		RunSucceeded: succeeded,
	}
}

// allDepsTerminal returns true when every named dependency has a terminal phase.
func allDepsTerminal(deps []string, states map[string]StepPhase) bool {
	for _, dep := range deps {
		switch states[dep] {
		case StepPhaseSucceeded, StepPhaseFailed, StepPhaseSkipped:
			// terminal
		default:
			return false
		}
	}
	return true
}

// conditionMet returns true when the step's run condition is satisfied
// given the terminal states of its dependencies.
func conditionMet(node *Node, states map[string]StepPhase) bool {
	// A step with no dependencies is always eligible.
	if len(node.DependsOn) == 0 {
		return true
	}

	anyFailed := false
	allSucceeded := true
	for _, dep := range node.DependsOn {
		switch states[dep] {
		case StepPhaseFailed:
			anyFailed = true
			allSucceeded = false
		case StepPhaseSkipped:
			allSucceeded = false
		}
	}

	if node.RunOnSuccess && allSucceeded {
		return true
	}
	if node.RunOnFailure && anyFailed {
		return true
	}
	return false
}

// anyTerminalFailure returns true when at least one step has failed.
func anyTerminalFailure(states map[string]StepPhase) bool {
	for _, phase := range states {
		if phase == StepPhaseFailed {
			return true
		}
	}
	return false
}
