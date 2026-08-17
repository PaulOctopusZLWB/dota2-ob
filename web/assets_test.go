package web

import (
	"io/fs"
	"strings"
	"testing"
)

func TestOperatorAndOverlayAreSeparateLocalBundles(t *testing.T) {
	assets := Assets()
	paths := []string{
		"operator/index.html", "operator/app.js", "operator/app.css",
		"overlay/index.html", "overlay/app.js", "overlay/app.css",
		"overlay/fonts/noto-sans-cjk-sc-dot24.woff", "overlay/fonts/LICENSE.txt",
	}
	font, _ := fs.ReadFile(assets, "overlay/fonts/noto-sans-cjk-sc-dot24.woff")
	if len(font) > 128<<10 || !strings.HasPrefix(string(font), "wOFF") {
		t.Fatalf("overlay font must be a bounded local WOFF asset, size=%d", len(font))
	}
	for _, name := range paths {
		data, err := fs.ReadFile(assets, name)
		if err != nil || len(data) == 0 {
			t.Fatalf("asset %s len=%d err=%v", name, len(data), err)
		}
		text := strings.ToLower(string(data))
		for _, remote := range []string{"https://", "http://", "//cdn", "@import"} {
			if strings.Contains(text, remote) {
				t.Fatalf("asset %s references %q", name, remote)
			}
		}
	}

	overlayJS, _ := fs.ReadFile(assets, "overlay/app.js")
	for _, want := range []string{"/v1/overlay/state", "publication_time_ms", "stale_deadline_ms", "overlay_state.v1", "latestsettledgeneration"} {
		if !strings.Contains(strings.ToLower(string(overlayJS)), want) {
			t.Fatalf("overlay JS missing %q", want)
		}
	}
	for _, forbidden := range []string{"/v1/operator", "authorization", "bearer", "cookie", "localstorage", "sessionstorage", "/api/", "/gsi", "raw_gsi", "history", "storage", "obs-websocket"} {
		if strings.Contains(strings.ToLower(string(overlayJS)), forbidden) {
			t.Fatalf("overlay JS contains forbidden capability %q", forbidden)
		}
	}

	operatorJS, _ := fs.ReadFile(assets, "operator/app.js")
	for _, want := range []string{"/v1/operator/commands", "/v1/operator/state", "authorization", "x-dota2-ob-csrf", "crypto.randomuuid", "expected_policy_revision"} {
		if !strings.Contains(strings.ToLower(string(operatorJS)), want) {
			t.Fatalf("operator JS missing %q", want)
		}
	}
	for _, forbidden := range []string{"/v1/overlay/state", "/api/", "/gsi", "localstorage", "sessionstorage", "document.cookie"} {
		if strings.Contains(strings.ToLower(string(operatorJS)), forbidden) {
			t.Fatalf("operator JS contains forbidden capability %q", forbidden)
		}
	}
}

func TestOperatorConsoleExposesStructuredRevisionCheckedControls(t *testing.T) {
	assets := Assets()
	html, _ := fs.ReadFile(assets, "operator/index.html")
	css, _ := fs.ReadFile(assets, "operator/app.css")
	for _, want := range []string{
		`id="connect-form"`, `id="candidate-list"`, `id="rule-form"`, `id="emergency-toggle"`,
		`id="retry-command"`, `id="policy-revision"`, `aria-live="polite"`,
	} {
		if !strings.Contains(strings.ToLower(string(html)), want) {
			t.Fatalf("operator HTML missing %q", want)
		}
	}
	for _, want := range []string{"--alert", ".candidate-card", ".emergency", "@media"} {
		if !strings.Contains(strings.ToLower(string(css)), want) {
			t.Fatalf("operator CSS missing %q", want)
		}
	}
}

func TestOverlayPreservesAcceptedTransparentSafeAreaMarkers(t *testing.T) {
	assets := Assets()
	html, _ := fs.ReadFile(assets, "overlay/index.html")
	css, _ := fs.ReadFile(assets, "overlay/app.css")
	js, _ := fs.ReadFile(assets, "overlay/app.js")
	for _, want := range []string{`lang="zh-cn"`, `id="overlay-card"`, `aria-hidden="true"`, `/overlay/app.css`, `/overlay/app.js`} {
		if !strings.Contains(strings.ToLower(string(html)), want) {
			t.Fatalf("overlay HTML missing %q", want)
		}
	}
	for _, want := range []string{"background: transparent", "overflow: hidden", "clamp(", "@media (max-width: 1600px)"} {
		if !strings.Contains(strings.ToLower(string(css)), want) {
			t.Fatalf("overlay CSS missing %q", want)
		}
	}
	for _, want := range []string{"dataset.renderstate = \"hidden\"", "connection-timeout", "invalid-state", "out-of-order"} {
		if !strings.Contains(strings.ToLower(string(js)), want) {
			t.Fatalf("overlay JS missing fail-closed marker %q", want)
		}
	}
}
