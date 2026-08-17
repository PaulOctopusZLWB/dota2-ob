// Package obscontrol owns the optional, separately authenticated OBS
// scene/source visibility boundary. Browser Source rendering never depends on
// this adapter, and the package exposes no analytical mutation API.
package obscontrol

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
)

var ErrUnavailable = errors.New("OBS visibility control unavailable")

// VisibilityClient is the narrow transport port implemented by an optional
// obs-websocket client outside this dependency-safe package.
type VisibilityClient interface {
	SetSourceVisibility(context.Context, string, string, bool) error
}

type Config struct {
	Endpoint      string
	Password      string
	AllowedScene  string
	AllowedSource string
	Client        VisibilityClient
}

type Adapter struct {
	scene, source string
	client        VisibilityClient
}

func New(config Config) (*Adapter, error) {
	if !validEndpoint(config.Endpoint) || strings.TrimSpace(config.Password) == "" || config.Client == nil {
		return nil, errors.New("invalid OBS control configuration")
	}
	if config.AllowedScene == "" {
		config.AllowedScene = "Dota2-OB M3"
	}
	if config.AllowedSource == "" {
		config.AllowedSource = "Analytics Sidebar"
	}
	if !safeName(config.AllowedScene) || !safeName(config.AllowedSource) {
		return nil, errors.New("invalid OBS scene or source")
	}
	return &Adapter{scene: config.AllowedScene, source: config.AllowedSource, client: config.Client}, nil
}

func validEndpoint(endpoint string) bool {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "ws" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
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

func safeName(value string) bool {
	if len(value) > 128 {
		return false
	}
	for _, r := range value {
		if r <= 0x1f || r == 0x7f {
			return false
		}
	}
	return value != ""
}

// SetVisible changes only the allowlisted Browser Source visibility. Transport
// detail is deliberately collapsed to a stable error without leaking the
// endpoint, password, scene, or source.
func (a *Adapter) SetVisible(ctx context.Context, visible bool) error {
	if err := a.client.SetSourceVisibility(ctx, a.scene, a.source, visible); err != nil {
		return ErrUnavailable
	}
	return nil
}
