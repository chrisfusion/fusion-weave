// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

// loader is the init container binary for code-source deploy steps.
// It resolves an artifact tag in fusion-index, downloads the archive, unpacks
// it to MOUNT_PATH, and writes MOUNT_PATH/.version with the resolved semver.
//
// Environment variables:
//
//	INDEX_URL      — fusion-index base URL (no trailing slash)
//	ARTIFACT_NAME  — full artifact name (e.g. "org.myteam.myapp")
//	ARTIFACT_TAG   — mutable tag to resolve (e.g. "stable")
//	MOUNT_PATH     — directory to unpack into (default: /weave-code)
package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
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

	version, fileID, fileName, err := resolveTagToFile(indexURL, artifactID, artifactTag)
	if err != nil {
		fatalf("resolve tag: %v", err)
	}
	fmt.Printf("loader: downloading version %s (file %d: %s)\n", version, fileID, fileName)

	data, err := downloadFile(indexURL, artifactID, version, fileID)
	if err != nil {
		fatalf("download: %v", err)
	}

	if err := os.MkdirAll(mountPath, 0755); err != nil {
		fatalf("mkdir %s: %v", mountPath, err)
	}

	if err := unpack(data, fileName, mountPath); err != nil {
		fatalf("unpack: %v", err)
	}

	versionFile := filepath.Join(mountPath, ".version")
	if err := os.WriteFile(versionFile, []byte(version), 0644); err != nil {
		fatalf("write .version: %v", err)
	}

	fmt.Printf("loader: ready — %s@%s unpacked to %s\n", artifactName, version, mountPath)
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

func resolveTagToFile(baseURL string, artifactID int64, tag string) (version string, fileID int64, fileName string, err error) {
	u := fmt.Sprintf("%s/api/v1/artifacts/%d/versions", baseURL, artifactID)
	var versions []versionItem
	if err = getJSON(u, &versions); err != nil {
		return
	}
	for _, v := range versions {
		for _, t := range v.Tags {
			if t.Tag == tag {
				version = fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
				fileID, fileName, err = firstFile(baseURL, artifactID, version)
				return
			}
		}
	}
	err = fmt.Errorf("tag %q not found for artifact %d", tag, artifactID)
	return
}

func firstFile(baseURL string, artifactID int64, version string) (int64, string, error) {
	u := fmt.Sprintf("%s/api/v1/artifacts/%d/versions/%s/files", baseURL, artifactID, version)
	var files []fileItem
	if err := getJSON(u, &files); err != nil {
		return 0, "", err
	}
	for _, f := range files {
		return f.ID, f.Name, nil
	}
	return 0, "", fmt.Errorf("no files for artifact %d version %s", artifactID, version)
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

// --- archive unpacking ---

func unpack(data []byte, fileName, destDir string) error {
	lower := strings.ToLower(fileName)
	switch {
	case strings.HasSuffix(lower, ".tar.gz") || strings.HasSuffix(lower, ".tgz"):
		return unpackTarGz(data, destDir)
	case strings.HasSuffix(lower, ".zip"):
		return unpackZip(data, destDir)
	default:
		// Unknown extension — write the raw file.
		out := filepath.Join(destDir, filepath.Base(fileName))
		return os.WriteFile(out, data, 0644)
	}
}

func unpackTarGz(data []byte, destDir string) error {
	gr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar next: %w", err)
		}
		target := filepath.Join(destDir, filepath.Clean(hdr.Name))
		// Guard against path traversal.
		if !strings.HasPrefix(target, filepath.Clean(destDir)+string(os.PathSeparator)) &&
			target != filepath.Clean(destDir) {
			return fmt.Errorf("tar: illegal path %q", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
		}
	}
	return nil
}

func unpackZip(data []byte, destDir string) error {
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("zip reader: %w", err)
	}
	for _, f := range r.File {
		target := filepath.Join(destDir, filepath.Clean(f.Name))
		if !strings.HasPrefix(target, filepath.Clean(destDir)+string(os.PathSeparator)) &&
			target != filepath.Clean(destDir) {
			return fmt.Errorf("zip: illegal path %q", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, f.Mode())
		if err != nil {
			rc.Close()
			return err
		}
		if _, err := io.Copy(out, rc); err != nil {
			rc.Close()
			out.Close()
			return err
		}
		rc.Close()
		out.Close()
	}
	return nil
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
