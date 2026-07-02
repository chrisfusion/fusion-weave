// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package trigger

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/robfig/cron/v3"
	corev1 "k8s.io/api/core/v1"
	"gopkg.in/yaml.v3"
)

// BatchJob is a scheduler-ready representation of one entry from the batch YAML.
type BatchJob struct {
	ID        string
	Schedule  cron.Schedule
	NotBefore time.Time
	EnvVars   []corev1.EnvVar
}

// ValidationError describes one problem found while validating the batch YAML.
type ValidationError struct {
	Line    int    `json:"line"`
	Message string `json:"message"`
}

// standardCronParser parses 5-field standard cron expressions (no seconds field).
var standardCronParser = cron.NewParser(
	cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor,
)

// ParseBatchJobs parses the batch job YAML. Valid jobs are returned together with
// any per-entry validation errors. Entries with errors are skipped.
func ParseBatchJobs(content string) ([]BatchJob, []ValidationError) {
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(content), &root); err != nil {
		return nil, []ValidationError{{Line: 1, Message: "YAML parse error: " + err.Error()}}
	}
	if root.Kind == 0 {
		return nil, nil
	}

	doc := &root
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		doc = doc.Content[0]
	}
	if doc.Kind != yaml.SequenceNode {
		return nil, []ValidationError{{Line: doc.Line, Message: "expected a YAML sequence (list of job entries)"}}
	}

	var jobs []BatchJob
	var errs []ValidationError

	for _, item := range doc.Content {
		itemLine := item.Line
		jobNode := mappingChild(item, "job")
		if jobNode == nil {
			errs = append(errs, ValidationError{Line: itemLine, Message: "missing 'job' key"})
			continue
		}

		job, jobErrs := decodeJob(jobNode)
		errs = append(errs, jobErrs...)
		if len(jobErrs) > 0 {
			continue
		}
		jobs = append(jobs, job)
	}

	return jobs, errs
}

// decodeJob parses one job mapping node into a BatchJob.
func decodeJob(node *yaml.Node) (BatchJob, []ValidationError) {
	line := node.Line
	var errs []ValidationError

	id := nodeRawValue(node, "id")
	name := nodeRawValue(node, "name")
	topic := nodeRawValue(node, "topic")
	maintainer := nodeRawValue(node, "maintainer")
	startDate := nodeRawValue(node, "startdate")
	startTime := nodeRawValue(node, "starttime")
	scheduleStr := nodeRawValue(node, "schedule")

	if id == "" {
		errs = append(errs, ValidationError{Line: line, Message: "id is required"})
	}
	if scheduleStr == "" {
		errs = append(errs, ValidationError{Line: line, Message: "schedule is required"})
	}

	var sched cron.Schedule
	if scheduleStr != "" {
		var err error
		sched, err = standardCronParser.Parse(scheduleStr)
		if err != nil {
			errs = append(errs, ValidationError{Line: line, Message: fmt.Sprintf("invalid schedule %q: %v", scheduleStr, err)})
		}
	}

	if len(errs) > 0 {
		return BatchJob{}, errs
	}

	var notBefore time.Time
	if startDate != "" {
		ts := startDate
		if startTime != "" {
			ts += " " + startTime
		}
		layout := "2006-01-02"
		if startTime != "" {
			layout = "2006-01-02 15:04"
		}
		t, err := time.Parse(layout, ts)
		if err != nil {
			errs = append(errs, ValidationError{Line: line, Message: fmt.Sprintf("invalid startdate/starttime %q: %v", ts, err)})
			return BatchJob{}, errs
		}
		notBefore = t
	}

	// Metadata: decode the raw YAML node to interface{} then JSON-encode for JOB_METADATA.
	metaJSON := ""
	if metaNode := mappingChild(node, "metadata"); metaNode != nil {
		var m interface{}
		if err := metaNode.Decode(&m); err == nil {
			if b, err := json.Marshal(m); err == nil {
				metaJSON = string(b)
			}
		}
	}

	envVars := []corev1.EnvVar{
		{Name: "JOB_ID", Value: id},
		{Name: "JOB_NAME", Value: name},
		{Name: "JOB_TOPIC", Value: topic},
		{Name: "JOB_MAINTAINER", Value: maintainer},
		{Name: "JOB_STARTDATE", Value: startDate},
		{Name: "JOB_STARTTIME", Value: startTime},
		{Name: "JOB_SCHEDULE", Value: scheduleStr},
		{Name: "JOB_METADATA", Value: metaJSON},
	}

	return BatchJob{
		ID:        id,
		Schedule:  sched,
		NotBefore: notBefore,
		EnvVars:   envVars,
	}, nil
}

// mappingChild returns the value node for the given key in a YAML mapping node, or nil.
func mappingChild(node *yaml.Node, key string) *yaml.Node {
	if node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

// nodeRawValue returns the raw scalar string for the given key in a YAML mapping node.
// Using the node's Value field preserves the literal string as written, e.g. "00001".
func nodeRawValue(node *yaml.Node, key string) string {
	child := mappingChild(node, key)
	if child == nil {
		return ""
	}
	return child.Value
}

// SanitizeJobID makes a job ID safe for use inside a Kubernetes resource name.
// It lowercases the string and replaces any non-alphanumeric character with '-',
// trims leading/trailing dashes, and truncates to 32 characters.
func SanitizeJobID(id string) string {
	var sb strings.Builder
	for _, r := range strings.ToLower(id) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			sb.WriteRune(r)
		} else {
			sb.WriteRune('-')
		}
	}
	s := strings.Trim(sb.String(), "-")
	if len(s) > 32 {
		s = s[:32]
	}
	if s == "" {
		s = "job"
	}
	return s
}
