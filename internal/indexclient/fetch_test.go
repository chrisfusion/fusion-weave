// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

package indexclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// happyMux returns a ServeMux that simulates a reachable fusion-index for
// artifact "myapp", tag "stable", version "1.2.3", with a single metadata.yaml.
func happyMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/artifacts", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("name") != "myapp" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"items":[{"id":42}]}`)) //nolint:errcheck
	})
	mux.HandleFunc("GET /api/v1/artifacts/42/versions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"major":1,"minor":2,"patch":3,"tags":[{"tag":"stable"}]}]`)) //nolint:errcheck
	})
	mux.HandleFunc("GET /api/v1/artifacts/42/versions/1.2.3/files", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"id":7,"name":"metadata.yaml"}]`)) //nolint:errcheck
	})
	mux.HandleFunc("GET /api/v1/artifacts/42/versions/1.2.3/files/7/download", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("runner:\n  type: python\n  port: 8080\nmaintainer: test@example.com\n")) //nolint:errcheck
	})
	return mux
}

// ---- FetchAppMetadataAndVersion ----

func TestFetchAppMetadataAndVersion_HappyPath(t *testing.T) {
	ts := httptest.NewServer(happyMux())
	defer ts.Close()

	meta, version, err := FetchAppMetadataAndVersion(context.Background(), ts.URL, "myapp", "stable")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if version != "1.2.3" {
		t.Errorf("version: got %q, want 1.2.3", version)
	}
	if meta.Runner.Type != "python" {
		t.Errorf("runner.type: got %q, want python", meta.Runner.Type)
	}
	if meta.Runner.Port != 8080 {
		t.Errorf("runner.port: got %d, want 8080", meta.Runner.Port)
	}
	if meta.Maintainer != "test@example.com" {
		t.Errorf("maintainer: got %q, want test@example.com", meta.Maintainer)
	}
}

func TestFetchAppMetadataAndVersion_ArtifactNotFound_404(t *testing.T) {
	// Index returns 404 for the artifact list → ErrNotFound.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer ts.Close()

	_, _, err := FetchAppMetadataAndVersion(context.Background(), ts.URL, "myapp", "stable")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound for 404 artifact endpoint, got %v", err)
	}
}

func TestFetchAppMetadataAndVersion_ArtifactListEmpty(t *testing.T) {
	// Index returns 200 with an empty items list → ErrNotFound.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"items":[]}`)) //nolint:errcheck
	}))
	defer ts.Close()

	_, _, err := FetchAppMetadataAndVersion(context.Background(), ts.URL, "myapp", "stable")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound for empty artifact list, got %v", err)
	}
}

func TestFetchAppMetadataAndVersion_TagNotFound(t *testing.T) {
	// Artifact exists and has versions, but none carry the requested tag.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/artifacts", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"items":[{"id":42}]}`)) //nolint:errcheck
	})
	mux.HandleFunc("GET /api/v1/artifacts/42/versions", func(w http.ResponseWriter, r *http.Request) {
		// Version tagged "dev", not "stable"
		w.Write([]byte(`[{"major":1,"minor":0,"patch":0,"tags":[{"tag":"dev"}]}]`)) //nolint:errcheck
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	_, _, err := FetchAppMetadataAndVersion(context.Background(), ts.URL, "myapp", "stable")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound when tag absent from all versions, got %v", err)
	}
}

func TestFetchAppMetadataAndVersion_NoVersionsAtAll(t *testing.T) {
	// Artifact exists but has no versions at all.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/artifacts", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"items":[{"id":42}]}`)) //nolint:errcheck
	})
	mux.HandleFunc("GET /api/v1/artifacts/42/versions", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[]`)) //nolint:errcheck
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	_, _, err := FetchAppMetadataAndVersion(context.Background(), ts.URL, "myapp", "stable")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound when versions list is empty, got %v", err)
	}
}

func TestFetchAppMetadataAndVersion_VersionsEndpoint404(t *testing.T) {
	// Artifact found, but versions endpoint returns 404.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/artifacts", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"items":[{"id":42}]}`)) //nolint:errcheck
	})
	mux.HandleFunc("GET /api/v1/artifacts/42/versions", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	_, _, err := FetchAppMetadataAndVersion(context.Background(), ts.URL, "myapp", "stable")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound when versions endpoint returns 404, got %v", err)
	}
}

func TestFetchAppMetadataAndVersion_MetadataYAMLMissing(t *testing.T) {
	// Tag resolves to a version but the file list contains no metadata.yaml.
	// This is a hard error — NOT ErrNotFound, because the artifact and version exist;
	// the metadata.yaml is simply absent from this release's files.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/artifacts", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"items":[{"id":42}]}`)) //nolint:errcheck
	})
	mux.HandleFunc("GET /api/v1/artifacts/42/versions", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"major":1,"minor":0,"patch":0,"tags":[{"tag":"stable"}]}]`)) //nolint:errcheck
	})
	mux.HandleFunc("GET /api/v1/artifacts/42/versions/1.0.0/files", func(w http.ResponseWriter, r *http.Request) {
		// No metadata.yaml — only a source archive
		w.Write([]byte(`[{"id":1,"name":"app.tar.gz"}]`)) //nolint:errcheck
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	_, _, err := FetchAppMetadataAndVersion(context.Background(), ts.URL, "myapp", "stable")
	if err == nil {
		t.Fatal("expected error when metadata.yaml absent from file list, got nil")
	}
	if errors.Is(err, ErrNotFound) {
		t.Error("missing metadata.yaml should return a distinct error, not ErrNotFound")
	}
}

func TestFetchAppMetadataAndVersion_FilesEndpointError(t *testing.T) {
	// Tag resolves but the files list endpoint returns a server error.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/artifacts", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"items":[{"id":42}]}`)) //nolint:errcheck
	})
	mux.HandleFunc("GET /api/v1/artifacts/42/versions", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"major":1,"minor":0,"patch":0,"tags":[{"tag":"stable"}]}]`)) //nolint:errcheck
	})
	mux.HandleFunc("GET /api/v1/artifacts/42/versions/1.0.0/files", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "storage unavailable", http.StatusInternalServerError)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	_, _, err := FetchAppMetadataAndVersion(context.Background(), ts.URL, "myapp", "stable")
	if err == nil {
		t.Fatal("expected error when files endpoint returns 500, got nil")
	}
}

func TestFetchAppMetadataAndVersion_DownloadEndpointError(t *testing.T) {
	// metadata.yaml is listed but its download endpoint returns 500.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/artifacts", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"items":[{"id":42}]}`)) //nolint:errcheck
	})
	mux.HandleFunc("GET /api/v1/artifacts/42/versions", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"major":1,"minor":0,"patch":0,"tags":[{"tag":"stable"}]}]`)) //nolint:errcheck
	})
	mux.HandleFunc("GET /api/v1/artifacts/42/versions/1.0.0/files", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"id":7,"name":"metadata.yaml"}]`)) //nolint:errcheck
	})
	mux.HandleFunc("GET /api/v1/artifacts/42/versions/1.0.0/files/7/download", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "blob storage unavailable", http.StatusInternalServerError)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	_, _, err := FetchAppMetadataAndVersion(context.Background(), ts.URL, "myapp", "stable")
	if err == nil {
		t.Fatal("expected error when download endpoint returns 500, got nil")
	}
}

func TestFetchAppMetadataAndVersion_IndexUnreachable(t *testing.T) {
	// Start a server, close it immediately, then make the call.
	// The client must get a network-level error, not ErrNotFound.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	ts.Close()

	_, _, err := FetchAppMetadataAndVersion(context.Background(), ts.URL, "myapp", "stable")
	if err == nil {
		t.Fatal("expected error when index is unreachable, got nil")
	}
	if errors.Is(err, ErrNotFound) {
		t.Error("unreachable index must not be reported as ErrNotFound")
	}
}

func TestFetchAppMetadataAndVersion_InvalidArtifactListJSON(t *testing.T) {
	// Index returns 200 but the body is not valid JSON.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`not json at all`)) //nolint:errcheck
	}))
	defer ts.Close()

	_, _, err := FetchAppMetadataAndVersion(context.Background(), ts.URL, "myapp", "stable")
	if err == nil {
		t.Fatal("expected error for invalid JSON in artifact list, got nil")
	}
}

func TestFetchAppMetadataAndVersion_InvalidVersionsJSON(t *testing.T) {
	// Artifact found, versions endpoint returns invalid JSON.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/artifacts", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"items":[{"id":42}]}`)) //nolint:errcheck
	})
	mux.HandleFunc("GET /api/v1/artifacts/42/versions", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{broken`)) //nolint:errcheck
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	_, _, err := FetchAppMetadataAndVersion(context.Background(), ts.URL, "myapp", "stable")
	if err == nil {
		t.Fatal("expected error for invalid JSON in versions response, got nil")
	}
}

func TestFetchAppMetadataAndVersion_InvalidFilesJSON(t *testing.T) {
	// Files list endpoint returns invalid JSON.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/artifacts", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"items":[{"id":42}]}`)) //nolint:errcheck
	})
	mux.HandleFunc("GET /api/v1/artifacts/42/versions", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"major":1,"minor":0,"patch":0,"tags":[{"tag":"stable"}]}]`)) //nolint:errcheck
	})
	mux.HandleFunc("GET /api/v1/artifacts/42/versions/1.0.0/files", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[not json`)) //nolint:errcheck
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	_, _, err := FetchAppMetadataAndVersion(context.Background(), ts.URL, "myapp", "stable")
	if err == nil {
		t.Fatal("expected error for invalid JSON in files response, got nil")
	}
}

func TestFetchAppMetadataAndVersion_InvalidMetadataYAMLContent(t *testing.T) {
	// metadata.yaml listed and downloadable, but its content is not valid YAML.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/artifacts", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"items":[{"id":42}]}`)) //nolint:errcheck
	})
	mux.HandleFunc("GET /api/v1/artifacts/42/versions", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"major":1,"minor":0,"patch":0,"tags":[{"tag":"stable"}]}]`)) //nolint:errcheck
	})
	mux.HandleFunc("GET /api/v1/artifacts/42/versions/1.0.0/files", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"id":7,"name":"metadata.yaml"}]`)) //nolint:errcheck
	})
	mux.HandleFunc("GET /api/v1/artifacts/42/versions/1.0.0/files/7/download", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("runner: [not: closed")) //nolint:errcheck
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	_, _, err := FetchAppMetadataAndVersion(context.Background(), ts.URL, "myapp", "stable")
	if err == nil {
		t.Fatal("expected error for invalid YAML in metadata.yaml content, got nil")
	}
}

func TestFetchAppMetadataAndVersion_ArtifactsEndpointNon200(t *testing.T) {
	// Index returns an unexpected non-404 error status (e.g. 503 Service Unavailable).
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
	}))
	defer ts.Close()

	_, _, err := FetchAppMetadataAndVersion(context.Background(), ts.URL, "myapp", "stable")
	if err == nil {
		t.Fatal("expected error for 503 from artifacts endpoint, got nil")
	}
	if errors.Is(err, ErrNotFound) {
		t.Error("503 error must not be reported as ErrNotFound")
	}
}

// ---- ResolveTag ----

func TestResolveTag_HappyPath(t *testing.T) {
	ts := httptest.NewServer(happyMux())
	defer ts.Close()

	version, err := ResolveTag(context.Background(), ts.URL, "myapp", "stable")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if version != "1.2.3" {
		t.Errorf("version: got %q, want 1.2.3", version)
	}
}

func TestResolveTag_ArtifactNotFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer ts.Close()

	_, err := ResolveTag(context.Background(), ts.URL, "myapp", "stable")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestResolveTag_TagAbsent(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/artifacts", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"items":[{"id":42}]}`)) //nolint:errcheck
	})
	mux.HandleFunc("GET /api/v1/artifacts/42/versions", func(w http.ResponseWriter, r *http.Request) {
		// Two versions with different tags — neither is "stable"
		w.Write([]byte(`[` +
			`{"major":1,"minor":0,"patch":0,"tags":[{"tag":"dev"}]},` +
			`{"major":2,"minor":0,"patch":0,"tags":[{"tag":"rc"}]}` +
			`]`)) //nolint:errcheck
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	_, err := ResolveTag(context.Background(), ts.URL, "myapp", "stable")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound when tag absent, got %v", err)
	}
}

func TestResolveTag_IndexUnreachable(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	ts.Close()

	_, err := ResolveTag(context.Background(), ts.URL, "myapp", "stable")
	if err == nil {
		t.Fatal("expected error when index unreachable, got nil")
	}
	if errors.Is(err, ErrNotFound) {
		t.Error("unreachable index must not be reported as ErrNotFound")
	}
}

func TestResolveTag_MultipleVersionsOneMatch(t *testing.T) {
	// Multiple versions in the list — only one carries the target tag.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/artifacts", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"items":[{"id":42}]}`)) //nolint:errcheck
	})
	mux.HandleFunc("GET /api/v1/artifacts/42/versions", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[` +
			`{"major":1,"minor":0,"patch":0,"tags":[{"tag":"old"}]},` +
			`{"major":2,"minor":3,"patch":1,"tags":[{"tag":"stable"},{"tag":"latest"}]},` +
			`{"major":3,"minor":0,"patch":0,"tags":[{"tag":"dev"}]}` +
			`]`)) //nolint:errcheck
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	version, err := ResolveTag(context.Background(), ts.URL, "myapp", "stable")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if version != "2.3.1" {
		t.Errorf("version: got %q, want 2.3.1", version)
	}
}
