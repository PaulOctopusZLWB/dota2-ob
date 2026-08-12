package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/atomicfile"
	"github.com/klauspost/compress/zstd"
)

func TestValidateMatchID(t *testing.T) {
	cases := []struct {
		id   string
		want bool
	}{
		{"8941092540", true},
		{"1", true},
		{"", false},
		{"abc", false},
		{"8941abc", false},
		{"123456789012345678901", false}, // too long
		{"../escape", false},
		{"123/456", false},
	}
	for _, c := range cases {
		err := validateMatchID(c.id)
		if (err == nil) != c.want {
			t.Errorf("validateMatchID(%q) err=%v, want ok=%v", c.id, err, c.want)
		}
	}
}

func TestValidateReplayURLAcceptlist(t *testing.T) {
	good := []string{
		"http://replay273.valve.net/570/8941092540_1595018738.dem.bz2",
		"https://replay1.valve.net/570/123_456.dem.bz2",
	}
	for _, u := range good {
		if err := validateReplayURL(u, ""); err != nil {
			t.Errorf("validateReplayURL(%q) unexpected err: %v", u, err)
		}
	}
	bad := []string{
		"",                                    // no url
		"ftp://replay273.valve.net/x.dem.bz2", // bad scheme
		"http://replay273.valve.net.evil.com/x.dem.bz2",     // not valve suffix
		"http://replay.valve.net/x.dem.bz2",                 // missing digits
		"http://replayabc.valve.net/x.dem.bz2",              // non-numeric suffix
		"http://evil.com/570/8941092540_1595018738.dem.bz2", // untrusted host
		"http://replay273.valve.net",                        // missing replay path
		"http://replay273.evil.net/x.dem.bz2",               // wrong domain
		"http://user@replay273.valve.net/570/123_456.dem.bz2",
		"http://replay273.valve.net:80/570/123_456.dem.bz2",
		"http://replay273.valve.net/570/123_456.dem.bz2?q=x",
		"http://replay273.valve.net/570/123_456.dem.bz2#x",
		"http://replay273.valve.net/571/123_456.dem.bz2",
		"http://replay273.valve.net/570/abc_456.dem.bz2",
		"http://replay273.valve.net/570/123_salt.dem.bz2",
		"http://replay1.valve.net/570/123_456.dem.bz2?",
		"http://replay1.valve.net/570/123_456.dem.bz2#",
		"http://replay1.valve.net:/570/123_456.dem.bz2",
	}
	for _, u := range bad {
		if err := validateReplayURL(u, ""); err == nil {
			t.Errorf("validateReplayURL(%q) expected rejection, got nil", u)
		}
	}
}

func TestBoundedDownloadRejectsExactShapeBypassesAtInitialBoundary(t *testing.T) {
	bad := []string{
		"http://replay1.valve.net/570/123_456.dem.bz2?",
		"http://replay1.valve.net/570/123_456.dem.bz2#",
		"http://replay1.valve.net:/570/123_456.dem.bz2",
	}
	for _, rawURL := range bad {
		t.Run(rawURL, func(t *testing.T) {
			requests := 0
			withHTTPClient(t, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				requests++
				return response(http.StatusOK, "", append(zstdMagic, 1)), nil
			})})
			err := boundedDownload(rawURL, filepath.Join(t.TempDir(), "replay"), 1024, "123")
			if err == nil || requests != 0 {
				t.Fatalf("initial URL bypass reached transport: requests=%d err=%v", requests, err)
			}
		})
	}
}

func TestBoundedDownloadRejectsExactShapeBypassesAtRedirectBoundary(t *testing.T) {
	bad := []string{
		"http://replay1.valve.net/570/123_456.dem.bz2?",
		"http://replay1.valve.net/570/123_456.dem.bz2#",
		"http://replay1.valve.net:/570/123_456.dem.bz2",
	}
	for _, location := range bad {
		t.Run(location, func(t *testing.T) {
			requests := 0
			withHTTPClient(t, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				requests++
				if requests == 1 {
					return response(http.StatusFound, location, nil), nil
				}
				return response(http.StatusOK, "", append(zstdMagic, 1)), nil
			})})
			err := boundedDownload("http://replay1.valve.net/570/123_456.dem.bz2", filepath.Join(t.TempDir(), "replay"), 1024, "123")
			if err == nil || requests != 1 {
				t.Fatalf("redirect bypass was fetched: requests=%d err=%v", requests, err)
			}
		})
	}
}

func TestBoundedDownloadRejectsExactShapeBypassesAtFinalBoundary(t *testing.T) {
	bad := []string{
		"http://replay1.valve.net/570/123_456.dem.bz2?",
		"http://replay1.valve.net:/570/123_456.dem.bz2",
	}
	for _, finalURL := range bad {
		t.Run(finalURL, func(t *testing.T) {
			withHTTPClient(t, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				u, err := url.Parse(finalURL)
				if err != nil {
					t.Fatal(err)
				}
				resp := response(http.StatusOK, "", append(zstdMagic, 1))
				resp.Request = &http.Request{URL: u}
				return resp, nil
			})})
			err := boundedDownload("http://replay1.valve.net/570/123_456.dem.bz2", filepath.Join(t.TempDir(), "replay"), 1024, "123")
			if err == nil || !strings.Contains(err.Error(), "final response URL") {
				t.Fatalf("final URL bypass accepted: %v", err)
			}
		})
	}
}

func TestBoundedDownloadAcceptsCanonicalMeasuredShape(t *testing.T) {
	withHTTPClient(t, &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		resp := response(http.StatusOK, "", append(zstdMagic, 1))
		resp.Request = req
		return resp, nil
	})})
	err := boundedDownload(
		"http://replay273.valve.net/570/8941092540_1595018738.dem.bz2",
		filepath.Join(t.TempDir(), "replay.dem.bz2"), 1024, "8941092540",
	)
	if err != nil {
		t.Fatalf("canonical measured URL rejected: %v", err)
	}
}

func TestValidateReplayURLCorrelatesRequestedMatch(t *testing.T) {
	u := "http://replay273.valve.net/570/8941092540_1595018738.dem.bz2"
	if err := validateReplayURL(u, "8941092540"); err != nil {
		t.Fatal(err)
	}
	if err := validateReplayURL(u, "8940891805"); err == nil || !strings.Contains(err.Error(), "requested match") {
		t.Fatalf("mismatched replay identity accepted: %v", err)
	}
}

func TestLooksLikeValveReplayHost(t *testing.T) {
	good := []string{"replay273.valve.net", "replay1.valve.net", "replay999999.valve.net"}
	for _, h := range good {
		if !looksLikeValveReplayHost(h) {
			t.Errorf("looksLikeValveReplayHost(%q) want true", h)
		}
	}
	bad := []string{"replay.valve.net", "replayabc.valve.net", "replay273.evil.net", "evil.valve.net", "replay273.valve.net.evil.com", "replay273valve.net"}
	for _, h := range bad {
		if looksLikeValveReplayHost(h) {
			t.Errorf("looksLikeValveReplayHost(%q) want false", h)
		}
	}
}

func TestValidateMagic(t *testing.T) {
	dir := t.TempDir()
	zstdFile := filepath.Join(dir, "x.bin")
	if err := os.WriteFile(zstdFile, append(append([]byte{}, zstdMagic...), 0xaa, 0xbb), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := validateMagic(zstdFile, zstdMagic); err != nil {
		t.Fatalf("zstd magic mismatch: %v", err)
	}
	demFile := filepath.Join(dir, "x.dem")
	demBytes := append(append([]byte{}, pbDEMS2Magic...), 0x01, 0x02)
	if err := os.WriteFile(demFile, demBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := validateMagic(demFile, pbDEMS2Magic); err != nil {
		t.Fatalf("pbDEMS2 magic mismatch: %v", err)
	}
	// wrong magic on the zstd file (expecting demo magic) must fail
	if err := validateMagic(zstdFile, pbDEMS2Magic); err == nil {
		t.Fatalf("expected magic mismatch on zstd file vs pbDEMS2")
	}
	// missing file must fail
	if err := validateMagic(filepath.Join(dir, "nope"), zstdMagic); err == nil {
		t.Fatalf("expected error for missing file")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func response(status int, location string, body []byte) *http.Response {
	h := make(http.Header)
	if location != "" {
		h.Set("Location", location)
	}
	return &http.Response{
		StatusCode: status,
		Header:     h,
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
}

func withHTTPClient(t *testing.T, client *http.Client) {
	t.Helper()
	old := httpClient
	httpClient = client
	t.Cleanup(func() { httpClient = old })
}

func TestBoundedDownloadRejectsCrossHostRedirect(t *testing.T) {
	requests := 0
	withHTTPClient(t, &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		if requests == 1 {
			return response(http.StatusFound, "https://attacker.example/570/123_456.dem.bz2", nil), nil
		}
		return response(http.StatusOK, "", append(zstdMagic, 1)), nil
	})})

	dest := filepath.Join(t.TempDir(), "replay.dem.bz2")
	err := boundedDownload("https://replay1.valve.net/570/123_456.dem.bz2", dest, 1024, "123")
	if err == nil || !strings.Contains(err.Error(), "allowlist") {
		t.Fatalf("cross-host redirect must be rejected by allowlist, got %v", err)
	}
	if requests != 1 {
		t.Fatalf("redirect destination was fetched: requests=%d", requests)
	}
}

func TestBoundedDownloadCapsAllowedRedirects(t *testing.T) {
	requests := 0
	withHTTPClient(t, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		requests++
		return response(http.StatusFound, fmt.Sprintf("https://replay%d.valve.net/570/123_456.dem.bz2", requests+1), nil), nil
	})})

	dest := filepath.Join(t.TempDir(), "replay.dem.bz2")
	err := boundedDownload("https://replay1.valve.net/570/123_456.dem.bz2", dest, 1024, "123")
	if err == nil || !strings.Contains(err.Error(), "too many redirects") {
		t.Fatalf("allowed redirect chain must be bounded, got %v", err)
	}
	if requests != maxReplayRedirects {
		t.Fatalf("requests=%d want %d", requests, maxReplayRedirects)
	}
}

func TestBoundedDownloadValidatesBeforeReplacingCanonicalFile(t *testing.T) {
	withHTTPClient(t, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(http.StatusOK, "", []byte("not-zstd")), nil
	})})
	dir := t.TempDir()
	dest := filepath.Join(dir, "replay.dem.bz2")
	if err := os.WriteFile(dest, []byte("prior-good"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest+".tmp", []byte("sentinel"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := boundedDownload("https://replay1.valve.net/570/123_456.dem.bz2", dest, 1024, "123")
	if err == nil || !strings.Contains(err.Error(), "magic") {
		t.Fatalf("invalid payload must fail before replacement, got %v", err)
	}
	got, _ := os.ReadFile(dest)
	if string(got) != "prior-good" {
		t.Fatalf("prior canonical artifact changed: %q", got)
	}
	tmp, _ := os.ReadFile(dest + ".tmp")
	if string(tmp) != "sentinel" {
		t.Fatalf("predictable temp file was reused: %q", tmp)
	}
}

func TestBoundedDownloadRejectsCrossSchemeRedirect(t *testing.T) {
	requests := 0
	withHTTPClient(t, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		requests++
		return response(http.StatusFound, "https://replay1.valve.net/570/123_456.dem.bz2", nil), nil
	})})
	err := boundedDownload("http://replay1.valve.net/570/123_456.dem.bz2", filepath.Join(t.TempDir(), "x"), 1024, "123")
	if err == nil || !strings.Contains(err.Error(), "transport policy") || requests != 1 {
		t.Fatalf("cross-scheme redirect accepted: requests=%d err=%v", requests, err)
	}
}

func TestZstdDecompressValidatesBeforeReplacingCanonicalFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "replay.dem.bz2")
	var encoded bytes.Buffer
	zw, err := zstd.NewWriter(&encoded)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := zw.Write([]byte("not-a-demo")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, encoded.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "replay.dem")
	if err := os.WriteFile(dest, []byte("prior-good-demo"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, _, _, err = zstdDecompressBounded(src, dest, 1024)
	if err == nil || !strings.Contains(err.Error(), "magic") {
		t.Fatalf("invalid demo must fail before replacement, got %v", err)
	}
	got, _ := os.ReadFile(dest)
	if string(got) != "prior-good-demo" {
		t.Fatalf("prior canonical demo changed: %q", got)
	}
}

func TestWriteAcquireRecordIsAtomic(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "match.acquire.json")
	if err := os.WriteFile(dest+".tmp", []byte("sentinel"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := acquireRecord{MatchID: "123", Source: "test"}
	if err := writeAcquireRecord(dest, rec); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	var got acquireRecord
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("record is not valid JSON: %v", err)
	}
	if got.MatchID != rec.MatchID {
		t.Fatalf("record mismatch: %+v", got)
	}
	tmp, _ := os.ReadFile(dest + ".tmp")
	if string(tmp) != "sentinel" {
		t.Fatalf("predictable temp file was reused: %q", tmp)
	}
}

func TestAcquisitionAtomicCallersSurfaceUncertaintyAndRetryConverges(t *testing.T) {
	oldOps := acquisitionAtomicOps
	failNext := true
	acquisitionAtomicOps.SyncDir = func(string) error {
		if failNext {
			failNext = false
			return errors.New("injected directory sync")
		}
		return nil
	}
	t.Cleanup(func() { acquisitionAtomicOps = oldOps })

	dir := t.TempDir()
	compressed := filepath.Join(dir, "123.dem.bz2")
	withHTTPClient(t, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(http.StatusOK, "", validZstdDemo(t)), nil
	})})
	err := boundedDownload("http://replay1.valve.net/570/123_456.dem.bz2", compressed, 1024, "123")
	assertUncertainMatch(t, err)
	if err := boundedDownload("http://replay1.valve.net/570/123_456.dem.bz2", compressed, 1024, "123"); err != nil {
		t.Fatalf("compressed retry: %v", err)
	}

	dem := filepath.Join(dir, "123.dem")
	failNext = true
	_, _, _, err = zstdDecompressBounded(compressed, dem, 1024)
	assertUncertainMatch(t, err)
	if _, _, _, err := zstdDecompressBounded(compressed, dem, 1024); err != nil {
		t.Fatalf("decompressed retry: %v", err)
	}

	record := filepath.Join(dir, "123.acquire.json")
	failNext = true
	err = writeAcquireRecord(record, acquireRecord{MatchID: "123"})
	assertUncertainMatch(t, err)
	if err := writeAcquireRecord(record, acquireRecord{MatchID: "123"}); err != nil {
		t.Fatalf("record retry: %v", err)
	}
}

func assertUncertainMatch(t *testing.T, err error) {
	t.Helper()
	var uncertain *atomicfile.CommitOutcomeUncertainError
	if !errors.As(err, &uncertain) || !uncertain.CanonicalMatchesExpected {
		t.Fatalf("got %v, want reconciled uncertain commit", err)
	}
}

func validZstdDemo(t *testing.T) []byte {
	t.Helper()
	var encoded bytes.Buffer
	zw, err := zstd.NewWriter(&encoded)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := zw.Write(append(append([]byte{}, pbDEMS2Magic...), []byte("payload")...)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

func TestAcquireRecordTransportProvenance(t *testing.T) {
	httpRecord := newAcquireRecord("123", matchMeta{ReplayURL: "http://replay1.valve.net/570/123_456.dem.bz2"})
	if httpRecord.TransportScheme != "http" || !httpRecord.UnauthenticatedTransport || httpRecord.SourceQuality != "untrusted_pending_identity_correlation" {
		t.Fatalf("HTTP provenance not explicit: %+v", httpRecord)
	}
	httpsRecord := newAcquireRecord("123", matchMeta{ReplayURL: "https://replay1.valve.net/570/123_456.dem.bz2"})
	if httpsRecord.TransportScheme != "https" || httpsRecord.UnauthenticatedTransport {
		t.Fatalf("HTTPS provenance incorrect: %+v", httpsRecord)
	}
}

func TestBatchCLIRejectsMalformedManifestsWithoutPanic(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "empty", body: `{ "entries": [] }`, want: "manifest is empty"},
		{name: "duplicate id", body: `{ "entries": [{"match_id":"x","dem_path":"a"},{"match_id":"x","dem_path":"b"}] }`, want: "duplicate match_id"},
		{name: "missing path", body: `{ "entries": [{"match_id":"x"}] }`, want: "empty dem_path"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, code := runBatchCLIProcess(t, tt.body, false)
			if code == 0 {
				t.Fatalf("malformed manifest exited zero: %s", out)
			}
			if strings.Contains(out, "panic:") || !strings.Contains(out, tt.want) {
				t.Fatalf("unexpected CLI output: %s", out)
			}
		})
	}
}

func TestBatchCLIRetryAllReturnsNonzeroForTerminalEntry(t *testing.T) {
	body := fmt.Sprintf(`{"entries":[{"match_id":"missing","dem_path":%q}]}`, filepath.Join(t.TempDir(), "missing.dem"))
	out, code := runBatchCLIProcess(t, body, true)
	if code == 0 || !strings.Contains(out, "failed=1") || !strings.Contains(out, "terminal failures") {
		t.Fatalf("retry-all terminal result code=%d output=%s", code, out)
	}
}

func runBatchCLIProcess(t *testing.T, manifest string, retryAll bool) (string, int) {
	t.Helper()
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(manifestPath, []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	args := []string{"-test.run=TestReplaySpikeCLIHelper", "--", manifestPath, dir}
	if retryAll {
		args = append(args, "--retry-all")
	}
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), "REPLAY_SPIKE_CLI_HELPER=1")
	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), 0
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("run helper: %v", err)
	}
	return string(out), exitErr.ExitCode()
}

func TestReplaySpikeCLIHelper(t *testing.T) {
	if os.Getenv("REPLAY_SPIKE_CLI_HELPER") != "1" {
		return
	}
	args := os.Args
	sep := -1
	for i, arg := range args {
		if arg == "--" {
			sep = i
			break
		}
	}
	if sep < 0 || len(args) < sep+3 {
		fmt.Fprintln(os.Stderr, "bad helper arguments")
		os.Exit(2)
	}
	batchArgs := []string{args[sep+1], "--data-dir", args[sep+2], "--max-retries", "1"}
	batchArgs = append(batchArgs, args[sep+3:]...)
	cmdBatch(batchArgs)
}
