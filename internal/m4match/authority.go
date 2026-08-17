package m4match

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	AuthorityRootSchemaVersion            = "match_authority_root.v1"
	AuthorityEvidenceSchemaVersion        = "match_authority_evidence.v1"
	AuthorityPreflightSchemaVersion       = "match_authority_preflight.v1"
	AuthorityRetrievalLimitVersion        = "match_authority_retrieval_limits.v1"
	AuthorityExtractionVersion            = "valve_json_pointer_or_exact_span.v2"
	maxAuthorityLogicalPages              = 12
	maxAuthorityPagesPerEndpoint          = 8
	maxAuthorityTransactions              = 48
	maxAuthorityRedirects                 = 3
	maxAuthorityResponseBytes       int64 = 2 << 20
	maxAuthoritySetBytes            int64 = 8 << 20
)

// This is canonical JSON, including its terminating newline. The root is deliberately
// compiled and has no path, environment, configuration, or caller replacement seam.
//
//go:embed authority_root_v1.json
var embeddedAuthorityRootJSON string

//go:embed authority_root_sources_v1.json
var embeddedAuthorityRootSourcesJSON string

const (
	EmbeddedAuthorityRootSHA256        = "e7d87e07baeabc2d9e0504363c952e6eb605347b9d381322de161c53ea5389d0"
	EmbeddedAuthorityRootSourcesSHA256 = "d9f02598213890e40a82c0fc1b6c31262b3dc17a27a3bd43e3d2742509c4cd07"
)

type AuthorityEndpointV1 struct {
	ID      string `json:"id"`
	Origin  string `json:"origin"`
	Path    string `json:"path"`
	Purpose string `json:"purpose"`
}

type AuthorityEstablishingRecordV1 struct {
	Origin string `json:"origin"`
	URL    string `json:"url"`
	Fact   string `json:"fact"`
}

type MatchAuthorityRootV1 struct {
	SchemaVersion            string                          `json:"schema_version"`
	EventID                  string                          `json:"event_id"`
	EventName                string                          `json:"event_name"`
	LeagueIDState            string                          `json:"league_id_state"`
	TrustedOrigins           []string                        `json:"trusted_origins"`
	EndpointTemplates        []AuthorityEndpointV1           `json:"endpoint_templates"`
	ExtractionProfileVersion string                          `json:"extraction_profile_version"`
	EstablishingRecords      []AuthorityEstablishingRecordV1 `json:"establishing_records"`
}

func EmbeddedAuthorityRoot() (MatchAuthorityRootV1, error) {
	payload := []byte(embeddedAuthorityRootJSON)
	if payloadSHA(payload) != EmbeddedAuthorityRootSHA256 {
		return MatchAuthorityRootV1{}, errors.New("compiled authority root digest mismatch")
	}
	var root MatchAuthorityRootV1
	if err := json.Unmarshal(payload, &root); err != nil {
		return MatchAuthorityRootV1{}, err
	}
	canonicalRoot, err := canonical(root)
	if err != nil || !bytes.Equal(canonicalRoot, payload) {
		return MatchAuthorityRootV1{}, errors.New("compiled authority root is not canonical")
	}
	if err := root.Validate(); err != nil {
		return MatchAuthorityRootV1{}, err
	}
	if payloadSHA([]byte(embeddedAuthorityRootSourcesJSON)) != EmbeddedAuthorityRootSourcesSHA256 || !json.Valid([]byte(embeddedAuthorityRootSourcesJSON)) {
		return MatchAuthorityRootV1{}, errors.New("compiled authority root source evidence mismatch")
	}
	return root, nil
}

func (root MatchAuthorityRootV1) Validate() error {
	if root.SchemaVersion != AuthorityRootSchemaVersion || root.EventID == "" || root.EventName == "" || root.ExtractionProfileVersion != AuthorityExtractionVersion {
		return errors.New("authority root identity is incomplete")
	}
	if len(root.TrustedOrigins) == 0 || len(root.EndpointTemplates) == 0 || len(root.EstablishingRecords) == 0 {
		return errors.New("authority root trust boundary is empty")
	}
	origins := map[string]bool{}
	for _, raw := range root.TrustedOrigins {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" || origins[raw] {
			return errors.New("authority root contains an unsafe or duplicate origin")
		}
		origins[raw] = true
	}
	ids := map[string]bool{}
	for _, endpoint := range root.EndpointTemplates {
		if endpoint.ID == "" || ids[endpoint.ID] || !origins[endpoint.Origin] || endpoint.Path == "" || !strings.HasPrefix(endpoint.Path, "/") || strings.ContainsAny(endpoint.Path, "?#") {
			return errors.New("authority root endpoint is invalid or untrusted")
		}
		ids[endpoint.ID] = true
	}
	for _, record := range root.EstablishingRecords {
		parsed, err := url.Parse(record.URL)
		origin := ""
		if err == nil {
			origin = parsed.Scheme + "://" + parsed.Host
		}
		if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Fragment != "" || !origins[record.Origin] || origin != record.Origin || record.Fact == "" {
			return errors.New("authority establishing record is invalid")
		}
	}
	if root.EventID != "dpc_stockholm_major_2022" || root.EventName != "The Stockholm Major" || root.LeagueIDState != "pinned:14173" {
		return errors.New("authority root event or Valve league identity mismatch")
	}
	return nil
}

type MatchAuthoritySelectionV1 struct {
	MatchID            string `json:"match_id"`
	LeagueID           string `json:"league_id"`
	LeagueOrEvent      string `json:"league_or_event"`
	Series             string `json:"series"`
	Game               string `json:"game"`
	RadiantTeam        string `json:"radiant_team"`
	DireTeam           string `json:"dire_team"`
	ExpectedStartBegin string `json:"expected_start_begin"`
	ExpectedStartEnd   string `json:"expected_start_end"`
}

func (selection MatchAuthoritySelectionV1) Validate() error {
	if selection.MatchID == "" || selection.MatchID == "0" || selection.LeagueID == "" || selection.LeagueID == "0" || selection.LeagueOrEvent == "" || selection.Series == "" || selection.Game == "" || selection.RadiantTeam == "" || selection.DireTeam == "" || selection.RadiantTeam == selection.DireTeam {
		return errors.New("selected tournament match identity is incomplete or contradictory")
	}
	begin, beginErr := time.Parse(time.RFC3339, selection.ExpectedStartBegin)
	end, endErr := time.Parse(time.RFC3339, selection.ExpectedStartEnd)
	if beginErr != nil || endErr != nil || !end.After(begin) || end.Sub(begin) > MaxArmingWindow {
		return errors.New("expected start window must be canonical and at most 30 minutes")
	}
	return nil
}

func (selection MatchAuthoritySelectionV1) validateAgainstRoot(root MatchAuthorityRootV1) error {
	if selection.LeagueID != strings.TrimPrefix(root.LeagueIDState, "pinned:") || selection.LeagueOrEvent != root.EventName {
		return errors.New("selected match does not belong to the embedded Valve event/league identity")
	}
	return nil
}

type AuthorityFactBindingV1 struct {
	Fact                     string `json:"fact"`
	NormalizedValue          string `json:"normalized_value"`
	ArtifactSHA256           string `json:"artifact_sha256"`
	ExtractionProfileVersion string `json:"extraction_profile_version"`
	JSONPointer              string `json:"json_pointer,omitempty"`
	ByteStart                int64  `json:"byte_start,omitempty"`
	ByteEnd                  int64  `json:"byte_end,omitempty"`
	BoundSpanSHA256          string `json:"bound_span_sha256"`
}

type AuthorityArtifactV1 struct {
	EndpointID         string   `json:"endpoint_id"`
	LogicalPageKey     string   `json:"logical_page_key"`
	Cursor             string   `json:"cursor"`
	PrivatePath        string   `json:"private_path"`
	RawSHA256          string   `json:"raw_sha256"`
	SanitizedPath      string   `json:"sanitized_path"`
	SanitizedSHA256    string   `json:"sanitized_sha256"`
	FinalURL           string   `json:"final_sanitized_url"`
	Status             int      `json:"status"`
	MediaType          string   `json:"media_type"`
	ContentEncoding    string   `json:"content_encoding"`
	WireBytes          int64    `json:"wire_bytes"`
	DecodedBytes       int64    `json:"decoded_bytes"`
	RedirectChain      []string `json:"redirect_chain"`
	TransactionCount   int      `json:"transaction_count"`
	RetryCount         int      `json:"retry_count"`
	PaginationTerminal bool     `json:"pagination_terminal"`
	RetrievalError     string   `json:"retrieval_error"`
}

type MatchAuthorityEvidenceV1 struct {
	SchemaVersion       string                    `json:"schema_version"`
	AuthorityRootSHA256 string                    `json:"authority_root_sha256"`
	Selection           MatchAuthoritySelectionV1 `json:"selection"`
	Artifacts           []AuthorityArtifactV1     `json:"artifacts"`
	FactBindings        []AuthorityFactBindingV1  `json:"fact_bindings"`
	ArtifactSetSHA256   string                    `json:"artifact_set_sha256"`
	RetrievalStartedAt  string                    `json:"retrieval_started_at"`
	RetrievalEndedAt    string                    `json:"retrieval_ended_at"`
	LogicalPages        int                       `json:"logical_pages"`
	Transactions        int                       `json:"transactions"`
	WireBytes           int64                     `json:"wire_bytes"`
	DecodedBytes        int64                     `json:"decoded_bytes"`
	RetryCount          int                       `json:"retry_count"`
}

type MatchAuthorityPreflightV1 struct {
	SchemaVersion           string `json:"schema_version"`
	CandidateCommit         string `json:"candidate_commit"`
	BinarySHA256            string `json:"binary_sha256"`
	AcceptedAmendmentCommit string `json:"accepted_amendment_commit"`
	AuthorityRootSHA256     string `json:"authority_root_sha256"`
	MatchID                 string `json:"match_id"`
	ArtifactSetSHA256       string `json:"artifact_set_sha256"`
	AuthorityEvidenceSHA256 string `json:"authority_evidence_sha256"`
	EvidenceIndexSHA256     string `json:"evidence_index_sha256"`
	RetrievalLimitVersion   string `json:"retrieval_limit_version"`
	TerminalLogicalPages    int    `json:"terminal_logical_pages"`
	TerminalTransactions    int    `json:"terminal_transactions"`
	TerminalWireBytes       int64  `json:"terminal_wire_bytes"`
	TerminalDecodedBytes    int64  `json:"terminal_decoded_bytes"`
	RetryCount              int    `json:"retry_count"`
}

type authorityFetcher struct {
	client    *http.Client
	now       func() time.Time
	webAPIKey string
}

func productionAuthorityFetcher(secret io.Reader) (authorityFetcher, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DisableCompression = true
	keyPayload, err := io.ReadAll(io.LimitReader(secret, 257))
	if err != nil || len(keyPayload) == 0 || len(keyPayload) > 256 {
		return authorityFetcher{}, errors.New("bounded external Web API secret channel is required")
	}
	key := strings.TrimSpace(string(keyPayload))
	if key == "" || strings.ContainsAny(key, "\r\n") {
		return authorityFetcher{}, errors.New("Web API secret channel is invalid")
	}
	return authorityFetcher{client: &http.Client{Transport: transport}, now: time.Now, webAPIKey: key}, nil
}

type fetchedAuthorityPage struct {
	artifact AuthorityArtifactV1
	decoded  []byte
}

func (fetcher authorityFetcher) fetch(ctx context.Context, root MatchAuthorityRootV1, endpoint AuthorityEndpointV1) (fetchedAuthorityPage, error) {
	pageCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	allowedOrigins := map[string]bool{}
	for _, origin := range root.TrustedOrigins {
		allowedOrigins[origin] = true
	}
	redirects := []string{}
	transactions := 0
	client := *fetcher.client
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		transactions++
		if len(via) > maxAuthorityRedirects {
			return errors.New("authority redirect limit exceeded")
		}
		origin := request.URL.Scheme + "://" + request.URL.Host
		if !allowedOrigins[origin] || request.URL.RawQuery != "" || request.URL.Path != endpoint.Path {
			return errors.New("authority redirect escaped embedded endpoint")
		}
		redirects = append(redirects, request.URL.String())
		return nil
	}
	requestCtx, requestCancel := context.WithTimeout(pageCtx, 5*time.Second)
	defer requestCancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, endpoint.Origin+endpoint.Path, nil)
	if err != nil {
		return fetchedAuthorityPage{}, err
	}
	request.Header.Set("Accept-Encoding", "gzip, identity")
	if endpoint.ID == "valve_live_league_games" {
		request.Header.Set("X-WebAPI-Key", fetcher.webAPIKey)
	}
	transactions++
	response, err := client.Do(request)
	if err != nil {
		return fetchedAuthorityPage{}, err
	}
	defer response.Body.Close()
	if transactions > maxAuthorityTransactions || response.StatusCode < 200 || response.StatusCode > 299 {
		return fetchedAuthorityPage{}, errors.New("authority HTTP transaction failed closed")
	}
	encoding := strings.ToLower(strings.TrimSpace(response.Header.Get("Content-Encoding")))
	if encoding != "" && encoding != "identity" && encoding != "gzip" {
		return fetchedAuthorityPage{}, errors.New("unsupported authority content encoding")
	}
	wire, err := io.ReadAll(io.LimitReader(response.Body, maxAuthorityResponseBytes+1))
	if err != nil || int64(len(wire)) > maxAuthorityResponseBytes {
		return fetchedAuthorityPage{}, errors.New("authority wire byte limit exceeded")
	}
	decoded := wire
	if encoding == "gzip" {
		reader, gzipErr := gzip.NewReader(bytes.NewReader(wire))
		if gzipErr != nil {
			return fetchedAuthorityPage{}, errors.New("authority gzip header is invalid")
		}
		reader.Multistream(false)
		decoded, err = io.ReadAll(io.LimitReader(reader, maxAuthorityResponseBytes+1))
		closeErr := reader.Close()
		if err != nil || closeErr != nil || int64(len(decoded)) > maxAuthorityResponseBytes {
			return fetchedAuthorityPage{}, errors.New("authority decoded byte limit exceeded")
		}
		// Recompress/decode ambiguity is rejected by requiring one complete member and no extra bytes.
		if !validSingleGzipMember(wire) {
			return fetchedAuthorityPage{}, errors.New("authority gzip is truncated or has trailing members")
		}
	}
	finalURL := response.Request.URL.Scheme + "://" + response.Request.URL.Host + response.Request.URL.EscapedPath()
	artifact := AuthorityArtifactV1{EndpointID: endpoint.ID, LogicalPageKey: endpoint.ID + ":0", Cursor: "terminal", RawSHA256: payloadSHA(decoded), FinalURL: finalURL, Status: response.StatusCode, MediaType: response.Header.Get("Content-Type"), ContentEncoding: encoding, WireBytes: int64(len(wire)), DecodedBytes: int64(len(decoded)), RedirectChain: redirects, TransactionCount: transactions, RetryCount: 0, PaginationTerminal: true}
	return fetchedAuthorityPage{artifact: artifact, decoded: decoded}, nil
}

func validSingleGzipMember(payload []byte) bool {
	reader := bytes.NewReader(payload)
	gz, err := gzip.NewReader(reader)
	if err != nil {
		return false
	}
	gz.Multistream(false)
	if _, err = io.Copy(io.Discard, gz); err != nil {
		_ = gz.Close()
		return false
	}
	if err = gz.Close(); err != nil {
		return false
	}
	return reader.Len() == 0
}

func authorityArtifactSetSHA(artifacts []AuthorityArtifactV1) (string, error) {
	copyArtifacts := append([]AuthorityArtifactV1(nil), artifacts...)
	sort.Slice(copyArtifacts, func(i, j int) bool { return copyArtifacts[i].EndpointID < copyArtifacts[j].EndpointID })
	payload, err := canonical(copyArtifacts)
	if err != nil {
		return "", err
	}
	return payloadSHA(payload), nil
}

func shaBytes(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func authorityPrivatePath(index int) string {
	return filepath.ToSlash(filepath.Join("evidence", "private", "authority", fmt.Sprintf("page-%02d.bin", index)))
}

type AuthorityPreflightConfig struct {
	DataRoot      string
	ReadinessRoot string
	RepoRoot      string
	SelectionPath string
	Credential    io.Reader
}

func PreflightMatchAuthority(ctx context.Context, config AuthorityPreflightConfig) (MatchAuthorityPreflightV1, error) {
	readiness, err := VerifyClassification(ctx, config.ReadinessRoot, config.RepoRoot, "preflight", RunClassificationV1{Purpose: PurposeP4Acceptance, Class: MatchClassPublicTournament})
	if err != nil || !readiness.Ready {
		return MatchAuthorityPreflightV1{}, errors.New("exact public-tournament readiness is not ready")
	}
	selectionPayload, err := os.ReadFile(filepath.Clean(config.SelectionPath))
	if err != nil || len(selectionPayload) > 16<<10 {
		return MatchAuthorityPreflightV1{}, errors.New("authority selection cannot be read within bounds")
	}
	var selection MatchAuthoritySelectionV1
	decoder := json.NewDecoder(bytes.NewReader(selectionPayload))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&selection) != nil || decoder.Decode(&struct{}{}) != io.EOF || selection.Validate() != nil {
		return MatchAuthorityPreflightV1{}, errors.New("authority selection is noncanonical or invalid")
	}
	canonicalSelection, _ := canonical(selection)
	if !bytes.Equal(canonicalSelection, selectionPayload) {
		return MatchAuthorityPreflightV1{}, errors.New("authority selection must be canonical JSON")
	}
	rootDefinition, err := EmbeddedAuthorityRoot()
	if err != nil {
		return MatchAuthorityPreflightV1{}, err
	}
	if err := selection.validateAgainstRoot(rootDefinition); err != nil {
		return MatchAuthorityPreflightV1{}, err
	}
	fetcher, err := productionAuthorityFetcher(config.Credential)
	if err != nil {
		return MatchAuthorityPreflightV1{}, err
	}
	lease, err := acquireFreshRoot(config.DataRoot, config.RepoRoot)
	if err != nil {
		return MatchAuthorityPreflightV1{}, err
	}
	defer lease.Close()
	retrievalCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	evidence := MatchAuthorityEvidenceV1{SchemaVersion: AuthorityEvidenceSchemaVersion, AuthorityRootSHA256: EmbeddedAuthorityRootSHA256, Selection: selection, RetrievalStartedAt: fetcher.now().UTC().Format(time.RFC3339Nano), RetryCount: 0}
	pages := make([]fetchedAuthorityPage, 0, len(rootDefinition.EndpointTemplates))
	seenRequests, seenPages := map[string]bool{}, map[string]bool{}
	for index, endpoint := range rootDefinition.EndpointTemplates {
		if index >= maxAuthorityLogicalPages || seenRequests[endpoint.ID] {
			return MatchAuthorityPreflightV1{}, errors.New("authority request/page bound or cycle violated")
		}
		seenRequests[endpoint.ID] = true
		page, fetchErr := fetcher.fetch(retrievalCtx, rootDefinition, endpoint)
		if fetchErr != nil {
			return MatchAuthorityPreflightV1{}, fetchErr
		}
		if seenPages[page.artifact.RawSHA256] {
			return MatchAuthorityPreflightV1{}, errors.New("duplicate authority page digest")
		}
		seenPages[page.artifact.RawSHA256] = true
		evidence.LogicalPages++
		evidence.Transactions += page.artifact.TransactionCount
		evidence.WireBytes += page.artifact.WireBytes
		evidence.DecodedBytes += page.artifact.DecodedBytes
		if evidence.Transactions > maxAuthorityTransactions || evidence.WireBytes > maxAuthoritySetBytes || evidence.DecodedBytes > maxAuthoritySetBytes {
			return MatchAuthorityPreflightV1{}, errors.New("authority artifact-set limits exceeded")
		}
		page.artifact.PrivatePath = authorityPrivatePath(index)
		if err := writePrivate(filepath.Join(lease.abs, filepath.FromSlash(page.artifact.PrivatePath)), page.decoded); err != nil {
			return MatchAuthorityPreflightV1{}, err
		}
		sanitizedPayload, _ := canonical(struct {
			SchemaVersion, EndpointID, RawSHA256 string
			Bytes                                int64
		}{"authority_sanitized_export.v1", endpoint.ID, page.artifact.RawSHA256, int64(len(page.decoded))})
		page.artifact.SanitizedPath = filepath.ToSlash(filepath.Join("evidence", "sanitized", "authority", fmt.Sprintf("page-%02d.json", index)))
		page.artifact.SanitizedSHA256 = payloadSHA(sanitizedPayload)
		if err := writePrivate(filepath.Join(lease.abs, filepath.FromSlash(page.artifact.SanitizedPath)), sanitizedPayload); err != nil {
			return MatchAuthorityPreflightV1{}, err
		}
		pages = append(pages, page)
		evidence.Artifacts = append(evidence.Artifacts, page.artifact)
	}
	evidence.RetrievalEndedAt = fetcher.now().UTC().Format(time.RFC3339Nano)
	facts := []struct{ name, value string }{{"match_id", selection.MatchID}, {"league_id", selection.LeagueID}, {"league_or_event", selection.LeagueOrEvent}, {"series", selection.Series}, {"game", selection.Game}, {"radiant_team", selection.RadiantTeam}, {"dire_team", selection.DireTeam}, {"expected_start_begin", selection.ExpectedStartBegin}, {"expected_start_end", selection.ExpectedStartEnd}}
	for _, fact := range facts {
		binding, bindErr := bindUniqueAuthorityFact(fact.name, fact.value, pages)
		if bindErr != nil {
			return MatchAuthorityPreflightV1{}, bindErr
		}
		evidence.FactBindings = append(evidence.FactBindings, binding)
	}
	evidence.ArtifactSetSHA256, err = authorityArtifactSetSHA(evidence.Artifacts)
	if err != nil {
		return MatchAuthorityPreflightV1{}, err
	}
	evidencePayload, _ := canonical(evidence)
	evidenceSHA := payloadSHA(evidencePayload)
	if err := writePrivate(filepath.Join(lease.abs, "evidence/canonical/match-authority-evidence.json"), evidencePayload); err != nil {
		return MatchAuthorityPreflightV1{}, err
	}
	preflightIndex, err := rootReadFile(filepath.Join(config.ReadinessRoot, "evidence/canonical/evidence-index.json"))
	if err != nil {
		return MatchAuthorityPreflightV1{}, err
	}
	var harness Evidence
	if json.Unmarshal(preflightIndex, &harness) != nil {
		return MatchAuthorityPreflightV1{}, errors.New("readiness evidence index is invalid")
	}
	result := MatchAuthorityPreflightV1{SchemaVersion: AuthorityPreflightSchemaVersion, CandidateCommit: readiness.CandidateCommit, BinarySHA256: harness.CandidateIdentity.BinarySHA256, AcceptedAmendmentCommit: AcceptedP4Spec, AuthorityRootSHA256: EmbeddedAuthorityRootSHA256, MatchID: selection.MatchID, ArtifactSetSHA256: evidence.ArtifactSetSHA256, AuthorityEvidenceSHA256: evidenceSHA, EvidenceIndexSHA256: readiness.EvidenceIndexSHA256, RetrievalLimitVersion: AuthorityRetrievalLimitVersion, TerminalLogicalPages: evidence.LogicalPages, TerminalTransactions: evidence.Transactions, TerminalWireBytes: evidence.WireBytes, TerminalDecodedBytes: evidence.DecodedBytes, RetryCount: 0}
	if err := writeJSON(filepath.Join(lease.abs, "evidence/canonical/match-authority-preflight.json"), result, 0o600); err != nil {
		return MatchAuthorityPreflightV1{}, err
	}
	return result, nil
}

type authorityJSONLeaf struct {
	pointer string
	value   string
	span    []byte
}

func bindUniqueAuthorityFact(name, value string, pages []fetchedAuthorityPage) (AuthorityFactBindingV1, error) {
	var found *AuthorityFactBindingV1
	for _, page := range pages {
		bindings, err := locateAuthorityFact(name, value, page.artifact, page.decoded)
		if err != nil {
			return AuthorityFactBindingV1{}, err
		}
		for _, binding := range bindings {
			if found != nil {
				return AuthorityFactBindingV1{}, fmt.Errorf("authority fact %s is ambiguous", name)
			}
			candidate := binding
			found = &candidate
		}
	}
	if found == nil {
		return AuthorityFactBindingV1{}, fmt.Errorf("authority fact %s is missing", name)
	}
	return *found, nil
}

func locateAuthorityFact(name, value string, artifact AuthorityArtifactV1, payload []byte) ([]AuthorityFactBindingV1, error) {
	var decoded any
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if decoder.Decode(&decoded) == nil && decoder.Decode(&struct{}{}) == io.EOF {
		leaves := make([]authorityJSONLeaf, 0)
		collectAuthorityJSONLeaves(decoded, "", &leaves)
		var result []AuthorityFactBindingV1
		for _, leaf := range leaves {
			if leaf.value != value || !authorityPointerAllowed(name, leaf.pointer, artifact.EndpointID) {
				continue
			}
			result = append(result, AuthorityFactBindingV1{Fact: name, NormalizedValue: value, ArtifactSHA256: artifact.RawSHA256, ExtractionProfileVersion: AuthorityExtractionVersion, JSONPointer: leaf.pointer, BoundSpanSHA256: payloadSHA(leaf.span)})
		}
		return result, nil
	}
	if artifact.EndpointID != "valve_dota2_event_record" {
		return nil, nil
	}
	if fact := name; fact != "league_or_event" && fact != "series" && fact != "expected_start_begin" && fact != "expected_start_end" {
		return nil, nil
	}
	needle := []byte(value)
	var result []AuthorityFactBindingV1
	for offset := 0; offset <= len(payload)-len(needle); {
		index := bytes.Index(payload[offset:], needle)
		if index < 0 {
			break
		}
		start := offset + index
		end := start + len(needle)
		if authorityTokenBoundary(payload, start, end) {
			span := payload[start:end]
			result = append(result, AuthorityFactBindingV1{Fact: name, NormalizedValue: value, ArtifactSHA256: artifact.RawSHA256, ExtractionProfileVersion: AuthorityExtractionVersion, ByteStart: int64(start), ByteEnd: int64(end), BoundSpanSHA256: payloadSHA(span)})
		}
		offset = end
	}
	return result, nil
}

func collectAuthorityJSONLeaves(value any, pointer string, leaves *[]authorityJSONLeaf) {
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			escaped := strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
			collectAuthorityJSONLeaves(typed[key], pointer+"/"+escaped, leaves)
		}
	case []any:
		for index, item := range typed {
			collectAuthorityJSONLeaves(item, pointer+"/"+strconv.Itoa(index), leaves)
		}
	case string:
		span, _ := canonical(typed)
		*leaves = append(*leaves, authorityJSONLeaf{pointer: pointer, value: typed, span: bytes.TrimSuffix(span, []byte("\n"))})
	case json.Number:
		*leaves = append(*leaves, authorityJSONLeaf{pointer: pointer, value: typed.String(), span: []byte(typed.String())})
	}
}

func authorityPointerAllowed(fact, pointer, endpoint string) bool {
	if endpoint != "valve_live_league_games" && endpoint != "valve_dota2_event_record" {
		return false
	}
	last := pointer
	if index := strings.LastIndex(pointer, "/"); index >= 0 {
		last = pointer[index+1:]
	}
	allowed := map[string][]string{
		"match_id": {"match_id"}, "league_id": {"league_id"}, "league_or_event": {"league_name", "event_name", "name"},
		"series": {"series", "series_name", "series_id"}, "game": {"game", "game_number"},
		"radiant_team": {"radiant_team", "radiant_team_name", "team_name_radiant"}, "dire_team": {"dire_team", "dire_team_name", "team_name_dire"},
		"expected_start_begin": {"expected_start_begin", "start_time", "scheduled_start_time"}, "expected_start_end": {"expected_start_end", "end_time", "scheduled_end_time"},
	}
	for _, candidate := range allowed[fact] {
		if last == candidate {
			if (fact == "match_id" || fact == "league_id" || fact == "radiant_team" || fact == "dire_team") && endpoint != "valve_live_league_games" {
				return false
			}
			return true
		}
	}
	return false
}

func authorityTokenBoundary(payload []byte, start, end int) bool {
	isToken := func(value byte) bool {
		return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9' || value == '_' || value == '-'
	}
	return (start == 0 || !isToken(payload[start-1])) && (end == len(payload) || !isToken(payload[end]))
}

func strictCanonicalJSON(payload []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("strict canonical JSON decode failed")
	}
	canonicalPayload, err := canonical(destination)
	if err != nil || !bytes.Equal(canonicalPayload, payload) {
		return errors.New("JSON is not canonical")
	}
	return nil
}

func verifyAuthorityEvidenceRoot(rootPath, repo string, readiness Readiness, harness Evidence, matchID string) (string, error) {
	if rootPath == "" {
		return "", errors.New("public tournament live requires the descriptor-confined authority evidence root")
	}
	lease, err := acquireExistingRoot(rootPath, repo)
	if err != nil {
		return "", err
	}
	defer lease.Close()
	return verifyAuthorityEvidenceAt(lease.abs, readiness, harness, matchID)
}

func verifyAuthorityEvidenceAt(root string, readiness Readiness, harness Evidence, matchID string) (string, error) {
	preflightPayload, err := rootReadFile(filepath.Join(root, "evidence/canonical/match-authority-preflight.json"))
	if err != nil || len(preflightPayload) > 64<<10 {
		return "", errors.New("authority preflight cannot be read within bounds")
	}
	var preflight MatchAuthorityPreflightV1
	if err := strictCanonicalJSON(preflightPayload, &preflight); err != nil {
		return "", fmt.Errorf("authority preflight: %w", err)
	}
	evidencePayload, err := rootReadFile(filepath.Join(root, "evidence/canonical/match-authority-evidence.json"))
	if err != nil || int64(len(evidencePayload)) > maxAuthoritySetBytes {
		return "", errors.New("authority evidence cannot be read within bounds")
	}
	var evidence MatchAuthorityEvidenceV1
	if err := strictCanonicalJSON(evidencePayload, &evidence); err != nil {
		return "", fmt.Errorf("authority evidence: %w", err)
	}
	rootDefinition, err := EmbeddedAuthorityRoot()
	if err != nil || evidence.Selection.Validate() != nil || evidence.Selection.validateAgainstRoot(rootDefinition) != nil {
		return "", errors.New("authority embedded root or selected match identity mismatch")
	}
	if preflight.SchemaVersion != AuthorityPreflightSchemaVersion || preflight.CandidateCommit != readiness.CandidateCommit || preflight.BinarySHA256 != harness.CandidateIdentity.BinarySHA256 || preflight.AcceptedAmendmentCommit != AcceptedP4Spec || preflight.AuthorityRootSHA256 != EmbeddedAuthorityRootSHA256 || preflight.MatchID != matchID || evidence.Selection.MatchID != matchID || preflight.EvidenceIndexSHA256 != readiness.EvidenceIndexSHA256 || preflight.RetrievalLimitVersion != AuthorityRetrievalLimitVersion || preflight.RetryCount != 0 || preflight.AuthorityEvidenceSHA256 != payloadSHA(evidencePayload) || evidence.SchemaVersion != AuthorityEvidenceSchemaVersion || evidence.AuthorityRootSHA256 != EmbeddedAuthorityRootSHA256 || evidence.RetryCount != 0 {
		return "", errors.New("authority preflight/evidence identity continuity mismatch")
	}
	artifactSetSHA, err := authorityArtifactSetSHA(evidence.Artifacts)
	if err != nil || artifactSetSHA != evidence.ArtifactSetSHA256 || preflight.ArtifactSetSHA256 != artifactSetSHA {
		return "", errors.New("authority artifact-set identity mismatch")
	}
	endpointByID := map[string]AuthorityEndpointV1{}
	for _, endpoint := range rootDefinition.EndpointTemplates {
		endpointByID[endpoint.ID] = endpoint
	}
	artifactsBySHA := map[string]fetchedAuthorityPage{}
	endpointPages := map[string]int{}
	var logicalPages, transactions int
	var wireBytes, decodedBytes int64
	for index, artifact := range evidence.Artifacts {
		endpoint, ok := endpointByID[artifact.EndpointID]
		endpointPages[artifact.EndpointID]++
		if !ok || endpointPages[artifact.EndpointID] > maxAuthorityPagesPerEndpoint || artifact.LogicalPageKey != artifact.EndpointID+":0" || artifact.Cursor != "terminal" || !artifact.PaginationTerminal || artifact.RetryCount != 0 || artifact.RetrievalError != "" || artifact.Status < 200 || artifact.Status > 299 || artifact.TransactionCount != 1+len(artifact.RedirectChain) || artifact.TransactionCount > maxAuthorityRedirects+1 || artifact.WireBytes < 0 || artifact.WireBytes > maxAuthorityResponseBytes || artifact.DecodedBytes < 0 || artifact.DecodedBytes > maxAuthorityResponseBytes || len(artifact.RedirectChain) > maxAuthorityRedirects || artifact.ContentEncoding != "" && artifact.ContentEncoding != "identity" && artifact.ContentEncoding != "gzip" || artifact.ContentEncoding != "gzip" && artifact.WireBytes != artifact.DecodedBytes {
			return "", errors.New("authority artifact terminal counters or endpoint identity invalid")
		}
		for _, redirect := range artifact.RedirectChain {
			parsed, parseErr := url.Parse(redirect)
			if parseErr != nil || parsed.Scheme+"://"+parsed.Host != endpoint.Origin || parsed.Path != endpoint.Path || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
				return "", errors.New("authority redirect chain escaped the embedded endpoint")
			}
		}
		expectedURL := endpoint.Origin + endpoint.Path
		if artifact.FinalURL != expectedURL || artifact.PrivatePath != authorityPrivatePath(index) || artifact.SanitizedPath == "" || !validSHA256(artifact.RawSHA256) || artifactsBySHA[artifact.RawSHA256].decoded != nil {
			return "", errors.New("authority artifact path, URL, or uniqueness mismatch")
		}
		privatePayload, readErr := rootReadFile(filepath.Join(root, filepath.FromSlash(artifact.PrivatePath)))
		if readErr != nil || int64(len(privatePayload)) != artifact.DecodedBytes || payloadSHA(privatePayload) != artifact.RawSHA256 {
			return "", errors.New("retained private authority page mismatch")
		}
		sanitizedPayload, readErr := rootReadFile(filepath.Join(root, filepath.FromSlash(artifact.SanitizedPath)))
		if readErr != nil || payloadSHA(sanitizedPayload) != artifact.SanitizedSHA256 {
			return "", errors.New("sanitized authority export mismatch")
		}
		expectedSanitized, _ := canonical(struct {
			SchemaVersion, EndpointID, RawSHA256 string
			Bytes                                int64
		}{"authority_sanitized_export.v1", endpoint.ID, artifact.RawSHA256, int64(len(privatePayload))})
		if !bytes.Equal(sanitizedPayload, expectedSanitized) {
			return "", errors.New("sanitized authority export is not derived from retained page")
		}
		artifactsBySHA[artifact.RawSHA256] = fetchedAuthorityPage{artifact: artifact, decoded: privatePayload}
		logicalPages++
		transactions += artifact.TransactionCount
		wireBytes += artifact.WireBytes
		decodedBytes += artifact.DecodedBytes
	}
	if logicalPages == 0 || logicalPages != evidence.LogicalPages || transactions != evidence.Transactions || wireBytes != evidence.WireBytes || decodedBytes != evidence.DecodedBytes || logicalPages != preflight.TerminalLogicalPages || transactions != preflight.TerminalTransactions || wireBytes != preflight.TerminalWireBytes || decodedBytes != preflight.TerminalDecodedBytes || logicalPages > maxAuthorityLogicalPages || transactions > maxAuthorityTransactions || wireBytes > maxAuthoritySetBytes || decodedBytes > maxAuthoritySetBytes {
		return "", errors.New("authority terminal retrieval counters mismatch")
	}
	required := map[string]string{"match_id": evidence.Selection.MatchID, "league_id": evidence.Selection.LeagueID, "league_or_event": evidence.Selection.LeagueOrEvent, "series": evidence.Selection.Series, "game": evidence.Selection.Game, "radiant_team": evidence.Selection.RadiantTeam, "dire_team": evidence.Selection.DireTeam, "expected_start_begin": evidence.Selection.ExpectedStartBegin, "expected_start_end": evidence.Selection.ExpectedStartEnd}
	seenFacts := map[string]bool{}
	for _, binding := range evidence.FactBindings {
		expected, ok := required[binding.Fact]
		page, pageOK := artifactsBySHA[binding.ArtifactSHA256]
		if !ok || pageOK != true || seenFacts[binding.Fact] || binding.NormalizedValue != expected || binding.ExtractionProfileVersion != AuthorityExtractionVersion {
			return "", errors.New("authority fact binding identity mismatch")
		}
		seenFacts[binding.Fact] = true
		located, locateErr := locateAuthorityFact(binding.Fact, binding.NormalizedValue, page.artifact, page.decoded)
		if locateErr != nil || len(located) != 1 || located[0] != binding {
			return "", errors.New("authority fact locator did not re-extract exactly")
		}
	}
	if len(seenFacts) != len(required) {
		return "", errors.New("authority fact graph is incomplete")
	}
	return payloadSHA(preflightPayload), nil
}

func importAuthorityEvidenceRoot(destination, source, repo string) error {
	sourceLease, err := acquireExistingRoot(source, repo)
	if err != nil {
		return err
	}
	defer sourceLease.Close()
	evidencePayload, err := rootReadFile(filepath.Join(sourceLease.abs, "evidence/canonical/match-authority-evidence.json"))
	if err != nil {
		return err
	}
	var evidence MatchAuthorityEvidenceV1
	if err := strictCanonicalJSON(evidencePayload, &evidence); err != nil {
		return err
	}
	paths := []string{"evidence/canonical/match-authority-evidence.json", "evidence/canonical/match-authority-preflight.json"}
	for _, artifact := range evidence.Artifacts {
		paths = append(paths, artifact.PrivatePath, artifact.SanitizedPath)
	}
	for _, relative := range paths {
		clean := filepath.Clean(filepath.FromSlash(relative))
		if relative == "" || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
			return errors.New("authority snapshot path escapes its root")
		}
		payload, readErr := rootReadFile(filepath.Join(sourceLease.abs, clean))
		if readErr != nil {
			return readErr
		}
		if err := writePrivate(filepath.Join(destination, "evidence/authority-root", clean), payload); err != nil {
			return err
		}
	}
	return nil
}

func verifyAuthorityLiveIdentityRoot(rootPath, repo string, identity LiveIdentity) (string, error) {
	lease, err := acquireExistingRoot(rootPath, repo)
	if err != nil {
		return "", err
	}
	defer lease.Close()
	return verifyAuthorityLiveIdentityAt(lease.abs, identity)
}

func verifyAuthorityLiveIdentityAt(root string, identity LiveIdentity) (string, error) {
	payload, err := rootReadFile(filepath.Join(root, "evidence/canonical/match-authority-evidence.json"))
	if err != nil {
		return "", err
	}
	var evidence MatchAuthorityEvidenceV1
	if err := strictCanonicalJSON(payload, &evidence); err != nil {
		return "", err
	}
	selection := evidence.Selection
	if selection.MatchID != identity.MatchID || selection.LeagueOrEvent != identity.Tournament || selection.Series != identity.Series || selection.Game != identity.Game || selection.RadiantTeam != identity.Radiant || selection.DireTeam != identity.Dire {
		return "", errors.New("live identity contradicts the authority selection")
	}
	selectionPayload, _ := canonical(selection)
	return payloadSHA(selectionPayload), nil
}
