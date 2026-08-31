package routes

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/nicholas-fedor/watchtower/internal/api/config"
	"github.com/nicholas-fedor/watchtower/internal/api/ui"
)

const (
	// sessionCookieMaxAge bounds how long a dashboard session cookie stays
	// valid before the operator has to sign in again.
	sessionCookieMaxAge = 7 * 24 * time.Hour
	// loginErrorInvalidToken is shown when a sign-in attempt fails. The message
	// is deliberately generic so it cannot be used to probe token shape.
	loginErrorInvalidToken = "That API token was not accepted. Check the http-api-token value and try again."
)

var (
	uiOnce        sync.Once
	uiRenderer    *ui.Renderer
	uiRendererErr error
)

// uiRendererFor returns the process-wide dashboard renderer, parsing the
// embedded templates on first use.
//
// Returns:
//   - *ui.Renderer: Renderer, nil when parsing failed.
//   - error: Non-nil when the embedded templates cannot be parsed.
func uiRendererFor() (*ui.Renderer, error) {
	uiOnce.Do(func() {
		uiRenderer, uiRendererErr = ui.New()
	})

	return uiRenderer, uiRendererErr
}

// registerUIRoutes mounts the built-in dashboard under /ui.
//
// The dashboard is served without the API auth middleware: it authenticates
// itself with a session cookie and falls back to a login page. Every JSON call
// the page makes still goes through the API auth middleware on /v1/*, so
// enabling the dashboard does not widen access to the API.
//
// Parameters:
//   - app: Fiber application.
//   - opts: API configuration options.
func registerUIRoutes(app *fiber.App, opts config.Options) {
	index := handleUIIndex(opts)

	app.Get(ui.BasePath, index)
	app.Get(ui.BasePath+"/", index)

	// Convenience entry point: the dashboard is the only thing served at the
	// root, so send bare requests straight to it.
	app.Get("/", func(c fiber.Ctx) error {
		return c.Redirect().Status(fiber.StatusFound).To(ui.BasePath)
	})

	app.Get(ui.AssetsPath+"/*", handleUIAsset())
	app.Post(ui.LoginPath, handleUILogin(opts))
	app.Post(ui.LogoutPath, handleUILogout())
}

// handleUIIndex serves the dashboard, or the login page without a valid session.
//
// The dashboard page itself carries no container data: the browser fetches it
// from /v1/containers after load so the page can be cached and re-rendered
// without touching Docker.
//
// Parameters:
//   - opts: API configuration options.
//
// Returns:
//   - fiber.Handler: Handler for GET /ui.
func handleUIIndex(opts config.Options) fiber.Handler {
	return func(c fiber.Ctx) error {
		renderer, err := uiRendererFor()
		if err != nil {
			return fiber.ErrInternalServerError
		}

		if !hasUISession(c, opts.Token) {
			return writeLogin(c, renderer, opts, "", fiber.StatusOK)
		}

		cfgJSON, err := uiConfig(opts).JSON()
		if err != nil {
			return fiber.ErrInternalServerError
		}

		var body strings.Builder

		err = renderer.RenderIndex(&body, ui.IndexData{
			ConfigJSON: cfgJSON,
			Version:    opts.Version,
		})
		if err != nil {
			return fiber.ErrInternalServerError
		}

		return writeHTML(c, body.String())
	}
}

// handleUILogin exchanges an API token for a session cookie.
//
// The submitted token is compared in constant time and never echoed back. A
// failed attempt re-renders the login page with a generic error so the endpoint
// cannot be used to enumerate tokens.
//
// Parameters:
//   - opts: API configuration options.
//
// Returns:
//   - fiber.Handler: Handler for POST /ui/login.
func handleUILogin(opts config.Options) fiber.Handler {
	return func(c fiber.Ctx) error {
		renderer, err := uiRendererFor()
		if err != nil {
			return fiber.ErrInternalServerError
		}

		token := strings.TrimSpace(c.FormValue("token"))
		if !config.TokenValid(opts.Token, token) {
			logUILoginFailure(opts, c)

			return writeLogin(c, renderer, opts, loginErrorInvalidToken, fiber.StatusUnauthorized)
		}

		writeSessionCookie(c, token)

		return c.Redirect().Status(fiber.StatusSeeOther).To(ui.BasePath)
	}
}

// handleUILogout clears the dashboard session cookie and returns to the login
// page.
//
// Returns:
//   - fiber.Handler: Handler for POST /ui/logout.
func handleUILogout() fiber.Handler {
	return func(c fiber.Ctx) error {
		clearSessionCookie(c)

		return c.Redirect().Status(fiber.StatusSeeOther).To(ui.BasePath)
	}
}

// handleUIAsset serves embedded dashboard assets.
//
// Assets are content-addressed with an ETag so repeat loads are cheap, while
// no-cache revalidation makes sure a Watchtower upgrade never leaves a browser
// serving stale assets from an older binary.
//
// Returns:
//   - fiber.Handler: Handler for GET /ui/assets/*.
func handleUIAsset() fiber.Handler {
	return func(c fiber.Ctx) error {
		renderer, err := uiRendererFor()
		if err != nil {
			return fiber.ErrInternalServerError
		}

		contents, contentType, err := renderer.Asset(c.Params("*"))
		if err != nil {
			return fiber.ErrNotFound
		}

		etag := `"` + assetETag(contents) + `"`

		c.Set(fiber.HeaderContentType, contentType)
		c.Set(fiber.HeaderETag, etag)
		c.Set(fiber.HeaderCacheControl, "no-cache")

		if c.Get(fiber.HeaderIfNoneMatch) == etag {
			return c.SendStatus(fiber.StatusNotModified)
		}

		return c.Send(contents)
	}
}

// writeLogin renders the login page with an optional error message.
//
// Parameters:
//   - c: Request context.
//   - renderer: Dashboard renderer.
//   - opts: API configuration options.
//   - message: Error message, or empty for a clean render.
//   - status: HTTP status code for the response.
//
// Returns:
//   - error: Non-nil if rendering or writing fails.
func writeLogin(c fiber.Ctx, renderer *ui.Renderer, opts config.Options, message string, status int) error {
	cfgJSON, err := uiConfig(opts).JSON()
	if err != nil {
		return fiber.ErrInternalServerError
	}

	var body strings.Builder

	err = renderer.RenderLogin(&body, ui.LoginData{
		ConfigJSON: cfgJSON,
		Version:    opts.Version,
		Error:      message,
	})
	if err != nil {
		return fiber.ErrInternalServerError
	}

	c.Status(status)

	return writeHTML(c, body.String())
}

// logUILoginFailure records a rejected sign-in attempt.
//
// Parameters:
//   - opts: API configuration options.
//   - c: Request context.
func logUILoginFailure(opts config.Options, c fiber.Ctx) {
	if opts.Logger == nil {
		return
	}

	opts.Logger.Warn().
		Str("ip", c.IP()).
		Str("path", c.Path()).
		Str("notify", "no").
		Msg("Rejected dashboard sign-in attempt")
}

// uiConfig projects API options into the dashboard's client configuration.
//
// Parameters:
//   - opts: API configuration options.
//
// Returns:
//   - ui.Config: Runtime values rendered into the dashboard page.
func uiConfig(opts config.Options) ui.Config {
	return ui.Config{
		Version:       opts.Version,
		Scope:         opts.Scope,
		ContainersAPI: opts.EnableContainersAPI,
		CheckAPI:      opts.EnableCheckAPI,
		UpdateAPI:     opts.EnableUpdateAPI,
		MetricsAPI:    opts.EnableMetricsAPI,
		HistoryAPI:    opts.EnableHistoryAPI,
		HealthAPI:     opts.EnableHealthAPI,
		EventsAPI:     opts.EnableEventsAPI,
	}
}

// hasUISession reports whether the request carries a valid session cookie.
//
// Parameters:
//   - c: Request context.
//   - token: Configured API token.
//
// Returns:
//   - bool: True when the cookie matches the configured token.
func hasUISession(c fiber.Ctx, token string) bool {
	return config.TokenValid(token, strings.TrimSpace(c.Cookies(config.CookieName)))
}

// writeSessionCookie stores the API token as an HttpOnly session cookie so
// same-origin dashboard calls authenticate without exposing it to scripts.
//
// Parameters:
//   - c: Request context.
//   - token: Verified API token.
func writeSessionCookie(c fiber.Ctx, token string) {
	c.Cookie(&fiber.Cookie{
		Name:     config.CookieName,
		Value:    token,
		Path:     "/",
		HTTPOnly: true,
		Secure:   c.Secure(),
		// Lax keeps top-level navigation (bookmarks, links) working while
		// still withholding the cookie from cross-site POST requests.
		SameSite: fiber.CookieSameSiteLaxMode,
		MaxAge:   int(sessionCookieMaxAge.Seconds()),
	})
}

// clearSessionCookie expires the dashboard session cookie.
//
// Both MaxAge and Expires are set so the cookie is dropped by clients that
// honour only one of the two attributes.
//
// Parameters:
//   - c: Request context.
func clearSessionCookie(c fiber.Ctx) {
	c.Cookie(&fiber.Cookie{
		Name:     config.CookieName,
		Value:    "",
		Path:     "/",
		HTTPOnly: true,
		Secure:   c.Secure(),
		SameSite: fiber.CookieSameSiteLaxMode,
		MaxAge:   -1,
		Expires:  time.Now().UTC().Add(-time.Hour),
	})
}

// writeHTML sends an HTML response that must never be cached.
//
// Parameters:
//   - c: Request context.
//   - body: Rendered page.
//
// Returns:
//   - error: Non-nil if writing fails.
func writeHTML(c fiber.Ctx, body string) error {
	c.Set(fiber.HeaderContentType, "text/html; charset=utf-8")
	c.Set(fiber.HeaderCacheControl, "no-store")

	err := c.SendString(body)
	if err != nil {
		return fmt.Errorf("send dashboard page: %w", err)
	}

	return nil
}

// assetETag returns a short content hash for cache revalidation.
//
// Parameters:
//   - contents: Asset bytes.
//
// Returns:
//   - string: Hex digest used as an ETag.
func assetETag(contents []byte) string {
	sum := sha256.Sum256(contents)

	return hex.EncodeToString(sum[:8])
}
