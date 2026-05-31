// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

// loader is the init container binary for code-source deploy steps.
// It resolves an artifact tag in fusion-index, downloads each file, copies it
// as-is to MOUNT_PATH, and writes MOUNT_PATH/.version with the resolved semver.
// No archive extraction is performed — the container image handles that itself.
//
// Environment variables:
//
//	INDEX_URL      — fusion-index base URL (no trailing slash)
//	ARTIFACT_NAME  — full artifact name (e.g. "org.myteam.myapp")
//	ARTIFACT_TAG   — mutable tag to resolve (e.g. "stable")
//	MOUNT_PATH     — directory to copy files into (default: /weave-code)
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
)

func main() {
	indexURL := mustEnv("INDEX_URL")
	artifactName := mustEnv("ARTIFACT_NAME")
	artifactTag := mustEnv("ARTIFACT_TAG")
	mountPath := envOr("MOUNT_PATH", "/weave-code")

	fmt.Printf("loader: resolving %s@%s from %s\n", artifactName, artifactTag, indexURL)

	artifactID, err := findArtifactID(indexURL, artifactName)
	if err != nil {
		fatalf("find artifact: %v", err)
	}

	version, files, err := resolveTagToFiles(indexURL, artifactID, artifactTag)
	if err != nil {
		fatalf("resolve tag: %v", err)
	}
	fmt.Printf("loader: downloading version %s (%d file(s))\n", version, len(files))

	if err := os.MkdirAll(mountPath, 0755); err != nil {
		fatalf("mkdir %s: %v", mountPath, err)
	}

	for _, f := range files {
		data, err := downloadFile(indexURL, artifactID, version, f.ID)
		if err != nil {
			fatalf("download %s: %v", f.Name, err)
		}
		dest := filepath.Join(mountPath, filepath.Base(f.Name))
		if err := os.WriteFile(dest, data, 0644); err != nil {
			fatalf("write %s: %v", f.Name, err)
		}
		fmt.Printf("loader: copied %s\n", f.Name)
	}

	// Write .version from the index-resolved semver — not from metadata.yaml content.
	versionFile := filepath.Join(mountPath, ".version")
	if err := os.WriteFile(versionFile, []byte(version), 0644); err != nil {
		fatalf("write .version: %v", err)
	}

	fmt.Printf("loader: ready — %s@%s copied to %s\n", artifactName, version, mountPath)
}

// --- fusion-index API helpers ---

type artifactItem struct {
	ID int64 `json:"id"`
}

type artifactListResponse struct {
	Items []artifactItem `json:"items"`
}

type tagItem struct {
	Tag string `json:"tag"`
}

type versionItem struct {
	Major int       `json:"major"`
	Minor int       `json:"minor"`
	Patch int       `json:"patch"`
	Tags  []tagItem `json:"tags"`
}

type fileItem struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

func findArtifactID(baseURL, artifactName string) (int64, error) {
	u := fmt.Sprintf("%s/api/v1/artifacts?name=%s", baseURL, url.QueryEscape(artifactName))
	var list artifactListResponse
	if err := getJSON(u, &list); err != nil {
		return 0, err
	}
	for _, item := range list.Items {
		return item.ID, nil
	}
	return 0, fmt.Errorf("artifact %q not found", artifactName)
}

func resolveTagToFiles(baseURL string, artifactID int64, tag string) (version string, files []fileItem, err error) {
	u := fmt.Sprintf("%s/api/v1/artifacts/%d/versions", baseURL, artifactID)
	var versions []versionItem
	if err = getJSON(u, &versions); err != nil {
		return
	}
	for _, v := range versions {
		for _, t := range v.Tags {
			if t.Tag == tag {
				version = fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
				fu := fmt.Sprintf("%s/api/v1/artifacts/%d/versions/%s/files", baseURL, artifactID, version)
				if err = getJSON(fu, &files); err != nil {
					return
				}
				if len(files) == 0 {
					err = fmt.Errorf("no files for artifact %d version %s", artifactID, version)
				}
				return
			}
		}
	}
	err = fmt.Errorf("tag %q not found for artifact %d", tag, artifactID)
	return
}

func downloadFile(baseURL string, artifactID int64, version string, fileID int64) ([]byte, error) {
	u := fmt.Sprintf("%s/api/v1/artifacts/%d/versions/%s/files/%d/download",
		baseURL, artifactID, version, fileID)
	resp, err := http.Get(u) //nolint:noctx
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", u, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: status %d", u, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func getJSON(u string, dst any) error {
	resp, err := http.Get(u) //nolint:noctx
	if err != nil {
		return fmt.Errorf("GET %s: %w", u, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("GET %s: 404 not found", u)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: status %d", u, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(dst)
}

// --- helpers ---

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		fatalf("required environment variable %s is not set", key)
	}
	return v
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "loader: "+format+"\n", args...)
	os.Exit(1)
}
