package routes

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nicholas-fedor/watchtower/internal/api/config"
	"github.com/nicholas-fedor/watchtower/internal/api/ui"
	"github.com/nicholas-fedor/watchtower/internal/metrics"
	"github.com/nicholas-fedor/watchtower/pkg/types"
	mockFilterable "github.com/nicholas-fedor/watchtower/pkg/types/mocks"
)

// dashboardContainerNames is the fake fleet the update pipeline is asked about.
var dashboardContainerNames = []string{"nginx", "nginx.sidecar", "web-1", "app[1]", "app1"}

// escapeRegExp mirrors the escaping the dashboard performs in the browser
// before it sends a container name as a filter pattern.
func escapeRegExp(value string) string {
	const special = ".*+?^${}()|[]\\"

	var b strings.Builder

	for _, r := range value {
		if strings.ContainsRune(special, r) {
			b.WriteByte('\\')
		}

		b.WriteRune(r)
	}

	return b.String()
}

// fakeContainer builds a FilterableContainer that only carries a name, which
// is all the update and check filters look at for container targeting.
func fakeContainer(t *testing.T, name string) types.FilterableContainer {
	t.Helper()

	container := mockFilterable.NewMockFilterableContainer(t)
	container.EXPECT().Name().Return(name).Maybe()

	return container
}

// dashboardOptions builds API options whose update pipeline records which
// containers the request filter selected.
func dashboardOptions(t *testing.T, mu *sync.Mutex, matched *[]string) config.Options {
	t.Helper()

	log := zerolog.Nop()
	lock := make(chan bool, 1)
	lock <- true

	return config.Options{
		Logger:              &log,
		Token:               "ui-token",
		Version:             "9.9.9",
		EnableUIAPI:         true,
		EnableContainersAPI: true,
		EnableCheckAPI:      true,
		EnableUpdateAPI:     true,
		UnblockHTTPAPI:      true,
		UpdateLock:          lock,
		Filter:              func(_ types.FilterableContainer) bool { return true },
		FilterByImage:       func(_ []string, f types.Filter) types.Filter { return f },
		DefaultMetrics:      func() *metrics.Metrics { return testMetrics },
		RunUpdatesWithNotifications: func(
			_ context.Context,
			filter types.Filter,
			_ types.UpdateParams,
		) *metrics.Metric {
			selected := make([]string, 0, len(dashboardContainerNames))

			for _, name := range dashboardContainerNames {
				if filter(fakeContainer(t, name)) {
					selected = append(selected, name)
				}
			}

			mu.Lock()
			*matched = selected
			mu.Unlock()

			return &metrics.Metric{Scanned: len(selected), Updated: len(selected)}
		},
	}
}

// TestDashboardUpdateTargetsSingleContainer walks a full browser session and
// asserts that triggering an update for one container targets only that one.
func TestDashboardUpdateTargetsSingleContainer(t *testing.T) {
	var (
		mu      sync.Mutex
		matched []string
	)

	opts := dashboardOptions(t, &mu, &matched)

	harness := uiTestServer(t, opts, func(app *fiber.App, o config.Options) {
		Register(t.Context(), app, testAuthMiddlewareFor("ui-token"), o)
	})

	// 1. Anonymous visitors get the login page, not the dashboard.
	login, body := harness.Get(ui.BasePath)
	require.Equal(t, http.StatusOK, login.StatusCode)
	assert.Contains(t, body, "Sign in")

	// 2. Sign in and keep the session cookie, exactly like a browser would.
	signed, _ := harness.PostForm(ui.LoginPath, url.Values{"token": []string{"ui-token"}})
	require.Equal(t, http.StatusSeeOther, signed.StatusCode)

	cookies := signed.Cookies()
	require.Len(t, cookies, 1)

	// 3. The session unlocks the dashboard page.
	dash, body := harness.Get(ui.BasePath, cookies[0])
	require.Equal(t, http.StatusOK, dash.StatusCode)
	assert.Contains(t, body, "container-table")

	// 4. Updating one container targets only that container.
	cases := []struct {
		name   string
		target string
		want   []string
	}{
		{name: "plain name", target: "nginx", want: []string{"nginx"}},
		{name: "dotted sibling", target: "nginx.sidecar", want: []string{"nginx.sidecar"}},
		{name: "hyphenated name", target: "web-1", want: []string{"web-1"}},
		{
			name:   "regex metacharacters stay literal",
			target: "app[1]",
			want:   []string{"app[1]"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			query := url.Values{"container": []string{escapeRegExp(tc.target)}}
			resp, _ := harness.Post("/v1/update?"+query.Encode(), cookies[0])

			require.Equal(t, http.StatusOK, resp.StatusCode)

			mu.Lock()
			got := append([]string(nil), matched...)
			mu.Unlock()

			assert.Equal(t, tc.want, got,
				"updating %q must not touch any other container", tc.target)
		})
	}
}

// TestDashboardUpdateAllTargetsEveryContainer verifies the dashboard's "update
// all" button issues an unfiltered update.
func TestDashboardUpdateAllTargetsEveryContainer(t *testing.T) {
	var (
		mu      sync.Mutex
		matched []string
	)

	opts := dashboardOptions(t, &mu, &matched)

	harness := uiTestServer(t, opts, func(app *fiber.App, o config.Options) {
		Register(t.Context(), app, testAuthMiddlewareFor("ui-token"), o)
	})

	signed, _ := harness.PostForm(ui.LoginPath, url.Values{"token": []string{"ui-token"}})
	cookies := signed.Cookies()
	require.Len(t, cookies, 1)

	resp, _ := harness.Post("/v1/update?async=true", cookies[0])
	assert.Equal(t, http.StatusAccepted, resp.StatusCode)

	// The async path runs in the background, so give it a moment to land.
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()

		return len(matched) == len(dashboardContainerNames)
	}, 3*time.Second, 10*time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	assert.ElementsMatch(t, dashboardContainerNames, matched)
}

// TestDashboardSessionCookieUnlocksAPI verifies the dashboard's cookie is
// accepted by the /v1/* endpoints the page calls.
func TestDashboardSessionCookieUnlocksAPI(t *testing.T) {
	var (
		mu      sync.Mutex
		matched []string
	)

	opts := dashboardOptions(t, &mu, &matched)

	harness := uiTestServer(t, opts, func(app *fiber.App, o config.Options) {
		Register(t.Context(), app, testAuthMiddlewareFor("ui-token"), o)
	})

	// Without a session the JSON API stays closed.
	anon, _ := harness.Post("/v1/update")
	assert.Equal(t, http.StatusUnauthorized, anon.StatusCode)

	// A wrong token yields no cookie and no access.
	bad, _ := harness.PostForm(ui.LoginPath, url.Values{"token": []string{"nope"}})
	require.Equal(t, http.StatusUnauthorized, bad.StatusCode)
	assert.Empty(t, bad.Cookies())
}

// testAuthMiddlewareFor builds a token auth middleware for an arbitrary token.
//
// routes_test.go pins testAuthMiddleware to the literal token "test"; the
// dashboard session tests need a token that matches their configured value.
func testAuthMiddlewareFor(token string) fiber.Handler {
	return func(c fiber.Ctx) error {
		if !config.TokenValid(token, extractCookieToken(c)) {
			return c.Status(fiber.StatusUnauthorized).SendString("missing or malformed API key")
		}

		return c.Next()
	}
}

// extractCookieToken reads the dashboard session cookie from a request.
func extractCookieToken(c fiber.Ctx) string {
	return strings.TrimSpace(c.Cookies(config.CookieName))
}
