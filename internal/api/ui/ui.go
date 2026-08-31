// Package ui serves the built-in Watchtower web dashboard.
//
// The dashboard is a dependency-free single-page frontend embedded into the
// binary. It talks to the existing /v1/* HTTP API endpoints from the browser
// and never introduces its own data model: every action a user takes in the UI
// maps onto a documented API call, so enabling or disabling the dashboard does
// not change how Watchtower scans or updates containers.
package ui

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"path"
	"strings"
)

//go:embed all:assets
var assetsFS embed.FS

const (
	// BasePath is the mount point of the dashboard.
	BasePath = "/ui"
	// AssetsPath is the mount point of the dashboard's static assets.
	AssetsPath = BasePath + "/assets"
	// LoginPath accepts the dashboard login form submission.
	LoginPath = BasePath + "/login"
	// LogoutPath clears the dashboard session.
	LogoutPath = BasePath + "/logout"

	// indexTemplate is the dashboard page template name.
	indexTemplate = "index.html"
	// loginTemplate is the login page template name.
	loginTemplate = "login.html"

	// assetsDir is the embedded directory holding the frontend files.
	assetsDir = "assets"
)

// Config carries runtime values rendered into the dashboard page so the
// frontend can hide controls for endpoints that are not enabled.
type Config struct {
	// Version is the Watchtower version string shown in the header.
	Version string
	// Scope is the monitoring scope, shown in the header when non-empty.
	Scope string
	// ContainersAPI reports whether GET /v1/containers is available.
	ContainersAPI bool
	// CheckAPI reports whether POST /v1/check is available.
	CheckAPI bool
	// UpdateAPI reports whether POST /v1/update is available.
	UpdateAPI bool
	// MetricsAPI reports whether the metrics endpoint is available.
	MetricsAPI bool
	// HistoryAPI reports whether the scan history endpoint is available.
	HistoryAPI bool
	// HealthAPI reports whether the health probes are available.
	HealthAPI bool
	// EventsAPI reports whether the SSE event stream is available.
	EventsAPI bool
}

// ClientConfig is the JSON payload handed to the browser.
type ClientConfig struct {
	// BasePath is the dashboard mount point.
	BasePath string `json:"basePath"`
	// Version is the Watchtower version string.
	Version string `json:"version"`
	// Scope is the configured monitoring scope.
	Scope string `json:"scope"`
	// Endpoints maps endpoint names to their enabled state.
	Endpoints map[string]bool `json:"endpoints"`
	// APIBasePath is the shared prefix of the JSON API routes.
	APIBasePath string `json:"apiBasePath"`
	// EndpointPaths maps logical names to JSON API paths.
	EndpointPaths map[string]string `json:"endpointPaths"`
}

// Endpoints returns the enabled-endpoint map handed to the browser.
func (c Config) Endpoints() map[string]bool {
	return map[string]bool{
		"containers": c.ContainersAPI,
		"check":      c.CheckAPI,
		"update":     c.UpdateAPI,
		"metrics":    c.MetricsAPI,
		"history":    c.HistoryAPI,
		"health":     c.HealthAPI,
		"events":     c.EventsAPI,
	}
}

// JSON returns the client configuration as indented JSON.
//
// Parameters:
//   - none.
//
// Returns:
//   - template.JS: Escaping-safe JSON for embedding in a script tag.
//   - error: Non-nil if marshaling fails (it cannot for this shape).
func (c Config) JSON() (template.JS, error) {
	payload := ClientConfig{
		BasePath:    BasePath,
		Version:     c.Version,
		Scope:       c.Scope,
		Endpoints:   c.Endpoints(),
		APIBasePath: "/v1",
		EndpointPaths: map[string]string{
			"containers": "/v1/containers",
			"details":    "/v1/containers/details",
			"check":      "/v1/check",
			"update":     "/v1/update",
			"metrics":    "/v1/metrics",
			"history":    "/v1/history",
			"status":     "/v1/status",
		},
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal UI config: %w", err)
	}

	return template.JS(encoded), nil //nolint:gosec // Marshaled from a fixed struct, never user input.
}

// LoginData is the template data for the login page.
type LoginData struct {
	// ConfigJSON is the dashboard client configuration.
	ConfigJSON template.JS
	// Version is the Watchtower version string.
	Version string
	// Error is a human-readable login failure reason, empty on first render.
	Error string
}

// IndexData is the template data for the dashboard page.
type IndexData struct {
	// ConfigJSON is the dashboard client configuration.
	ConfigJSON template.JS
	// Version is the Watchtower version string.
	Version string
}

// Renderer renders the dashboard templates and serves embedded assets.
type Renderer struct {
	templates *template.Template
	assets    fs.FS
}

// New parses the embedded templates and exposes the embedded asset tree.
//
// Parameters:
//   - none.
//
// Returns:
//   - *Renderer: Ready-to-use renderer.
//   - error: Non-nil if the embedded templates cannot be parsed.
func New() (*Renderer, error) {
	templates, err := template.ParseFS(assetsFS, path.Join(assetsDir, "*.html"))
	if err != nil {
		return nil, fmt.Errorf("parse UI templates: %w", err)
	}

	assets, err := fs.Sub(assetsFS, assetsDir)
	if err != nil {
		return nil, fmt.Errorf("resolve UI assets: %w", err)
	}

	return &Renderer{templates: templates, assets: assets}, nil
}

// RenderLogin writes the login page.
//
// Parameters:
//   - w: Destination writer.
//   - data: Page data.
//
// Returns:
//   - error: Non-nil if rendering or writing fails.
func (r *Renderer) RenderLogin(w io.Writer, data LoginData) error {
	return r.render(w, loginTemplate, data)
}

// RenderIndex writes the dashboard page.
//
// Parameters:
//   - w: Destination writer.
//   - data: Page data.
//
// Returns:
//   - error: Non-nil if rendering or writing fails.
func (r *Renderer) RenderIndex(w io.Writer, data IndexData) error {
	return r.render(w, indexTemplate, data)
}

// render executes a named template into a buffer before writing so a
// half-rendered page never reaches the client on error.
func (r *Renderer) render(w io.Writer, name string, data any) error {
	var buf bytes.Buffer

	err := r.templates.ExecuteTemplate(&buf, name, data)
	if err != nil {
		return fmt.Errorf("render %s: %w", name, err)
	}

	_, err = buf.WriteTo(w)
	if err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}

	return nil
}

// Asset returns the contents and content type of an embedded asset.
//
// Names are resolved relative to the asset root and must not escape it, so
// callers can pass the wildcard segment of a request path directly.
//
// Parameters:
//   - name: Asset path relative to the asset root (for example "app.js").
//
// Returns:
//   - []byte: Asset contents.
//   - string: Content type for the asset.
//   - error: Non-nil when the name is invalid or the asset is missing.
func (r *Renderer) Asset(name string) ([]byte, string, error) {
	cleaned := sanitizeAssetName(name)
	if cleaned == "" {
		return nil, "", fmt.Errorf("%w: %q", fs.ErrNotExist, name)
	}

	contents, err := fs.ReadFile(r.assets, cleaned)
	if err != nil {
		return nil, "", fmt.Errorf("read asset %q: %w", cleaned, err)
	}

	return contents, ContentType(cleaned), nil
}

// sanitizeAssetName normalizes a requested asset path and rejects traversal.
//
// Parameters:
//   - name: Raw asset path from the request.
//
// Returns:
//   - string: Root-relative asset path, or empty when the path is invalid.
func sanitizeAssetName(name string) string {
	// Reject traversal up front: path.Clean would otherwise dissolve ".."
	// segments into a result that looks harmless.
	if strings.Contains(name, "..") {
		return ""
	}

	trimmed := strings.TrimPrefix(path.Clean("/"+name), "/")
	if trimmed == "" || trimmed == "." {
		return ""
	}

	if !fs.ValidPath(trimmed) {
		return ""
	}

	return trimmed
}

// ContentType maps an asset file name to its response content type.
//
// Unknown extensions fall back to application/octet-stream, which makes
// browsers download instead of executing the payload.
//
// Parameters:
//   - name: Asset file name.
//
// Returns:
//   - string: Content type value.
func ContentType(name string) string {
	switch path.Ext(name) {
	case ".css":
		return "text/css; charset=utf-8"
	case ".js":
		return "text/javascript; charset=utf-8"
	case ".html":
		return "text/html; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".ico":
		return "image/x-icon"
	case ".json":
		return "application/json; charset=utf-8"
	default:
		return "application/octet-stream"
	}
}
