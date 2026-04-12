// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

// Package dag provides pure-Go DAG construction and validation utilities
// with no Kubernetes dependencies. All functions are stateless and safe
// for concurrent use.
package dag

import "fmt"

// Node is a single vertex in the job dependency graph.
type Node struct {
	Name         string
	DependsOn    []string
	RunOnSuccess bool
	RunOnFailure bool
}

// Graph is a validated directed acyclic graph of job steps.
type Graph struct {
	nodes map[string]*Node
	order []string // topological order, roots first
}

// BuildGraph constructs a Graph from a slice of nodes and validates:
// - all dependency names resolve to existing nodes
// - the graph is acyclic
func BuildGraph(nodes []Node) (*Graph, error) {
	g := &Graph{nodes: make(map[string]*Node, len(nodes))}
	for i := range nodes {
		n := &nodes[i]
		if _, dup := g.nodes[n.Name]; dup {
			return nil, fmt.Errorf("duplicate step name %q", n.Name)
		}
		g.nodes[n.Name] = n
	}

	for _, n := range g.nodes {
		for _, dep := range n.DependsOn {
			if _, ok := g.nodes[dep]; !ok {
				return nil, fmt.Errorf("step %q depends on unknown step %q", n.Name, dep)
			}
		}
	}

	order, err := topoSort(g.nodes)
	if err != nil {
		return nil, err
	}
	g.order = order
	return g, nil
}

// Nodes returns all nodes in topological order (roots first).
func (g *Graph) Nodes() []*Node {
	out := make([]*Node, len(g.order))
	for i, name := range g.order {
		out[i] = g.nodes[name]
	}
	return out
}

// Node returns the node with the given name or nil.
func (g *Graph) Node(name string) *Node {
	return g.nodes[name]
}

// Ancestors returns the set of all step names that are direct or transitive
// dependencies of the named step. The step itself is not included.
func (g *Graph) Ancestors(name string) map[string]bool {
	visited := make(map[string]bool)
	var walk func(n string)
	walk = func(n string) {
		node := g.nodes[n]
		if node == nil {
			return
		}
		for _, dep := range node.DependsOn {
			if !visited[dep] {
				visited[dep] = true
				walk(dep)
			}
		}
	}
	walk(name)
	return visited
}

// topoSort returns node names in topological order using Kahn's algorithm (O(V+E)).
// Returns an error if the graph contains a cycle.
func topoSort(nodes map[string]*Node) ([]string, error) {
	// inDegree[n] = number of dependencies n has.
	inDegree := make(map[string]int, len(nodes))
	// dependents[dep] = list of nodes that depend on dep.
	dependents := make(map[string][]string, len(nodes))
	for name := range nodes {
		inDegree[name] = 0
	}
	for _, n := range nodes {
		inDegree[n.Name] = len(n.DependsOn)
		for _, dep := range n.DependsOn {
			dependents[dep] = append(dependents[dep], n.Name)
		}
	}

	queue := make([]string, 0, len(nodes))
	for name, deg := range inDegree {
		if deg == 0 {
			queue = append(queue, name)
		}
	}

	order := make([]string, 0, len(nodes))
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		order = append(order, cur)
		for _, dependent := range dependents[cur] {
			inDegree[dependent]--
			if inDegree[dependent] == 0 {
				queue = append(queue, dependent)
			}
		}
	}

	if len(order) != len(nodes) {
		return nil, fmt.Errorf("cycle detected in job chain DAG")
	}
	return order, nil
}
