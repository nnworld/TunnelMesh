package tray

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/client"
)

// secretCookieName carries the launch secret to same-origin subresources.
//
// The webview is pointed at a URL carrying ?secret=... once. Its JavaScript and CSS
// requests would not repeat it, so the document response stores it in a cookie that the
// rest of the session sends automatically.
const secretCookieName = "tunnelmesh_tray_secret"

// secretHeaderName is the explicit carrier the front end uses for API calls.
const secretHeaderName = "X-Tray-Secret"

// maxRequestBody bounds a settings save. The largest plausible routing form is a few
// kilobytes, so the limit exists to stop a local caller from streaming into memory
// rather than to accommodate a real payload.
const maxRequestBody = 1 << 20

// secretBytes is the launch secret length. 32 bytes rendered as hex is well past brute
// force for a port that only exists for the lifetime of one process.
const secretBytes = 32

// apiTimeouts keep a stuck renderer from pinning a handler goroutine.
const (
	apiReadHeaderTimeout = 10 * time.Second
	apiWriteTimeout      = 60 * time.Second
	apiIdleTimeout       = 90 * time.Second
)

// placeholderUI is served when the binary was built without the webview bundle, so the
// failure reads as a packaging problem instead of a blank window.
const placeholderUI = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>TunnelMesh Client</title></head>
<body style="font-family:-apple-system,sans-serif;padding:24px">
<h1>TunnelMesh Client</h1>
<p>The settings interface is not bundled into this binary.</p>
<p>Build the front end (<code>cd web-tray &amp;&amp; npm run build</code>) and rebuild with
<code>-tags tray</code> so the bundle is embedded.</p>
</body></html>`

// GenerateSecret returns a fresh launch secret.
func GenerateSecret() (string, error) {
	buffer := make([]byte, secretBytes)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("tray: generate the local API secret: %w", err)
	}
	return hex.EncodeToString(buffer), nil
}

// APIOptions configures the loopback server.
type APIOptions struct {
	// App is required: the server is a transport for it and holds no logic of its own.
	App *App
	// Assets is the embedded settings bundle. Nil serves a placeholder page.
	Assets fs.FS
	// Secret overrides the generated launch secret. Only tests set it.
	Secret string
	// ListenAddr overrides the bind address. It must stay on loopback.
	ListenAddr string
}

// APIServer serves the settings UI and the local JSON API on 127.0.0.1.
//
// It exists rather than a webview bridge because the two need the same thing - a
// request/response channel to the Go side - and an HTTP server is testable without a
// window, a renderer or a macOS host. The price is that anything on the machine can
// reach the port, which is why every API request carries a per-launch secret and the
// socket never leaves loopback.
type APIServer struct {
	app    *App
	assets fs.FS
	secret string
	addr   string

	mu       sync.Mutex
	listener net.Listener
	server   *http.Server
	show     func()
	quit     func()
}

// NewAPIServer builds the server without binding a socket. Start does that, so a test
// can drive Handler() directly and never open a port.
func NewAPIServer(options APIOptions) (*APIServer, error) {
	if options.App == nil {
		return nil, errors.New("tray: the local API needs an application")
	}
	secret := strings.TrimSpace(options.Secret)
	if secret == "" {
		generated, err := GenerateSecret()
		if err != nil {
			return nil, err
		}
		secret = generated
	}
	addr := strings.TrimSpace(options.ListenAddr)
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("tray: the local API address %q is unusable: %w", addr, err)
	}
	// Binding anything else would expose the token-editing surface to the network. The
	// check is on the literal address rather than on the resolved listener because a
	// hostname that happens to resolve to a public interface is exactly the mistake
	// worth refusing early.
	if !isLoopbackHost(host) {
		return nil, fmt.Errorf("tray: the local API must bind a loopback address, got %q", host)
	}
	return &APIServer{app: options.App, assets: options.Assets, secret: secret, addr: addr}, nil
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	parsed := net.ParseIP(host)
	return parsed != nil && parsed.IsLoopback()
}

// SetWindowHandlers installs the callbacks the window actions need. The native shell
// supplies them; without them the actions report that no window is attached.
func (s *APIServer) SetWindowHandlers(show, quit func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.show, s.quit = show, quit
}

// Secret returns the launch secret so the shell can put it in the initial URL.
func (s *APIServer) Secret() string { return s.secret }

// Addr returns the bound address, or the configured one before Start.
func (s *APIServer) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener != nil {
		return s.listener.Addr().String()
	}
	return s.addr
}

// URL is the address the webview loads. The secret rides along once; the response turns
// it into a cookie for the rest of the session.
func (s *APIServer) URL() string {
	return "http://" + s.Addr() + "/?secret=" + url.QueryEscape(s.secret)
}

// Start binds the loopback socket and serves in the background.
func (s *APIServer) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener != nil {
		return nil
	}
	listener, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("tray: cannot bind the local API: %w", err)
	}
	s.listener = listener
	s.server = &http.Server{
		Handler:           s,
		ReadHeaderTimeout: apiReadHeaderTimeout,
		WriteTimeout:      apiWriteTimeout,
		IdleTimeout:       apiIdleTimeout,
	}
	go func() {
		// ErrServerClosed is the normal path: Shutdown is how the tray exits.
		if err := s.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.mu.Lock()
			s.listener, s.server = nil, nil
			s.mu.Unlock()
		}
	}()
	return nil
}

// Shutdown stops serving. It is safe to call on a server that never started.
func (s *APIServer) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	server, listener := s.server, s.listener
	s.server, s.listener = nil, nil
	s.mu.Unlock()
	if server == nil {
		if listener != nil {
			return listener.Close()
		}
		return nil
	}
	return server.Shutdown(ctx)
}

// Handler exposes the routing for tests without a socket.
func (s *APIServer) Handler() http.Handler { return s }

// ServeHTTP splits the local API from the static bundle.
func (s *APIServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if isAPIPath(r.URL.Path) {
		s.serveAPI(w, r)
		return
	}
	s.serveAssets(w, r)
}

func isAPIPath(path string) bool {
	return path == "/api" || strings.HasPrefix(path, "/api/")
}

// serveAssets delivers the embedded bundle.
//
// Static files are served without the secret on purpose. They are the same JavaScript
// anyone can read out of the repository, they carry no credential, and requiring the
// secret on them would break the very first page load: the webview requests the
// document with ?secret=... and then requests its assets without it. Everything that
// can read or change state lives behind /api, which does require the secret.
func (s *APIServer) serveAssets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeAPIError(w, http.StatusMethodNotAllowed, "the settings interface only answers GET")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	// A page that can read the token must never be framed by another site.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:")
	if s.assets == nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, placeholderUI)
		return
	}
	http.FileServerFS(s.assets).ServeHTTP(w, r)
}

// route is one API endpoint.
type route struct {
	path    string
	method  string
	handler func(*APIServer, http.ResponseWriter, *http.Request)
}

// apiRoutes is the whole local surface. Keeping it in one table makes the boundary of
// what a renderer can ask the tray to do reviewable at a glance.
var apiRoutes = []route{
	{"/api/settings", http.MethodGet, (*APIServer).handleGetSettings},
	{"/api/settings", http.MethodPut, (*APIServer).handleSaveSettings},
	{"/api/routing", http.MethodGet, (*APIServer).handleGetRouting},
	{"/api/routing", http.MethodPut, (*APIServer).handleSaveRouting},
	{"/api/agents", http.MethodGet, (*APIServer).handleAgents},
	{"/api/validate", http.MethodPost, (*APIServer).handleValidate},
	{"/api/stats", http.MethodGet, (*APIServer).handleStats},
	{"/api/about", http.MethodGet, (*APIServer).handleAbout},
	{"/api/actions/open-website", http.MethodPost, (*APIServer).handleOpenWebsite},
	{"/api/actions/open-releases", http.MethodPost, (*APIServer).handleOpenReleases},
	{"/api/actions/open-docs", http.MethodPost, (*APIServer).handleOpenDocs},
	{"/api/actions/start", http.MethodPost, (*APIServer).handleStart},
	{"/api/actions/stop", http.MethodPost, (*APIServer).handleStop},
	{"/api/actions/restart", http.MethodPost, (*APIServer).handleRestart},
	{"/api/actions/show-window", http.MethodPost, (*APIServer).handleShowWindow},
	{"/api/actions/quit", http.MethodPost, (*APIServer).handleQuit},
}

func (s *APIServer) serveAPI(w http.ResponseWriter, r *http.Request) {
	// Checked before routing rather than inside the decoders: an oversized body is
	// rejected whether or not the route happens to read it, so no handler can be
	// coaxed into buffering one.
	if r.ContentLength > maxRequestBody {
		writeAPIError(w, http.StatusRequestEntityTooLarge, "the request body is too large")
		return
	}
	if !s.authorized(r, w) {
		return
	}
	if foreign, origin := s.crossOrigin(r); foreign {
		writeAPIError(w, http.StatusForbidden, "this origin may not drive the tray: "+origin)
		return
	}
	path := r.URL.Path
	matched := false
	for _, candidate := range apiRoutes {
		if candidate.path != path {
			continue
		}
		matched = true
		if candidate.method != r.Method {
			continue
		}
		candidate.handler(s, w, r)
		return
	}
	if matched {
		writeAPIError(w, http.StatusMethodNotAllowed, path+" does not accept "+r.Method)
		return
	}
	writeAPIError(w, http.StatusNotFound, "unknown local API path "+path)
}

// authorized reports whether the request carries the launch secret, and sets the cookie
// when the secret arrived in the query string.
//
// The comparison is constant time. The secret is the only thing standing between another
// process on this machine and the ability to read or rewrite the client token, so a
// byte-by-byte short circuit is not an acceptable shortcut.
func (s *APIServer) authorized(r *http.Request, w http.ResponseWriter) bool {
	fromQuery := false
	candidates := []string{
		r.Header.Get(secretHeaderName),
		strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "),
	}
	if cookie, err := r.Cookie(secretCookieName); err == nil {
		candidates = append(candidates, cookie.Value)
	}
	if query := r.URL.Query().Get("secret"); query != "" {
		candidates = append(candidates, query)
		fromQuery = true
	}
	wanted := []byte(s.secret)
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if subtle.ConstantTimeCompare(wanted, []byte(candidate)) == 1 {
			if fromQuery {
				// SameSite=Strict and no Secure flag: the origin is loopback over HTTP,
				// where Secure would stop the cookie being stored at all.
				http.SetCookie(w, &http.Cookie{
					Name: secretCookieName, Value: s.secret, Path: "/",
					HttpOnly: true, SameSite: http.SameSiteStrictMode,
				})
			}
			return true
		}
	}
	writeAPIError(w, http.StatusUnauthorized, "this request does not carry the tray secret")
	return false
}

// crossOrigin rejects a request whose Origin is not this server.
//
// The secret already makes a remote page unable to call the API, but a same-machine page
// that somehow learned it should still not be able to drive the tray from a different
// origin. Requests with no Origin - curl, the initial document load, same-origin GETs -
// are allowed through on the strength of the secret.
func (s *APIServer) crossOrigin(r *http.Request) (bool, string) {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" || origin == "null" {
		return false, ""
	}
	parsed, err := url.Parse(origin)
	if err != nil {
		return true, origin
	}
	if parsed.Host != r.Host {
		return true, origin
	}
	return false, origin
}

func (s *APIServer) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	view, err := s.app.Settings(r.Context())
	if err != nil {
		writeAPIFailure(w, err)
		return
	}
	writeEnvelope(w, http.StatusOK, view)
}

func (s *APIServer) handleSaveSettings(w http.ResponseWriter, r *http.Request) {
	var update SettingsUpdate
	if err := decodeBody(w, r, &update); err != nil {
		return
	}
	view, err := s.app.SaveSettings(r.Context(), update)
	if err != nil {
		writeAPIFailure(w, err)
		return
	}
	writeEnvelope(w, http.StatusOK, view)
}

func (s *APIServer) handleGetRouting(w http.ResponseWriter, r *http.Request) {
	view, err := s.app.Routing(r.Context())
	if err != nil {
		writeAPIFailure(w, err)
		return
	}
	writeEnvelope(w, http.StatusOK, view)
}

func (s *APIServer) handleSaveRouting(w http.ResponseWriter, r *http.Request) {
	var update RoutingUpdate
	if err := decodeBody(w, r, &update); err != nil {
		return
	}
	result, err := s.app.SaveRouting(r.Context(), update)
	if err != nil {
		writeAPIFailure(w, err)
		return
	}
	writeEnvelope(w, http.StatusOK, result)
}

func (s *APIServer) handleAgents(w http.ResponseWriter, r *http.Request) {
	agents, err := s.app.Agents(r.Context(), r.URL.Query().Get("serverUrl"))
	if err != nil {
		writeAPIFailure(w, err)
		return
	}
	writeEnvelope(w, http.StatusOK, agents)
}

func (s *APIServer) handleValidate(w http.ResponseWriter, r *http.Request) {
	// An empty body validates the stored configuration, which is what the routing tab
	// does before the operator has touched the form. Decoding it anyway would report a
	// JSON error for a request that was never meant to carry a payload.
	var update *RoutingUpdate
	if r.ContentLength != 0 {
		parsed := &RoutingUpdate{}
		if err := decodeBody(w, r, parsed); err != nil {
			return
		}
		update = parsed
	}
	report, err := s.app.Validate(r.Context(), update)
	if err != nil {
		writeAPIFailure(w, err)
		return
	}
	writeEnvelope(w, http.StatusOK, report)
}

func (s *APIServer) handleStats(w http.ResponseWriter, r *http.Request) {
	writeEnvelope(w, http.StatusOK, s.app.Stats(r.Context()))
}

func (s *APIServer) handleAbout(w http.ResponseWriter, r *http.Request) {
	writeEnvelope(w, http.StatusOK, s.app.About(r.Context()))
}

func (s *APIServer) handleOpenWebsite(w http.ResponseWriter, r *http.Request) {
	s.openURL(w, s.app.OpenWebsite)
}

func (s *APIServer) handleOpenReleases(w http.ResponseWriter, r *http.Request) {
	s.openURL(w, s.app.OpenReleases)
}

func (s *APIServer) handleOpenDocs(w http.ResponseWriter, r *http.Request) {
	s.openURL(w, s.app.OpenDocs)
}

func (s *APIServer) openURL(w http.ResponseWriter, open func() error) {
	if err := open(); err != nil {
		writeAPIFailure(w, err)
		return
	}
	writeEnvelope(w, http.StatusOK, map[string]bool{"opened": true})
}

func (s *APIServer) handleStart(w http.ResponseWriter, r *http.Request) {
	if err := s.app.StartRuntime(r.Context()); err != nil {
		writeAPIFailure(w, err)
		return
	}
	writeEnvelope(w, http.StatusOK, s.app.Stats(r.Context()))
}

func (s *APIServer) handleStop(w http.ResponseWriter, r *http.Request) {
	if err := s.app.StopRuntime(); err != nil {
		writeAPIFailure(w, err)
		return
	}
	writeEnvelope(w, http.StatusOK, s.app.Stats(r.Context()))
}

func (s *APIServer) handleRestart(w http.ResponseWriter, r *http.Request) {
	if err := s.app.RestartRuntime(r.Context()); err != nil {
		writeAPIFailure(w, err)
		return
	}
	writeEnvelope(w, http.StatusOK, s.app.Stats(r.Context()))
}

func (s *APIServer) handleShowWindow(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	show := s.show
	s.mu.Unlock()
	if show == nil {
		writeAPIError(w, http.StatusNotImplemented, "no window is attached to this tray")
		return
	}
	show()
	writeEnvelope(w, http.StatusOK, map[string]bool{"shown": true})
}

func (s *APIServer) handleQuit(w http.ResponseWriter, _ *http.Request) {
	s.app.RequestQuit()
	s.mu.Lock()
	quit := s.quit
	s.mu.Unlock()
	if quit != nil {
		quit()
	}
	writeEnvelope(w, http.StatusOK, map[string]bool{"quitting": true})
}

// decodeBody reads a size-bounded JSON request body.
func decodeBody(w http.ResponseWriter, r *http.Request, target any) error {
	body := http.MaxBytesReader(w, r.Body, maxRequestBody)
	defer func() { _ = body.Close() }()
	decoder := json.NewDecoder(body)
	// Unknown keys are rejected so a stale or hand-crafted payload cannot smuggle a
	// field the interface does not know about into a settings save.
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		if errors.Is(err, context.Canceled) {
			writeAPIError(w, http.StatusBadRequest, "the request was cancelled")
			return err
		}
		writeAPIError(w, http.StatusBadRequest, "the request body is not valid JSON: "+err.Error())
		return err
	}
	if decoder.More() {
		writeAPIError(w, http.StatusBadRequest, "the request body must hold exactly one JSON object")
		return errors.New("trailing content in the request body")
	}
	return nil
}

// writeAPIFailure maps an application error onto a status the interface can act on.
//
// 401 is reserved for "the secret is wrong". Reporting a missing remote token or an
// unusable configuration as 401 would make the front end reload itself looking for a
// secret that was never the problem.
func writeAPIFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, client.ErrRuntimeAlreadyRunning):
		writeAPIError(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrConfigUnusable):
		writeAPIError(w, http.StatusUnprocessableEntity, err.Error())
	default:
		writeAPIError(w, http.StatusBadRequest, err.Error())
	}
}

// writeEnvelope renders the project-wide {code,msg,data} shape.
func writeEnvelope(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": status, "msg": "OK", "data": data})
}

func writeAPIError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": status, "msg": message, "data": nil})
}
