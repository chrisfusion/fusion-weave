// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package apiserver

// APIError is the standard JSON error response body returned by the API.
type APIError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}
