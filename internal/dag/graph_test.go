// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package dag_test

import (
	"testing"

	"fusion-platform.io/fusion-weave/internal/dag"
)

func TestBuildGraph_Valid(t *testing.T) {
	nodes := []dag.Node{
		{Name: "a", RunOnSuccess: true},
		{Name: "b", DependsOn: []string{"a"}, RunOnSuccess: true},
		{Name: "c", DependsOn: []string{"a"}, RunOnSuccess: true},
		{Name: "d", DependsOn: []string{"b", "c"}, RunOnSuccess: true},
	}
	g, err := dag.BuildGraph(nodes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if g.Node("d") == nil {
		t.Fatal("expected node d")
	}
}

func TestBuildGraph_Cycle(t *testing.T) {
	nodes := []dag.Node{
		{Name: "a", DependsOn: []string{"b"}, RunOnSuccess: true},
		{Name: "b", DependsOn: []string{"a"}, RunOnSuccess: true},
	}
	_, err := dag.BuildGraph(nodes)
	if err == nil {
		t.Fatal("expected cycle error")
	}
}

func TestBuildGraph_UnknownDep(t *testing.T) {
	nodes := []dag.Node{
		{Name: "a", DependsOn: []string{"x"}, RunOnSuccess: true},
	}
	_, err := dag.BuildGraph(nodes)
	if err == nil {
		t.Fatal("expected unknown dep error")
	}
}

func TestBuildGraph_DuplicateName(t *testing.T) {
	nodes := []dag.Node{
		{Name: "a", RunOnSuccess: true},
		{Name: "a", RunOnSuccess: true},
	}
	_, err := dag.BuildGraph(nodes)
	if err == nil {
		t.Fatal("expected duplicate name error")
	}
}

func states(pairs ...string) map[string]dag.StepPhase {
	m := make(map[string]dag.StepPhase, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		m[pairs[i]] = dag.StepPhase(pairs[i+1])
	}
	return m
}

func TestAdvance_Sequential(t *testing.T) {
	nodes := []dag.Node{
		{Name: "a", RunOnSuccess: true},
		{Name: "b", DependsOn: []string{"a"}, RunOnSuccess: true},
	}
	g, _ := dag.BuildGraph(nodes)

	// Initial state: both pending.
	adv := dag.Advance(g, states(), dag.FailurePolicyContinueOthers)
	if adv.Decisions["a"] != dag.DecisionStart {
		t.Errorf("expected a=Start, got %s", adv.Decisions["a"])
	}
	if adv.Decisions["b"] != dag.DecisionWait {
		t.Errorf("expected b=Wait, got %s", adv.Decisions["b"])
	}

	// After a succeeds.
	adv = dag.Advance(g, states("a", "Succeeded"), dag.FailurePolicyContinueOthers)
	if adv.Decisions["b"] != dag.DecisionStart {
		t.Errorf("expected b=Start, got %s", adv.Decisions["b"])
	}

	// Both done.
	adv = dag.Advance(g, states("a", "Succeeded", "b", "Succeeded"), dag.FailurePolicyContinueOthers)
	if !adv.RunComplete {
		t.Error("expected run complete")
	}
	if !adv.RunSucceeded {
		t.Error("expected run succeeded")
	}
}

func TestAdvance_FanOut(t *testing.T) {
	nodes := []dag.Node{
		{Name: "root", RunOnSuccess: true},
		{Name: "b1", DependsOn: []string{"root"}, RunOnSuccess: true},
		{Name: "b2", DependsOn: []string{"root"}, RunOnSuccess: true},
	}
	g, _ := dag.BuildGraph(nodes)

	adv := dag.Advance(g, states("root", "Succeeded"), dag.FailurePolicyContinueOthers)
	if adv.Decisions["b1"] != dag.DecisionStart {
		t.Errorf("expected b1=Start")
	}
	if adv.Decisions["b2"] != dag.DecisionStart {
		t.Errorf("expected b2=Start")
	}
}

func TestAdvance_FanIn(t *testing.T) {
	nodes := []dag.Node{
		{Name: "a", RunOnSuccess: true},
		{Name: "b", RunOnSuccess: true},
		{Name: "join", DependsOn: []string{"a", "b"}, RunOnSuccess: true},
	}
	g, _ := dag.BuildGraph(nodes)

	// Only a done — join must wait.
	adv := dag.Advance(g, states("a", "Succeeded"), dag.FailurePolicyContinueOthers)
	if adv.Decisions["join"] != dag.DecisionWait {
		t.Errorf("expected join=Wait, got %s", adv.Decisions["join"])
	}

	// Both done — join can start.
	adv = dag.Advance(g, states("a", "Succeeded", "b", "Succeeded"), dag.FailurePolicyContinueOthers)
	if adv.Decisions["join"] != dag.DecisionStart {
		t.Errorf("expected join=Start, got %s", adv.Decisions["join"])
	}
}

func TestAdvance_ConditionalOnFailure(t *testing.T) {
	nodes := []dag.Node{
		{Name: "main", RunOnSuccess: true},
		{Name: "cleanup", DependsOn: []string{"main"}, RunOnSuccess: false, RunOnFailure: true},
	}
	g, _ := dag.BuildGraph(nodes)

	// main succeeds — cleanup should be skipped.
	adv := dag.Advance(g, states("main", "Succeeded"), dag.FailurePolicyContinueOthers)
	if adv.Decisions["cleanup"] != dag.DecisionSkip {
		t.Errorf("expected cleanup=Skip, got %s", adv.Decisions["cleanup"])
	}

	// main fails — cleanup should start.
	adv = dag.Advance(g, states("main", "Failed"), dag.FailurePolicyContinueOthers)
	if adv.Decisions["cleanup"] != dag.DecisionStart {
		t.Errorf("expected cleanup=Start, got %s", adv.Decisions["cleanup"])
	}
}

func TestAncestors(t *testing.T) {
	nodes := []dag.Node{
		{Name: "a", RunOnSuccess: true},
		{Name: "b", DependsOn: []string{"a"}, RunOnSuccess: true},
		{Name: "c", DependsOn: []string{"a"}, RunOnSuccess: true},
		{Name: "d", DependsOn: []string{"b", "c"}, RunOnSuccess: true},
	}
	g, err := dag.BuildGraph(nodes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Root has no ancestors.
	if anc := g.Ancestors("a"); len(anc) != 0 {
		t.Errorf("expected no ancestors for a, got %v", anc)
	}

	// Direct dep only.
	anc := g.Ancestors("b")
	if !anc["a"] || len(anc) != 1 {
		t.Errorf("expected exactly {a} as ancestors of b, got %v", anc)
	}

	// Transitive + fan-in: d → b, c → a.
	anc = g.Ancestors("d")
	if !anc["a"] || !anc["b"] || !anc["c"] || len(anc) != 3 {
		t.Errorf("expected ancestors {a,b,c} for d, got %v", anc)
	}
}

func TestAdvance_StopAll(t *testing.T) {
	nodes := []dag.Node{
		{Name: "a", RunOnSuccess: true},
		{Name: "b", RunOnSuccess: true},
		{Name: "c", DependsOn: []string{"a", "b"}, RunOnSuccess: true},
	}
	g, _ := dag.BuildGraph(nodes)

	// a failed, b still pending — StopAll should skip c and b.
	adv := dag.Advance(g, states("a", "Failed"), dag.FailurePolicyStopAll)
	if adv.Decisions["c"] != dag.DecisionSkip {
		t.Errorf("expected c=Skip, got %s", adv.Decisions["c"])
	}
	if adv.Decisions["b"] != dag.DecisionSkip {
		t.Errorf("expected b=Skip, got %s", adv.Decisions["b"])
	}
}
