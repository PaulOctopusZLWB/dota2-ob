package delivery

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
)

const (
	JSONContentType      = "application/json"
	CSRFHeader           = "X-Dota2-OB-CSRF"
	CSRFValue            = "operator-command"
	MaxCommandBodyBytes  = 16 << 10
	MaxResponseBytes     = 64 << 10
	MaxCommandsPerWindow = 20
	CommandWindow        = 10 * time.Second
)

const (
	operatorCSPPrefix = "default-src 'none'; connect-src "
	operatorCSPSuffix = "; script-src 'self'; style-src 'self'; img-src 'self'; font-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
	overlayCSPPrefix  = "default-src 'none'; connect-src "
	overlayCSPSuffix  = "; script-src 'self'; style-src 'self'; img-src 'self'; font-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'self'"
)

// CommandPort is the narrow policy command/result boundary owned by the policy
// track. Delivery never imports a concrete policy implementation.
type CommandPort interface {
	Execute(context.Context, contracts.OperatorCommandV1) (contracts.OperatorCommandResultV1, error)
}

// OverlayPort exposes only the latest committed presentation contract.
type OverlayPort interface {
	Current(context.Context) (contracts.OverlayStateV1, error)
}

type Config struct {
	BearerToken   string
	AllowedOrigin string
	Commands      CommandPort
	Overlay       OverlayPort
	ReadAsset     func(string) ([]byte, error)
	Now           func() time.Time
}

type Gateway struct {
	token         string
	allowedOrigin string
	commands      CommandPort
	overlay       OverlayPort
	readAsset     func(string) ([]byte, error)
	now           func() time.Time
	rateMu        sync.Mutex
	commandTimes  []time.Time
	overlayMu     sync.Mutex
	lastOverlayMS int64
	operatorCSP   string
	overlayCSP    string
}

type Rejection struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
}

func NewGateway(config Config) (*Gateway, error) {
	if strings.TrimSpace(config.BearerToken) == "" || !validLoopbackOrigin(config.AllowedOrigin) || config.Commands == nil || config.Overlay == nil || config.ReadAsset == nil {
		return nil, errors.New("invalid delivery gateway configuration")
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	return &Gateway{
		token: config.BearerToken, allowedOrigin: strings.TrimRight(config.AllowedOrigin, "/"),
		commands: config.Commands, overlay: config.Overlay, readAsset: config.ReadAsset, now: config.Now,
		operatorCSP: operatorCSPPrefix + strings.TrimRight(config.AllowedOrigin, "/") + "/v1/operator/commands" + operatorCSPSuffix,
		overlayCSP:  overlayCSPPrefix + strings.TrimRight(config.AllowedOrigin, "/") + "/v1/overlay/state" + overlayCSPSuffix,
	}, nil
}

func validLoopbackOrigin(origin string) bool {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	host, portText, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		return false
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return false
	}
	return strings.EqualFold(host, "localhost") || (net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback())
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setCommonHeaders(w)
	switch {
	case r.URL.Path == "/v1/operator/commands":
		w.Header().Set("Content-Security-Policy", g.operatorCSP)
		g.handleCommand(w, r)
	case r.URL.Path == "/v1/overlay/state":
		w.Header().Set("Content-Security-Policy", g.overlayCSP)
		g.handleOverlay(w, r)
	case r.URL.Path == "/operator" || r.URL.Path == "/operator/" || strings.HasPrefix(r.URL.Path, "/operator/"):
		w.Header().Set("Content-Security-Policy", g.operatorCSP)
		g.serveAsset(w, r, "operator")
	case r.URL.Path == "/overlay" || r.URL.Path == "/overlay/" || strings.HasPrefix(r.URL.Path, "/overlay/"):
		w.Header().Set("Content-Security-Policy", g.overlayCSP)
		g.serveAsset(w, r, "overlay")
	default:
		http.NotFound(w, r)
	}
}

func setCommonHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "SAMEORIGIN")
}

func (g *Gateway) handleCommand(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r.Header.Get("Origin"), g.allowedOrigin) {
		writeRejection(w, http.StatusForbidden, "origin_rejected")
		return
	}
	if !bearerMatches(r.Header.Get("Authorization"), g.token) {
		writeRejection(w, http.StatusUnauthorized, "bearer_required")
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeRejection(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != JSONContentType {
		writeRejection(w, http.StatusUnsupportedMediaType, "content_type_required")
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get(CSRFHeader)), []byte(CSRFValue)) != 1 {
		writeRejection(w, http.StatusForbidden, "csrf_rejected")
		return
	}
	if !g.allowCommand(g.now()) {
		writeRejection(w, http.StatusTooManyRequests, "rate_limited")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxCommandBodyBytes))
	if err != nil {
		writeRejection(w, http.StatusRequestEntityTooLarge, "body_too_large")
		return
	}
	var command contracts.OperatorCommandV1
	if err := contracts.DecodeStrict(body, &command); err != nil || command.Validate() != nil {
		writeRejection(w, http.StatusBadRequest, "invalid_command")
		return
	}
	result, err := g.commands.Execute(r.Context(), command)
	if err != nil {
		writeRejection(w, http.StatusServiceUnavailable, "command_unavailable")
		return
	}
	if err := result.Validate(); err != nil || result.CommandID != command.CommandID || result.SessionID != command.SessionID {
		writeRejection(w, http.StatusBadGateway, "invalid_command_result")
		return
	}
	encoded, err := contracts.MarshalCanonical(result)
	if err != nil || len(encoded) > MaxResponseBytes {
		writeRejection(w, http.StatusBadGateway, "invalid_command_result")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(encoded)
}

func sameOrigin(origin, allowed string) bool {
	return origin == "" || (origin != "null" && strings.TrimRight(origin, "/") == allowed)
}

func bearerMatches(header, token string) bool {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	provided := strings.TrimPrefix(header, prefix)
	return len(provided) == len(token) && subtle.ConstantTimeCompare([]byte(provided), []byte(token)) == 1
}

func (g *Gateway) allowCommand(now time.Time) bool {
	g.rateMu.Lock()
	defer g.rateMu.Unlock()
	cutoff := now.Add(-CommandWindow)
	first := 0
	for first < len(g.commandTimes) && !g.commandTimes[first].After(cutoff) {
		first++
	}
	g.commandTimes = append(g.commandTimes[:0], g.commandTimes[first:]...)
	if len(g.commandTimes) >= MaxCommandsPerWindow {
		return false
	}
	g.commandTimes = append(g.commandTimes, now)
	return true
}

func (g *Gateway) handleOverlay(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeRejection(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	state, err := g.overlay.Current(r.Context())
	if err != nil || state.Validate() != nil {
		writeRejection(w, http.StatusServiceUnavailable, "overlay_unavailable")
		return
	}
	nowMS := g.now().UnixMilli()
	if state.PublicationTimeMS > nowMS || nowMS > state.StaleDeadlineMS {
		writeRejection(w, http.StatusServiceUnavailable, "overlay_unsafe")
		return
	}
	g.overlayMu.Lock()
	if state.PublicationTimeMS < g.lastOverlayMS {
		g.overlayMu.Unlock()
		writeRejection(w, http.StatusServiceUnavailable, "overlay_out_of_order")
		return
	}
	g.lastOverlayMS = state.PublicationTimeMS
	g.overlayMu.Unlock()
	encoded, err := contracts.MarshalCanonical(state)
	if err != nil || len(encoded) > contracts.MaxOverlayBytes || len(encoded) > MaxResponseBytes {
		writeRejection(w, http.StatusServiceUnavailable, "overlay_unsafe")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(encoded)
	}
}

func (g *Gateway) serveAsset(w http.ResponseWriter, r *http.Request, root string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeRejection(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/"+root)
	if name == "" || name == "/" {
		name = "/index.html"
	}
	name = strings.TrimPrefix(path.Clean(name), "/")
	if strings.Contains(name, "..") || !allowedAsset(name) {
		http.NotFound(w, r)
		return
	}
	filename := root + "/" + name
	data, err := g.readAsset(filename)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	switch path.Ext(name) {
	case ".html":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	case ".js":
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	case ".css":
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	default:
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(data)
	}
}

func allowedAsset(name string) bool {
	return name == "index.html" || name == "app.js" || name == "app.css"
}

func writeRejection(w http.ResponseWriter, status int, reason string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	encoded, _ := json.Marshal(Rejection{Status: contracts.CommandRejected, Reason: reason})
	if len(encoded) <= MaxResponseBytes {
		_, _ = w.Write(encoded)
	}
}
