// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 fusion-platform contributors

// Package indexclient provides a minimal HTTP client for querying the
// fusion-index artifact registry. It is used by the operator to resolve
// artifact tags to concrete semver versions for code-source polling.
package indexclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	sigsyaml "sigs.k8s.io/yaml"
)

// ErrNotFound is returned when the artifact or tag does not exist in fusion-index.
var ErrNotFound = errors.New("not found in fusion-index")

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

// ResolveTag returns the semver string (e.g. "1.2.0") that tag points to for
// the named artifact in fusion-index. Returns ErrNotFound when the artifact or
// tag is absent. The baseURL must not have a trailing slash.
func ResolveTag(ctx context.Context, baseURL, artifactName, tag string) (string, error) {
	id, err := findArtifactID(ctx, baseURL, artifactName)
	if err != nil {
		return "", err
	}
	return resolveTagForID(ctx, baseURL, id, tag)
}

func findArtifactID(ctx context.Context, baseURL, artifactName string) (int64, error) {
	u := fmt.Sprintf("%s/api/v1/artifacts?name=%s", baseURL, url.QueryEscape(artifactName))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, fmt.Errorf("indexclient: build request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("indexclient: GET artifacts: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return 0, ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("indexclient: GET artifacts: unexpected status %d", resp.StatusCode)
	}

	var list artifactListResponse
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return 0, fmt.Errorf("indexclient: decode artifacts: %w", err)
	}
	for _, item := range list.Items {
		return item.ID, nil
	}
	return 0, ErrNotFound
}

// AppRunnerArg is a single named argument for an app runner (from metadata.yaml).
type AppRunnerArg struct {
	Name  string
	Value string
}

// AppRunner holds runner configuration parsed from an artifact's metadata.yaml.
type AppRunner struct {
	Type         string
	Port         int32
	Args         map[string]string
	BuilderImage string
}

// AppIngress holds ingress configuration parsed from an artifact's metadata.yaml.
type AppIngress struct {
	PathPrefix string
}

// AppMetadata is the subset of metadata.yaml fields the operator uses at runtime.
// Name and version are intentionally absent — those come from fusion-index directly.
type AppMetadata struct {
	Runner     AppRunner
	Ingress    AppIngress
	Resources  corev1.ResourceRequirements
	Maintainer string
}

// rawMetadata mirrors the metadata.yaml structure for JSON/YAML unmarshalling.
type rawMetadata struct {
	Maintainer string `yaml:"maintainer" json:"maintainer"`
	Runner     struct {
		Type         string            `yaml:"type" json:"type"`
		Port         int32             `yaml:"port" json:"port"`
		Args         map[string]string `yaml:"args" json:"args"`
		BuilderImage string            `yaml:"builderImage" json:"builderImage"`
	} `yaml:"runner" json:"runner"`
	Ingress struct {
		PathPrefix string `yaml:"pathPrefix" json:"pathPrefix"`
	} `yaml:"ingress" json:"ingress"`
	Resources struct {
		Requests map[string]string `yaml:"requests" json:"requests"`
		Limits   map[string]string `yaml:"limits" json:"limits"`
	} `yaml:"resources" json:"resources"`
}

// FetchAppMetadata resolves artifactName@tag in fusion-index, downloads the
// metadata.yaml file attached to that version, and returns the parsed AppMetadata.
// Name and version are not returned — callers must use ResolveTag for the version.
func FetchAppMetadata(ctx context.Context, baseURL, artifactName, tag string) (*AppMetadata, error) {
	meta, _, err := FetchAppMetadataAndVersion(ctx, baseURL, artifactName, tag)
	return meta, err
}

// FetchAppMetadataAndVersion is like FetchAppMetadata but also returns the
// resolved semver string, avoiding a separate ResolveTag round-trip.
func FetchAppMetadataAndVersion(ctx context.Context, baseURL, artifactName, tag string) (*AppMetadata, string, error) {
	id, err := findArtifactID(ctx, baseURL, artifactName)
	if err != nil {
		return nil, "", err
	}
	version, err := resolveTagForID(ctx, baseURL, id, tag)
	if err != nil {
		return nil, "", err
	}
	meta, err := fetchMetadataForVersion(ctx, baseURL, id, version)
	return meta, version, err
}

func fetchMetadataForVersion(ctx context.Context, baseURL string, artifactID int64, version string) (*AppMetadata, error) {
	u := fmt.Sprintf("%s/api/v1/artifacts/%d/versions/%s/files", baseURL, artifactID, version)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("indexclient: build request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("indexclient: GET files: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("indexclient: GET files: unexpected status %d", resp.StatusCode)
	}

	var files []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&files); err != nil {
		return nil, fmt.Errorf("indexclient: decode files: %w", err)
	}

	for _, f := range files {
		if f.Name != "metadata.yaml" {
			continue
		}
		data, dlErr := downloadFileBytes(ctx, baseURL, artifactID, version, f.ID)
		if dlErr != nil {
			return nil, dlErr
		}
		return parseMetadata(data)
	}
	return nil, fmt.Errorf("indexclient: metadata.yaml not found for artifact %d version %s", artifactID, version)
}

func downloadFileBytes(ctx context.Context, baseURL string, artifactID int64, version string, fileID int64) ([]byte, error) {
	u := fmt.Sprintf("%s/api/v1/artifacts/%d/versions/%s/files/%d/download", baseURL, artifactID, version, fileID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("indexclient: build download request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("indexclient: download file: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("indexclient: download file: unexpected status %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func parseMetadata(data []byte) (*AppMetadata, error) {
	var raw rawMetadata
	if err := sigsyaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("indexclient: parse metadata.yaml: %w", err)
	}

	meta := &AppMetadata{
		Maintainer: raw.Maintainer,
		Runner: AppRunner{
			Type:         raw.Runner.Type,
			Port:         raw.Runner.Port,
			Args:         raw.Runner.Args,
			BuilderImage: raw.Runner.BuilderImage,
		},
		Ingress: AppIngress{
			PathPrefix: raw.Ingress.PathPrefix,
		},
	}

	requests := corev1.ResourceList{}
	for k, v := range raw.Resources.Requests {
		qty, err := resource.ParseQuantity(v)
		if err == nil {
			requests[corev1.ResourceName(k)] = qty
		}
	}
	limits := corev1.ResourceList{}
	for k, v := range raw.Resources.Limits {
		qty, err := resource.ParseQuantity(v)
		if err == nil {
			limits[corev1.ResourceName(k)] = qty
		}
	}
	if len(requests) > 0 || len(limits) > 0 {
		meta.Resources = corev1.ResourceRequirements{
			Requests: requests,
			Limits:   limits,
		}
	}
	return meta, nil
}

func resolveTagForID(ctx context.Context, baseURL string, artifactID int64, tag string) (string, error) {
	u := fmt.Sprintf("%s/api/v1/artifacts/%d/versions", baseURL, artifactID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", fmt.Errorf("indexclient: build request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("indexclient: GET versions: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return "", ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("indexclient: GET versions: unexpected status %d", resp.StatusCode)
	}

	var versions []versionItem
	if err := json.NewDecoder(resp.Body).Decode(&versions); err != nil {
		return "", fmt.Errorf("indexclient: decode versions: %w", err)
	}

	for _, v := range versions {
		for _, t := range v.Tags {
			if t.Tag == tag {
				return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch), nil
			}
		}
	}
	return "", ErrNotFound
}
