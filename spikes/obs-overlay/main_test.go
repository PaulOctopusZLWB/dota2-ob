package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerServesOverlayWithBrowserSafetyHeaders(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/?fixture=healthy", nil)
	response := httptest.NewRecorder()

	newHandler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d", response.Code, http.StatusOK)
	}
	if got := response.Header().Get("Content-Security-Policy"); !strings.Contains(got, "default-src 'self'") {
		t.Fatalf("Content-Security-Policy = %q, want local-only default-src", got)
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if body := response.Body.String(); !strings.Contains(body, `lang="zh-CN"`) {
		t.Fatalf("overlay HTML does not declare zh-CN: %q", body)
	}
}

func TestHandlerServesOnlyKnownFixtureNames(t *testing.T) {
	tests := []struct {
		path string
		want int
	}{
		{path: "/fixtures/draft-context.json", want: http.StatusOK},
		{path: "/fixtures/../../go.mod", want: http.StatusNotFound},
		{path: "/fixtures/not-declared.json", want: http.StatusNotFound},
	}

	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			response := httptest.NewRecorder()
			newHandler().ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("GET %s status = %d, want %d", test.path, response.Code, test.want)
			}
		})
	}
}

func TestListenAddressMustBeLoopback(t *testing.T) {
	for _, address := range []string{"127.0.0.1:18838", "localhost:18838", "[::1]:18838"} {
		if err := validateListenAddress(address); err != nil {
			t.Errorf("validateListenAddress(%q) = %v, want nil", address, err)
		}
	}
	for _, address := range []string{"0.0.0.0:18838", ":18838", "192.0.2.1:18838"} {
		if err := validateListenAddress(address); err == nil {
			t.Errorf("validateListenAddress(%q) = nil, want rejection", address)
		}
	}
}
