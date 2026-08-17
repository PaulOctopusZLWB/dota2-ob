package main

import (
	"embed"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"path"
	"strings"
)

const defaultAddress = "127.0.0.1:18838"

//go:embed static fixtures
var overlayFiles embed.FS

var fixtureNames = map[string]struct{}{
	"healthy":             {},
	"draft-context":       {},
	"lane-checkpoint":     {},
	"item-timing-long":    {},
	"objective-exchange":  {},
	"teamfight-readiness": {},
	"missing-asset":       {},
	"stale":               {},
	"disconnected":        {},
	"emergency-hide":      {},
}

func main() {
	address := flag.String("addr", defaultAddress, "loopback listen address")
	flag.Parse()
	if err := validateListenAddress(*address); err != nil {
		log.Fatal(err)
	}
	log.Printf("OBS overlay spike listening on http://%s", *address)
	log.Fatal(http.ListenAndServe(*address, newHandler()))
}

func validateListenAddress(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("parse listen address: %w", err)
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("overlay spike must listen on loopback")
	}
	return nil
}

func newHandler() http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		setSafetyHeaders(response)
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			response.Header().Set("Allow", "GET, HEAD")
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		switch request.URL.Path {
		case "/", "/index.html":
			serveEmbedded(response, request, "static/index.html", "text/html; charset=utf-8")
		case "/overlay.css":
			serveEmbedded(response, request, "static/overlay.css", "text/css; charset=utf-8")
		case "/overlay.js":
			serveEmbedded(response, request, "static/overlay.js", "text/javascript; charset=utf-8")
		case "/healthz":
			response.Header().Set("Content-Type", "text/plain; charset=utf-8")
			response.WriteHeader(http.StatusOK)
			_, _ = response.Write([]byte("ok\n"))
		default:
			serveFixture(response, request)
		}
	})
}

func setSafetyHeaders(response http.ResponseWriter) {
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; img-src 'self'; script-src 'self'; style-src 'self'; font-src 'none'; object-src 'none'; base-uri 'none'; frame-ancestors 'self'")
	response.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	response.Header().Set("Referrer-Policy", "no-referrer")
	response.Header().Set("X-Content-Type-Options", "nosniff")
}

func serveFixture(response http.ResponseWriter, request *http.Request) {
	if !strings.HasPrefix(request.URL.Path, "/fixtures/") || path.Ext(request.URL.Path) != ".json" {
		http.NotFound(response, request)
		return
	}
	name := strings.TrimSuffix(strings.TrimPrefix(request.URL.Path, "/fixtures/"), ".json")
	if strings.ContainsAny(name, "/\\") {
		http.NotFound(response, request)
		return
	}
	if _, ok := fixtureNames[name]; !ok {
		http.NotFound(response, request)
		return
	}
	serveEmbedded(response, request, "fixtures/"+name+".json", "application/json; charset=utf-8")
}

func serveEmbedded(response http.ResponseWriter, request *http.Request, filename, contentType string) {
	data, err := overlayFiles.ReadFile(filename)
	if err != nil {
		http.NotFound(response, request)
		return
	}
	response.Header().Set("Content-Type", contentType)
	response.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
	response.WriteHeader(http.StatusOK)
	if request.Method != http.MethodHead {
		_, _ = response.Write(data)
	}
}
