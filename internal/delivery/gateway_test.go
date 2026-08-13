package delivery_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/delivery"
)

const testToken = "test-only-ephemeral-token"

type commandPort struct {
	calls  int
	last   contracts.OperatorCommandV1
	result contracts.OperatorCommandResultV1
	err    error
}

func (p *commandPort) Execute(_ context.Context, command contracts.OperatorCommandV1) (contracts.OperatorCommandResultV1, error) {
	p.calls++
	p.last = command
	return p.result, p.err
}

type overlayPort struct {
	state contracts.OverlayStateV1
	err   error
}

func (p *overlayPort) Current(context.Context) (contracts.OverlayStateV1, error) {
	return p.state, p.err
}

type operatorStatePort struct {
	state delivery.OperatorState
	err   error
}

func (p *operatorStatePort) Current(context.Context) (delivery.OperatorState, error) {
	return p.state, p.err
}

func validOperatorState() delivery.OperatorState {
	return delivery.OperatorState{
		SchemaVersion: "operator_state.v1", SessionID: "session-1", PolicyRevision: 4,
		EmergencyHidden: false,
		Previews: []delivery.OperatorPreview{{
			CandidateID: "candidate-1", RuleID: "lane-checkpoint", RuleVersion: "lane-checkpoint.v1",
			Confidence: "已观测", SampleSize: 12, ExpiresAtMS: 5_000,
			Claim: contracts.OverlayClaimV1{Title: "十分钟对线检查点", Body: "天辉经济领先 1800。", AssetKey: "lane"},
		}},
	}
}

func validCommand() contracts.OperatorCommandV1 {
	return contracts.OperatorCommandV1{
		SchemaVersion: contracts.OperatorCommandSchemaV1,
		CommandID:     "command-1", SessionID: "session-1", Action: contracts.ActionEmergencyHide,
		ExpectedPolicyRevision: 4, PolicyTimeMS: 100,
	}
}

func validResult() contracts.OperatorCommandResultV1 {
	return contracts.OperatorCommandResultV1{
		SchemaVersion: contracts.OperatorCommandResultSchemaV1,
		CommandID:     "command-1", SessionID: "session-1", Status: contracts.CommandAccepted,
		PreviousRevision: 4, ResultingRevision: 5, DecisionIDs: []string{}, Reason: "operator_emergency_hide",
	}
}

func validOverlay(publication int64) contracts.OverlayStateV1 {
	received := time.UnixMilli(publication - 100).UTC()
	return contracts.OverlayStateV1{
		SchemaVersion: contracts.OverlayStateSchemaV1, SessionID: "session-1",
		PublicationTimeMS: publication, StaleDeadlineMS: publication + 1_500, Visibility: "visible",
		DecisionID: "decision-1", Confidence: "high", SourceReceiveTime: &received,
		Evidence: []contracts.EvidenceRefV1{{
			RecordSchemaVersion: 1, SessionID: "session-1", Sequence: 1, ReceiveTime: received,
			Source: "gsi", ProviderVersion: contracts.Absent[int64](), RawPayloadSHA256: strings.Repeat("a", 64),
		}},
		Claim: &contracts.OverlayClaimV1{Title: "团战窗口", Body: "经济领先可转化为肉山控制。", AssetKey: "roshan"},
	}
}

func testGateway(t *testing.T, commands *commandPort, overlay *overlayPort, now *time.Time) http.Handler {
	t.Helper()
	assets := fstest.MapFS{
		"operator/index.html":                       {Data: []byte(`<html lang="zh-CN">operator</html>`)},
		"operator/app.js":                           {Data: []byte(`window.operatorApp = true`)},
		"operator/app.css":                          {Data: []byte(`body{color:white}`)},
		"overlay/index.html":                        {Data: []byte(`<html lang="zh-CN">overlay</html>`)},
		"overlay/app.js":                            {Data: []byte(`fetch("/v1/overlay/state")`)},
		"overlay/app.css":                           {Data: []byte(`body{background:transparent}`)},
		"overlay/fonts/noto-sans-cjk-sc-dot24.woff": {Data: []byte(`wOFF-local-font`)},
	}
	gateway, err := delivery.NewGateway(delivery.Config{
		BearerToken: testToken, AllowedOrigin: "http://127.0.0.1:43211",
		Commands: commands, Operator: &operatorStatePort{state: validOperatorState()}, Overlay: overlay,
		ReadAsset: func(name string) ([]byte, error) { return fs.ReadFile(assets, name) }, Now: func() time.Time { return *now },
	})
	if err != nil {
		t.Fatal(err)
	}
	return gateway
}

func TestOperatorStateIsAuthenticatedBoundedAndReadOnly(t *testing.T) {
	now := time.UnixMilli(1_000).UTC()
	commands := &commandPort{result: validResult()}
	handler := testGateway(t, commands, &overlayPort{state: validOverlay(1_000)}, &now)

	for _, tc := range []struct {
		name    string
		headers map[string]string
		want    int
	}{
		{name: "missing bearer", headers: map[string]string{"Origin": "http://127.0.0.1:43211"}, want: http.StatusUnauthorized},
		{name: "hostile origin", headers: map[string]string{"Authorization": "Bearer " + testToken, "Origin": "https://hostile.invalid"}, want: http.StatusForbidden},
		{name: "null origin", headers: map[string]string{"Authorization": "Bearer " + testToken, "Origin": "null"}, want: http.StatusForbidden},
		{name: "authorized", headers: map[string]string{"Authorization": "Bearer " + testToken, "Origin": "http://127.0.0.1:43211"}, want: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := doRequest(handler, http.MethodGet, "/v1/operator/state", "", tc.headers)
			if rec.Code != tc.want || rec.Body.Len() > delivery.MaxResponseBytes {
				t.Fatalf("status=%d want=%d len=%d body=%s", rec.Code, tc.want, rec.Body.Len(), rec.Body.String())
			}
			if commands.calls != 0 || strings.Contains(rec.Body.String(), testToken) || strings.Contains(rec.Body.String(), "raw_gsi") {
				t.Fatalf("operator state leaked capability or mutated policy: calls=%d body=%s", commands.calls, rec.Body.String())
			}
		})
	}
	if rec := doRequest(handler, http.MethodPost, "/v1/operator/state", "{}", authHeaders()); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("operator state POST status=%d", rec.Code)
	}
}

func TestOperatorStateFailsClosedOnInvalidOrUnavailableView(t *testing.T) {
	now := time.UnixMilli(1_000).UTC()
	assets := fstest.MapFS{"operator/index.html": {Data: []byte("ok")}}
	for _, port := range []*operatorStatePort{
		{err: errors.New("/private/operator-state")},
		{state: delivery.OperatorState{SchemaVersion: "operator_state.v1", SessionID: "session-1", Previews: []delivery.OperatorPreview{{CandidateID: "candidate", Claim: contracts.OverlayClaimV1{Title: "<unsafe>", Body: "x"}}}}},
	} {
		gateway, err := delivery.NewGateway(delivery.Config{
			BearerToken: testToken, AllowedOrigin: "http://127.0.0.1:43211", Commands: &commandPort{result: validResult()},
			Operator: port, Overlay: &overlayPort{state: validOverlay(1_000)},
			ReadAsset: func(name string) ([]byte, error) { return fs.ReadFile(assets, name) }, Now: func() time.Time { return now },
		})
		if err != nil {
			t.Fatal(err)
		}
		rec := doRequest(gateway, http.MethodGet, "/v1/operator/state", "", map[string]string{"Authorization": "Bearer " + testToken, "Origin": "http://127.0.0.1:43211"})
		if rec.Code != http.StatusServiceUnavailable || strings.Contains(rec.Body.String(), "private") || strings.Contains(rec.Body.String(), "unsafe") {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	}
}

func doRequest(handler http.Handler, method, target, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func authHeaders() map[string]string {
	return map[string]string{
		"Authorization":     "Bearer " + testToken,
		"Content-Type":      delivery.JSONContentType,
		delivery.CSRFHeader: delivery.CSRFValue,
		"Origin":            "http://127.0.0.1:43211",
	}
}

func TestOperatorCommandRequiresPrivilegeAndPassesOnlyTypedCommand(t *testing.T) {
	now := time.UnixMilli(1_000).UTC()
	commands := &commandPort{result: validResult()}
	handler := testGateway(t, commands, &overlayPort{state: validOverlay(1_000)}, &now)
	body, _ := json.Marshal(validCommand())
	rec := doRequest(handler, http.MethodPost, "/v1/operator/commands", string(body), authHeaders())
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if commands.calls != 1 || commands.last.CommandID != "command-1" {
		t.Fatalf("command calls=%d last=%#v", commands.calls, commands.last)
	}
	var result contracts.OperatorCommandResultV1
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil || result.Status != contracts.CommandAccepted {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("permissive CORS=%q", got)
	}
}

func TestGatewayRejectsNonLoopbackOrNonHTTPOperatorOrigins(t *testing.T) {
	assets := fstest.MapFS{"operator/index.html": {Data: []byte("ok")}}
	for _, origin := range []string{"https://127.0.0.1:43211", "http://192.0.2.1:43211", "http://127.0.0.1:43211/path", "http://user@127.0.0.1:43211"} {
		_, err := delivery.NewGateway(delivery.Config{
			BearerToken: testToken, AllowedOrigin: origin,
			Commands: &commandPort{result: validResult()}, Overlay: &overlayPort{state: validOverlay(1_000)},
			ReadAsset: func(name string) ([]byte, error) { return fs.ReadFile(assets, name) },
		})
		if err == nil {
			t.Fatalf("origin %q accepted", origin)
		}
	}
}

func TestOperatorHostileRequestMatrixFailsBeforeCommandPort(t *testing.T) {
	now := time.UnixMilli(1_000).UTC()
	body, _ := json.Marshal(validCommand())
	tests := []struct {
		name, method, body string
		headers            map[string]string
		want               int
	}{
		{name: "unauthenticated probe", method: http.MethodGet, body: "", headers: nil, want: http.StatusUnauthorized},
		{name: "wrong method", method: http.MethodGet, body: string(body), headers: authHeaders(), want: http.StatusMethodNotAllowed},
		{name: "wrong content type", method: http.MethodPost, body: string(body), headers: map[string]string{"Authorization": "Bearer " + testToken, delivery.CSRFHeader: delivery.CSRFValue}, want: http.StatusUnsupportedMediaType},
		{name: "cross origin", method: http.MethodPost, body: string(body), headers: map[string]string{"Authorization": "Bearer " + testToken, "Content-Type": delivery.JSONContentType, delivery.CSRFHeader: delivery.CSRFValue, "Origin": "https://hostile.invalid"}, want: http.StatusForbidden},
		{name: "null origin", method: http.MethodPost, body: string(body), headers: map[string]string{"Authorization": "Bearer " + testToken, "Content-Type": delivery.JSONContentType, delivery.CSRFHeader: delivery.CSRFValue, "Origin": "null"}, want: http.StatusForbidden},
		{name: "cookie only", method: http.MethodPost, body: string(body), headers: map[string]string{"Cookie": "token=" + testToken, "Content-Type": delivery.JSONContentType, delivery.CSRFHeader: delivery.CSRFValue, "Origin": "http://127.0.0.1:43211"}, want: http.StatusUnauthorized},
		{name: "wrong bearer", method: http.MethodPost, body: string(body), headers: map[string]string{"Authorization": "Bearer wrong", "Content-Type": delivery.JSONContentType, delivery.CSRFHeader: delivery.CSRFValue, "Origin": "http://127.0.0.1:43211"}, want: http.StatusUnauthorized},
		{name: "missing csrf", method: http.MethodPost, body: string(body), headers: map[string]string{"Authorization": "Bearer " + testToken, "Content-Type": delivery.JSONContentType, "Origin": "http://127.0.0.1:43211"}, want: http.StatusForbidden},
		{name: "wrong csrf", method: http.MethodPost, body: string(body), headers: map[string]string{"Authorization": "Bearer " + testToken, "Content-Type": delivery.JSONContentType, delivery.CSRFHeader: "wrong", "Origin": "http://127.0.0.1:43211"}, want: http.StatusForbidden},
		{name: "oversized", method: http.MethodPost, body: strings.Repeat(" ", delivery.MaxCommandBodyBytes+1), headers: authHeaders(), want: http.StatusRequestEntityTooLarge},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			commands := &commandPort{result: validResult()}
			rec := doRequest(testGateway(t, commands, &overlayPort{state: validOverlay(1_000)}, &now), tc.method, "/v1/operator/commands", tc.body, tc.headers)
			if rec.Code != tc.want || commands.calls != 0 {
				t.Fatalf("status=%d want=%d calls=%d body=%s", rec.Code, tc.want, commands.calls, rec.Body.String())
			}
			var rejection delivery.Rejection
			if err := json.Unmarshal(rec.Body.Bytes(), &rejection); err != nil || rejection.Status != contracts.CommandRejected || rejection.Reason == "" {
				t.Fatalf("non-deterministic rejection=%#v err=%v", rejection, err)
			}
			if rec.Header().Get("Access-Control-Allow-Origin") != "" {
				t.Fatal("hostile response enabled CORS")
			}
		})
	}
}

func TestOperatorRateLimitIsTwentyCommandsPerTenSeconds(t *testing.T) {
	now := time.UnixMilli(1_000).UTC()
	commands := &commandPort{result: validResult()}
	handler := testGateway(t, commands, &overlayPort{state: validOverlay(1_000)}, &now)
	body, _ := json.Marshal(validCommand())
	for i := 0; i < delivery.MaxCommandsPerWindow; i++ {
		rec := doRequest(handler, http.MethodPost, "/v1/operator/commands", string(body), authHeaders())
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d status=%d", i+1, rec.Code)
		}
	}
	if rec := doRequest(handler, http.MethodPost, "/v1/operator/commands", string(body), authHeaders()); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("rate-limit status=%d body=%s", rec.Code, rec.Body.String())
	}
	if commands.calls != delivery.MaxCommandsPerWindow {
		t.Fatalf("calls=%d", commands.calls)
	}
	now = now.Add(delivery.CommandWindow)
	if rec := doRequest(handler, http.MethodPost, "/v1/operator/commands", string(body), authHeaders()); rec.Code != http.StatusOK {
		t.Fatalf("post-window status=%d", rec.Code)
	}
}

func TestCommandPortFailureAndUnsafeResponseAreBoundedDeterministic(t *testing.T) {
	now := time.UnixMilli(1_000).UTC()
	command := validCommand()
	body, _ := json.Marshal(command)
	for _, tc := range []struct {
		name   string
		port   *commandPort
		want   int
		reason string
	}{
		{name: "port failure", port: &commandPort{err: errors.New("/secret/path")}, want: http.StatusServiceUnavailable, reason: "command_unavailable"},
		{name: "invalid result", port: &commandPort{result: contracts.OperatorCommandResultV1{Reason: strings.Repeat("x", delivery.MaxResponseBytes+1)}}, want: http.StatusBadGateway, reason: "invalid_command_result"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := doRequest(testGateway(t, tc.port, &overlayPort{state: validOverlay(1_000)}, &now), http.MethodPost, "/v1/operator/commands", string(body), authHeaders())
			if rec.Code != tc.want || rec.Body.Len() > delivery.MaxResponseBytes || strings.Contains(rec.Body.String(), "secret") {
				t.Fatalf("status=%d len=%d body=%s", rec.Code, rec.Body.Len(), rec.Body.String())
			}
			var rejection delivery.Rejection
			_ = json.Unmarshal(rec.Body.Bytes(), &rejection)
			if rejection.Reason != tc.reason {
				t.Fatalf("reason=%q", rejection.Reason)
			}
		})
	}
}

func TestCommandAdmissionErrorsRemainDeterministicClientRejections(t *testing.T) {
	now := time.UnixMilli(1_000).UTC()
	body, _ := json.Marshal(validCommand())
	for _, tc := range []struct {
		name, reason string
		err          error
		want         int
	}{
		{name: "session capacity", reason: "session_command_limit", err: contracts.ErrSessionCommandLimit, want: http.StatusConflict},
		{name: "identifier bound", reason: "policy_identifier_limit", err: contracts.ErrPolicyIdentifierLimit, want: http.StatusBadRequest},
		{name: "malformed v2 admission", reason: "invalid_command", err: contracts.ErrMalformedCommand, want: http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			port := &commandPort{err: tc.err}
			rec := doRequest(testGateway(t, port, &overlayPort{state: validOverlay(1_000)}, &now), http.MethodPost, "/v1/operator/commands", string(body), authHeaders())
			var rejection delivery.Rejection
			if err := json.Unmarshal(rec.Body.Bytes(), &rejection); err != nil || rec.Code != tc.want || rejection.Reason != tc.reason || port.calls != 1 {
				t.Fatalf("status=%d rejection=%#v calls=%d decode=%v", rec.Code, rejection, port.calls, err)
			}
		})
	}
}

func TestOverlayRouteIsReadOnlyUnauthenticatedAndFailsClosed(t *testing.T) {
	now := time.UnixMilli(2_000).UTC()
	commands := &commandPort{result: validResult()}
	overlay := &overlayPort{state: validOverlay(2_000)}
	handler := testGateway(t, commands, overlay, &now)
	rec := doRequest(handler, http.MethodGet, "/v1/overlay/state", "", nil)
	if rec.Code != http.StatusOK || rec.Body.Len() > contracts.MaxOverlayBytes {
		t.Fatalf("status=%d len=%d body=%s", rec.Code, rec.Body.Len(), rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), testToken) || rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("overlay leaked privilege: headers=%v body=%s", rec.Header(), rec.Body.String())
	}
	if rec := doRequest(handler, http.MethodPost, "/v1/overlay/state", `{}`, authHeaders()); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("overlay POST status=%d", rec.Code)
	}
	if commands.calls != 0 {
		t.Fatalf("overlay reached command port calls=%d", commands.calls)
	}

	unsafe := []struct {
		name   string
		mutate func(*overlayPort, *time.Time)
	}{
		{name: "malformed", mutate: func(p *overlayPort, _ *time.Time) { p.state.SchemaVersion = "unknown" }},
		{name: "stale", mutate: func(p *overlayPort, now *time.Time) { *now = time.UnixMilli(p.state.StaleDeadlineMS + 1).UTC() }},
		{name: "oversized", mutate: func(p *overlayPort, _ *time.Time) { p.state.Claim.Body = strings.Repeat("界", 65<<10) }},
		{name: "source failure", mutate: func(p *overlayPort, _ *time.Time) { p.err = errors.New("capture must not block") }},
	}
	for _, tc := range unsafe {
		t.Run(tc.name, func(t *testing.T) {
			testNow := time.UnixMilli(2_000).UTC()
			port := &overlayPort{state: validOverlay(2_000)}
			tc.mutate(port, &testNow)
			rec := doRequest(testGateway(t, commands, port, &testNow), http.MethodGet, "/v1/overlay/state", "", nil)
			if rec.Code != http.StatusServiceUnavailable || rec.Body.Len() > delivery.MaxResponseBytes {
				t.Fatalf("status=%d len=%d body=%s", rec.Code, rec.Body.Len(), rec.Body.String())
			}
		})
	}
}

func TestOverlayRejectsOutOfOrderState(t *testing.T) {
	now := time.UnixMilli(3_000).UTC()
	port := &overlayPort{state: validOverlay(3_000)}
	handler := testGateway(t, &commandPort{result: validResult()}, port, &now)
	if rec := doRequest(handler, http.MethodGet, "/v1/overlay/state", "", nil); rec.Code != http.StatusOK {
		t.Fatalf("first=%d", rec.Code)
	}
	port.state = validOverlay(2_999)
	if rec := doRequest(handler, http.MethodGet, "/v1/overlay/state", "", nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("older=%d", rec.Code)
	}
}

func TestStaticEntryPointsHaveSeparateCSPAndNoPrivilegeLeakage(t *testing.T) {
	now := time.UnixMilli(1_000).UTC()
	handler := testGateway(t, &commandPort{result: validResult()}, &overlayPort{state: validOverlay(1_000)}, &now)
	operator := doRequest(handler, http.MethodGet, "/operator/", "", nil)
	overlay := doRequest(handler, http.MethodGet, "/overlay/", "", nil)
	if operator.Code != http.StatusOK || overlay.Code != http.StatusOK {
		t.Fatalf("static statuses=%d/%d", operator.Code, overlay.Code)
	}
	font := doRequest(handler, http.MethodGet, "/overlay/fonts/noto-sans-cjk-sc-dot24.woff", "", nil)
	if font.Code != http.StatusOK || font.Header().Get("Content-Type") != "font/woff" {
		t.Fatalf("font status=%d content-type=%q", font.Code, font.Header().Get("Content-Type"))
	}
	operatorCSP, overlayCSP := operator.Header().Get("Content-Security-Policy"), overlay.Header().Get("Content-Security-Policy")
	if operatorCSP == overlayCSP || !strings.Contains(operatorCSP, "connect-src http://127.0.0.1:43211/v1/operator/commands") || !strings.Contains(overlayCSP, "connect-src http://127.0.0.1:43211/v1/overlay/state") || !strings.Contains(operatorCSP, "frame-ancestors 'none'") || !strings.Contains(overlayCSP, "frame-ancestors 'self'") {
		t.Fatalf("CSP operator=%q overlay=%q", operatorCSP, overlayCSP)
	}
	for _, target := range []string{"/overlay/", "/overlay/app.js", "/overlay/app.css"} {
		rec := doRequest(handler, http.MethodGet, target, "", nil)
		body, _ := io.ReadAll(rec.Result().Body)
		text := string(body)
		for _, forbidden := range []string{testToken, "/v1/operator", "/gsi", "/api/", "history", "storage", "obs-websocket"} {
			if strings.Contains(strings.ToLower(text), strings.ToLower(forbidden)) {
				t.Fatalf("%s contains %q", target, forbidden)
			}
		}
	}
	for _, target := range []string{"/api/latest", "/gsi", "/v1/operator/results", "/v1/overlay/command"} {
		if rec := doRequest(handler, http.MethodGet, target, "", nil); rec.Code != http.StatusNotFound {
			t.Fatalf("route probe %s status=%d", target, rec.Code)
		}
	}
}
