// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package trigger_test

import (
	"testing"

	"fusion-platform.io/fusion-weave/internal/trigger"
)

func TestSanitizeJobID(t *testing.T) {
	cases := []struct {
		name string
		id   string
		want string
	}{
		{"already clean", "job-1", "job-1"},
		{"uppercase and spaces", "My Job 1", "my-job-1"},
		{"each invalid char becomes its own dash", "job/../weird:id", "job----weird-id"},
		{"empty falls back", "", "job"},
		{"only symbols falls back", "!!!", "job"},
		// Kubernetes label values and GenerateName prefixes are DNS-1123
		// ([a-z0-9-]) — a non-ASCII Unicode letter must NOT pass through, unlike
		// unicode.IsLetter/IsDigit which would wrongly admit it.
		{"non-ASCII letters are replaced, not kept", "café-batch", "caf--batch"},
		{"non-ASCII digits are replaced, not kept", "job-١٢٣", "job"}, // Arabic-Indic digits
		{"CJK is replaced, not kept", "job-日本語", "job"},
		{"truncated to 32 chars", "abcdefghijklmnopqrstuvwxyz0123456789", "abcdefghijklmnopqrstuvwxyz012345"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := trigger.SanitizeJobID(tc.id)
			if got != tc.want {
				t.Errorf("SanitizeJobID(%q) = %q, want %q", tc.id, got, tc.want)
			}
			for _, r := range got {
				if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-') {
					t.Errorf("SanitizeJobID(%q) = %q contains non-DNS-1123 rune %q", tc.id, got, r)
				}
			}
		})
	}
}
