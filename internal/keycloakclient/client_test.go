// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package keycloakclient_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"fusion-platform.io/fusion-weave/internal/keycloakclient"
)

func TestMintClientCredentialsToken_Success(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /realms/fusion/protocol/openid-connect/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		if r.FormValue("grant_type") != "client_credentials" {
			t.Errorf("expected grant_type=client_credentials, got %q", r.FormValue("grant_type"))
		}
		if r.FormValue("client_id") != "demo-client" {
			t.Errorf("expected client_id=demo-client, got %q", r.FormValue("client_id"))
		}
		if r.FormValue("client_secret") != "demo-secret" {
			t.Errorf("expected client_secret=demo-secret, got %q", r.FormValue("client_secret"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"tok-123","expires_in":300,"token_type":"Bearer"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	tok, err := keycloakclient.MintClientCredentialsToken(context.Background(), srv.URL+"/realms/fusion/protocol/openid-connect/token", "demo-client", "demo-secret")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok.AccessToken != "tok-123" {
		t.Errorf("access token: got %q, want tok-123", tok.AccessToken)
	}
	if tok.ExpiresIn != 300 {
		t.Errorf("expires_in: got %d, want 300", tok.ExpiresIn)
	}
}

func TestMintClientCredentialsToken_NonOKStatus(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	_, err := keycloakclient.MintClientCredentialsToken(context.Background(), srv.URL+"/token", "bad-client", "bad-secret")
	if err == nil {
		t.Fatal("expected error for non-200 status")
	}
	if !errors.Is(err, keycloakclient.ErrTokenRequestFailed) {
		t.Errorf("expected ErrTokenRequestFailed, got %v", err)
	}
}
