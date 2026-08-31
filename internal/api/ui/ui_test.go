package ui

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNew(t *testing.T) {
	renderer, err := New()

	require.NoError(t, err)
	require.NotNil(t, renderer)
}

func TestRenderer_RendersLoginPage(t *testing.T) {
	renderer, err := New()
	require.NoError(t, err)

	var out strings.Builder

	err = renderer.RenderLogin(&out, LoginData{
		ConfigJSON: `{"basePath":"/ui"}`,
		Version:    "1.0.0",
		Error:      "nope",
	})
	require.NoError(t, err)

	body := out.String()
	assert.Contains(t, body, "Sign in")
	assert.Contains(t, body, "nope")
	assert.Contains(t, body, `action="/ui/login"`)
	assert.Contains(t, body, `{"basePath":"/ui"}`)
}

func TestRenderer_RendersLoginPageWithoutError(t *testing.T) {
	renderer, err := New()
	require.NoError(t, err)

	var out strings.Builder

	require.NoError(t, renderer.RenderLogin(&out, LoginData{ConfigJSON: "{}"}))

	assert.NotContains(t, out.String(), `role="alert"`,
		"a clean render must not include the error banner")
}

func TestRenderer_RendersDashboardPage(t *testing.T) {
	renderer, err := New()
	require.NoError(t, err)

	var out strings.Builder

	err = renderer.RenderIndex(&out, IndexData{
		ConfigJSON: `{"version":"2.0.0"}`,
		Version:    "2.0.0",
	})
	require.NoError(t, err)

	body := out.String()
	assert.Contains(t, body, "container-table")
	assert.Contains(t, body, `{"version":"2.0.0"}`)
	assert.Contains(t, body, "/ui/assets/app.js")
	assert.NotContains(t, body, `name="token"`,
		"the dashboard page must not include the login form")
}

func TestRenderer_UnknownTemplateIsAnError(t *testing.T) {
	renderer, err := New()
	require.NoError(t, err)

	var out strings.Builder

	err = renderer.RenderIndex(&out, IndexData{})
	require.NoError(t, err)

	assert.Error(t, renderer.render(&out, "missing.html", nil))
}

func TestRenderer_Asset(t *testing.T) {
	renderer, err := New()
	require.NoError(t, err)

	t.Run("serves embedded files", func(t *testing.T) {
		for name, wantType := range map[string]string{
			"app.css":     "text/css; charset=utf-8",
			"app.js":      "text/javascript; charset=utf-8",
			"favicon.svg": "image/svg+xml",
		} {
			contents, contentType, err := renderer.Asset(name)

			require.NoError(t, err, name)
			assert.NotEmpty(t, contents, name)
			assert.Equal(t, wantType, contentType, name)
		}
	})

	t.Run("rejects missing files", func(t *testing.T) {
		_, _, err := renderer.Asset("nope.txt")

		assert.Error(t, err)
	})

	t.Run("rejects traversal", func(t *testing.T) {
		for _, name := range []string{
			"../../go.mod",
			"..%2F..%2Fgo.mod",
			"./../../etc/passwd",
			"",
			"/",
		} {
			_, _, err := renderer.Asset(name)

			assert.Error(t, err, name)
		}
	})
}

func TestSanitizeAssetName(t *testing.T) {
	cases := map[string]string{
		"app.js":           "app.js",
		"/app.js":          "app.js",
		"./app.js":         "app.js",
		"app.css":          "app.css",
		"../app.js":        "",
		"../../etc/passwd": "",
		"sub/../../app.js": "",
		"":                 "",
		".":                "",
		"/":                "",
	}

	for input, want := range cases {
		t.Run(input, func(t *testing.T) {
			assert.Equal(t, want, sanitizeAssetName(input))
		})
	}
}

func TestContentType(t *testing.T) {
	cases := map[string]string{
		"a.css":       "text/css; charset=utf-8",
		"a.js":        "text/javascript; charset=utf-8",
		"a.html":      "text/html; charset=utf-8",
		"a.svg":       "image/svg+xml",
		"a.ico":       "image/x-icon",
		"a.json":      "application/json; charset=utf-8",
		"a.bin":       "application/octet-stream",
		"noextension": "application/octet-stream",
	}

	for input, want := range cases {
		t.Run(input, func(t *testing.T) {
			assert.Equal(t, want, ContentType(input))
		})
	}
}

func TestConfigEndpoints(t *testing.T) {
	cfg := Config{
		Version:       "1.2.3",
		Scope:         "prod",
		ContainersAPI: true,
		CheckAPI:      true,
	}

	endpoints := cfg.Endpoints()

	assert.True(t, endpoints["containers"])
	assert.True(t, endpoints["check"])
	assert.False(t, endpoints["update"], "disabled endpoints must stay false, not absent")
}

func TestConfigJSON(t *testing.T) {
	cfg := Config{
		Version:       "1.2.3",
		Scope:         "prod",
		ContainersAPI: true,
		UpdateAPI:     true,
	}

	encoded, err := cfg.JSON()
	require.NoError(t, err)

	body := string(encoded)
	assert.Contains(t, body, `"basePath":"/ui"`)
	assert.Contains(t, body, `"version":"1.2.3"`)
	assert.Contains(t, body, `"scope":"prod"`)
	assert.Contains(t, body, `"containers":true`)
	assert.Contains(t, body, `"update":true`)
	assert.Contains(t, body, `"/v1/containers/details"`)
}

func TestBasePaths(t *testing.T) {
	assert.Equal(t, "/ui", BasePath)
	assert.Equal(t, "/ui/assets", AssetsPath)
	assert.Equal(t, "/ui/login", LoginPath)
	assert.Equal(t, "/ui/logout", LogoutPath)
}
