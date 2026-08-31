package routes

import (
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/nicholas-fedor/watchtower/internal/api/config"
	"github.com/nicholas-fedor/watchtower/internal/api/ui"
	mockContainer "github.com/nicholas-fedor/watchtower/pkg/container/mocks"
	"github.com/nicholas-fedor/watchtower/pkg/types"
)

// uiTestServer mounts routes on a real loopback listener.
//
// A real server keeps every request independent: Fiber's in-process tester
// reuses one keep-alive connection per app, so repeated app.Test calls against
// a shared app deadlock.
//
// Parameters:
//   - t: Test handle.
//   - opts: API configuration options.
//   - register: Callback that mounts routes onto the app.
//
// Returns:
//   - *uiHarness: Harness with a client and the server base URL.
func uiTestServer(t *testing.T, opts config.Options, register func(*fiber.App, config.Options)) *uiHarness {
	t.Helper()

	app := fiber.New(fiber.Config{
		StrictRouting: true,
		CaseSensitive: true,
		IdleTimeout:   time.Second,
	})
	register(app, opts)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	served := make(chan error, 1)

	go func() { served <- app.Listener(listener) }()

	t.Cleanup(func() {
		_ = app.ShutdownWithTimeout(time.Second)
		<-served
	})

	client := &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	return &uiHarness{t: t, client: client, base: "http://" + listener.Addr().String()}
}

// uiHarness wraps an HTTP client bound to a running test server.
type uiHarness struct {
	t      *testing.T
	client *http.Client
	base   string
}

// Do performs a request and reads the whole response body.
func (h *uiHarness) Do(req *http.Request) (*http.Response, string) {
	h.t.Helper()

	resp, err := h.client.Do(req)
	require.NoError(h.t, err)

	body, err := io.ReadAll(resp.Body)
	require.NoError(h.t, err)
	require.NoError(h.t, resp.Body.Close())

	return resp, string(body)
}

// Get performs a GET request, optionally carrying cookies.
func (h *uiHarness) Get(target string, cookies ...*http.Cookie) (*http.Response, string) {
	h.t.Helper()

	req, err := http.NewRequestWithContext(h.t.Context(), http.MethodGet, h.base+target, nil)
	require.NoError(h.t, err)

	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}

	return h.Do(req)
}

// Post performs a POST request with an empty body.
//
// The update and check endpoints read their filters from the query string, so
// callers build the query into the target path.
func (h *uiHarness) Post(target string, cookies ...*http.Cookie) (*http.Response, string) {
	h.t.Helper()

	req, err := http.NewRequestWithContext(h.t.Context(), http.MethodPost, h.base+target, nil)
	require.NoError(h.t, err)

	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}

	return h.Do(req)
}

// PostForm performs a URL-encoded POST request.
func (h *uiHarness) PostForm(target string, form url.Values, cookies ...*http.Cookie) (*http.Response, string) {
	h.t.Helper()

	req, err := http.NewRequestWithContext(
		h.t.Context(),
		http.MethodPost,
		h.base+target,
		strings.NewReader(form.Encode()),
	)
	require.NoError(h.t, err)

	req.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationForm)
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}

	return h.Do(req)
}

// uiTestOptions builds options with the dashboard enabled alongside the
// endpoints the dashboard renders controls for.
func uiTestOptions() config.Options {
	log := zerolog.Nop()

	return config.Options{
		Logger:              &log,
		Token:               "test-token",
		Version:             "1.2.3",
		Scope:               "prod",
		EnableUIAPI:         true,
		EnableContainersAPI: true,
		EnableCheckAPI:      true,
		EnableUpdateAPI:     true,
	}
}

// uiDashboard starts a server with only the dashboard routes mounted.
func uiDashboard(t *testing.T) *uiHarness {
	t.Helper()

	return uiTestServer(t, uiTestOptions(), func(app *fiber.App, opts config.Options) {
		registerUIRoutes(app, opts)
	})
}

// sessionCookie builds a cookie carrying the dashboard session token.
func sessionCookie(value string) *http.Cookie {
	return &http.Cookie{Name: config.CookieName, Value: value}
}

// loginForm builds the sign-in form body for a token.
func loginForm(token string) url.Values {
	return url.Values{"token": []string{token}}
}

func TestRegisterUIRoutes_ServesLoginWithoutSession(t *testing.T) {
	resp, body := uiDashboard(t).Get(ui.BasePath)

	assert.Equal(t, fiber.StatusOK, resp.StatusCode)
	assert.Contains(t, body, "Sign in")
	assert.Contains(t, body, `name="token"`)
	assert.NotContains(t, body, "container-table",
		"the dashboard must not render without a valid session")
}

func TestRegisterUIRoutes_ServesDashboardWithSession(t *testing.T) {
	resp, body := uiDashboard(t).Get(ui.BasePath, sessionCookie("test-token"))

	require.Equal(t, fiber.StatusOK, resp.StatusCode)
	assert.Contains(t, body, "container-table")
	assert.Contains(t, body, `"version":"1.2.3"`)
	assert.Contains(t, body, `"scope":"prod"`)
	assert.Contains(t, body, `"containers":true`)
	assert.Contains(t, body, `"update":true`)
	assert.NotContains(t, body, "test-token",
		"the API token must never appear in the rendered page")
}

func TestRegisterUIRoutes_ServesTrailingSlash(t *testing.T) {
	resp, body := uiDashboard(t).Get(ui.BasePath+"/", sessionCookie("test-token"))

	assert.Equal(t, fiber.StatusOK, resp.StatusCode)
	assert.Contains(t, body, "container-table")
}

func TestRegisterUIRoutes_RedirectsRootToDashboard(t *testing.T) {
	resp, _ := uiDashboard(t).Get("/")

	assert.Equal(t, fiber.StatusFound, resp.StatusCode)
	assert.Equal(t, ui.BasePath, resp.Header.Get(fiber.HeaderLocation))
}

func TestRegisterUIRoutes_RejectsStaleSession(t *testing.T) {
	opts := uiTestOptions()
	opts.Token = "rotated-token"

	harness := uiTestServer(t, opts, registerUIRoutes)
	resp, body := harness.Get(ui.BasePath, sessionCookie("old-token"))

	assert.Equal(t, fiber.StatusOK, resp.StatusCode)
	assert.Contains(t, body, "Sign in")
}

func TestRegisterUIRoutes_LoginSetsSessionCookie(t *testing.T) {
	resp, _ := uiDashboard(t).PostForm(ui.LoginPath, loginForm("test-token"))

	require.Equal(t, fiber.StatusSeeOther, resp.StatusCode)
	assert.Equal(t, ui.BasePath, resp.Header.Get(fiber.HeaderLocation))

	cookies := resp.Cookies()
	require.Len(t, cookies, 1)

	cookie := cookies[0]
	assert.Equal(t, config.CookieName, cookie.Name)
	assert.Equal(t, "test-token", cookie.Value)
	assert.Equal(t, "/", cookie.Path)
	assert.True(t, cookie.HttpOnly, "the session cookie must be HttpOnly")
	assert.Equal(t, http.SameSiteLaxMode, cookie.SameSite)
	assert.Positive(t, cookie.MaxAge)
	assert.False(t, cookie.Secure, "plain HTTP requests must not set the Secure flag")
}

func TestRegisterUIRoutes_LoginCookieUnlocksDashboard(t *testing.T) {
	harness := uiDashboard(t)

	resp, _ := harness.PostForm(ui.LoginPath, loginForm("test-token"))
	require.Equal(t, fiber.StatusSeeOther, resp.StatusCode)

	cookies := resp.Cookies()
	require.Len(t, cookies, 1)

	dash, body := harness.Get(ui.BasePath, cookies[0])
	assert.Equal(t, fiber.StatusOK, dash.StatusCode)
	assert.Contains(t, body, "container-table")
}

func TestRegisterUIRoutes_LoginRejectsBadToken(t *testing.T) {
	resp, body := uiDashboard(t).PostForm(ui.LoginPath, loginForm("nope"))

	assert.Equal(t, fiber.StatusUnauthorized, resp.StatusCode)
	assert.Contains(t, body, "was not accepted")
	assert.Empty(t, resp.Cookies(), "a failed sign-in must not issue a session cookie")
}

func TestRegisterUIRoutes_LoginRejectsEmptyToken(t *testing.T) {
	resp, _ := uiDashboard(t).PostForm(ui.LoginPath, loginForm(""))

	assert.Equal(t, fiber.StatusUnauthorized, resp.StatusCode)
}

func TestRegisterUIRoutes_LogoutClearsSessionCookie(t *testing.T) {
	resp, _ := uiDashboard(t).PostForm(ui.LogoutPath, url.Values{}, sessionCookie("test-token"))

	require.Equal(t, fiber.StatusSeeOther, resp.StatusCode)

	cookies := resp.Cookies()
	require.Len(t, cookies, 1)
	assert.Equal(t, config.CookieName, cookies[0].Name)
	assert.Equal(t, "", cookies[0].Value)
	assert.Negative(t, cookies[0].MaxAge, "logout must expire the cookie")
}

func TestRegisterUIRoutes_ServesAssets(t *testing.T) {
	cases := map[string]string{
		"app.css":     "text/css; charset=utf-8",
		"app.js":      "text/javascript; charset=utf-8",
		"favicon.svg": "image/svg+xml",
	}

	for name, contentType := range cases {
		t.Run(name, func(t *testing.T) {
			resp, body := uiDashboard(t).Get(ui.AssetsPath + "/" + name)

			require.Equal(t, fiber.StatusOK, resp.StatusCode)
			assert.Equal(t, contentType, resp.Header.Get(fiber.HeaderContentType))
			assert.NotEmpty(t, body)
			assert.Equal(t, "no-cache", resp.Header.Get(fiber.HeaderCacheControl))
			assert.NotEmpty(t, resp.Header.Get(fiber.HeaderETag))
		})
	}
}

func TestRegisterUIRoutes_AssetReturnsNotModifiedOnMatchingETag(t *testing.T) {
	harness := uiDashboard(t)

	resp, _ := harness.Get(ui.AssetsPath + "/app.css")
	etag := resp.Header.Get(fiber.HeaderETag)
	require.NotEmpty(t, etag)

	req, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		harness.base+ui.AssetsPath+"/app.css",
		nil,
	)
	require.NoError(t, err)
	req.Header.Set(fiber.HeaderIfNoneMatch, etag)

	cached, _ := harness.Do(req)

	assert.Equal(t, fiber.StatusNotModified, cached.StatusCode)
}

func TestRegisterUIRoutes_RejectsAssetTraversal(t *testing.T) {
	targets := []string{
		"/ui/assets/../../go.mod",
		"/ui/assets/..%2F..%2Fgo.mod",
		"/ui/assets/%2e%2e%2f%2e%2e%2fgo.mod",
		"/ui/assets/",
		"/ui/assets/nope.css",
	}

	for _, target := range targets {
		t.Run(target, func(t *testing.T) {
			resp, _ := uiDashboard(t).Get(target)

			assert.Equal(t, fiber.StatusNotFound, resp.StatusCode)
		})
	}
}

func TestUIRoutesDoNotBypassAPIAuth(t *testing.T) {
	opts := uiTestOptions()
	mockClient := mockContainer.NewMockClient(t)
	mockClient.EXPECT().ListContainers(mock.Anything).Return([]types.Container{}, nil).Maybe()
	opts.Client = mockClient

	// testAuthMiddleware expects the literal token "test", so the dashboard
	// session token has to match it for the JSON API to answer.
	opts.Token = "test"

	harness := uiTestServer(t, opts, func(app *fiber.App, o config.Options) {
		Register(t.Context(), app, testAuthMiddleware(), o)
	})

	t.Run("bogus cookie is rejected", func(t *testing.T) {
		resp, _ := harness.Get("/v1/containers", sessionCookie("not-the-token"))

		assert.Equal(t, fiber.StatusUnauthorized, resp.StatusCode)
	})

	t.Run("valid cookie unlocks the API", func(t *testing.T) {
		resp, _ := harness.Get("/v1/containers", sessionCookie("test"))

		assert.Equal(t, fiber.StatusOK, resp.StatusCode)
	})
}

func TestUIRoutesServeDashboardAlongsideAPI(t *testing.T) {
	harness := uiTestServer(t, uiTestOptions(), func(app *fiber.App, opts config.Options) {
		Register(t.Context(), app, testAuthMiddleware(), opts)
	})

	resp, _ := harness.Get(ui.BasePath, sessionCookie("test-token"))

	assert.Equal(t, fiber.StatusOK, resp.StatusCode)
}

func TestUIConfigProjectsEnabledEndpoints(t *testing.T) {
	cfg := uiConfig(uiTestOptions())

	assert.Equal(t, "1.2.3", cfg.Version)
	assert.Equal(t, "prod", cfg.Scope)
	assert.True(t, cfg.ContainersAPI)
	assert.True(t, cfg.CheckAPI)
	assert.True(t, cfg.UpdateAPI)
	assert.False(t, cfg.MetricsAPI)
}

func TestHasUISession(t *testing.T) {
	opts := uiTestOptions()

	var valid bool

	harness := uiTestServer(t, opts, func(app *fiber.App, o config.Options) {
		app.Get("/probe-session", func(c fiber.Ctx) error {
			valid = hasUISession(c, o.Token)

			return c.SendStatus(fiber.StatusOK)
		})
	})

	cases := []struct {
		name   string
		cookie *http.Cookie
		want   bool
	}{
		{name: "matching token", cookie: sessionCookie("test-token"), want: true},
		{name: "wrong token", cookie: sessionCookie("wrong"), want: false},
		{name: "empty cookie", cookie: sessionCookie(""), want: false},
		{name: "no cookie", cookie: nil, want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.cookie == nil {
				harness.Get("/probe-session")
			} else {
				harness.Get("/probe-session", tc.cookie)
			}

			assert.Equal(t, tc.want, valid)
		})
	}
}

func TestHasUISessionRejectsUnconfiguredToken(t *testing.T) {
	opts := uiTestOptions()
	opts.Token = ""

	var valid bool

	harness := uiTestServer(t, opts, func(app *fiber.App, o config.Options) {
		app.Get("/probe-session", func(c fiber.Ctx) error {
			valid = hasUISession(c, o.Token)

			return c.SendStatus(fiber.StatusOK)
		})
	})

	_, _ = harness.Get("/probe-session", sessionCookie("test-token"))

	assert.False(t, valid, "an unconfigured token must never authenticate")
}

func TestUIConfigJSONEscapesTemplateValues(t *testing.T) {
	opts := uiTestOptions()
	opts.Scope = `"><script>alert(1)</script>`

	harness := uiTestServer(t, opts, registerUIRoutes)
	resp, body := harness.Get(ui.BasePath, sessionCookie("test-token"))

	require.Equal(t, fiber.StatusOK, resp.StatusCode)
	assert.NotContains(t, body, "<script>alert(1)</script>",
		"template values must stay escaped inside the config script tag")
	assert.Contains(t, body, "alert(1)")
}
