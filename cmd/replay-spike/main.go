// Command replay-spike is the reproducible M0 replay acquisition, parsing, and
// batch-feasibility spike for DOT-29.
//
// It exercises three stages against the canonical local data root and never
// uses credentials or Game Coordinator automation:
//
//	replay-spike acquire <matchID>   resolve OpenDota metadata, download the
//	                                Valve public replay CDN file, decompress
//	                                (zstd), and write a provenance record.
//	replay-spike parse   <demFile>  parse one decompressed Source 2 demo into
//	                                deterministic ReplayFactsV1 with measured
//	                                metrics. --twice proves byte-identical hash.
//	replay-spike batch   <manifest> run the resumable, deduplicating batch
//	                                state machine over a match manifest.
//
// Raw replays and parsed facts stay under the local data root and out of git.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/atomicfile"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay"
	"github.com/klauspost/compress/zstd"
)

const (
	opendotaMatchURL        = "https://api.opendota.com/api/matches/%s"
	opendotaPatchCatalogURL = "https://api.opendota.com/api/constants/patch"
	// Bounds defending an untrusted-input acquisition surface.
	maxMatchIDLen        = 20      // Steam match IDs are uint64 <= 20 digits
	maxCompressedBytes   = 1 << 30 // 1 GiB compressed replay ceiling
	maxDecompressedBytes = 4 << 30 // 4 GiB decompressed demo ceiling
	maxMetadataBytes     = 4 << 20 // 4 MiB metadata/patch-catalog body ceiling
	maxReplayRedirects   = 5
)

var (
	zstdMagic    = []byte{0x28, 0xb5, 0x2f, 0xfd}
	pbDEMS2Magic = []byte{'P', 'B', 'D', 'E', 'M', 'S', '2', 0x00}
	// httpClient is the single bounded client used for all acquisition calls.
	httpClient = &http.Client{Timeout: 90 * time.Second}
	// acquisitionAtomicOps is empty in production and injectable in tests.
	acquisitionAtomicOps atomicfile.Ops
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "acquire":
		cmdAcquire(os.Args[2:])
	case "parse":
		cmdParse(os.Args[2:])
	case "batch":
		cmdBatch(os.Args[2:])
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `replay-spike: M0 replay acquisition/parser/storage spike

  replay-spike acquire <matchID> [--data-dir DIR]
  replay-spike parse   <demFile> [--out FILE] [--twice]
  replay-spike batch   <manifest> [--data-dir DIR] [--retry-all] [--max-retries N]
`)
}

func cmdParse(args []string) {
	fs := flag.NewFlagSet("parse", flag.ExitOnError)
	out := fs.String("out", "", "write canonical facts JSON to this path")
	twice := fs.Bool("twice", false, "parse twice and assert byte-identical hash")
	fs.Parse(reorderFlags(args, map[string]bool{"twice": true}))
	if fs.NArg() < 1 {
		fs.Usage()
		os.Exit(2)
	}
	path := fs.Arg(0)
	first := printOne(path, *out)
	if !*twice {
		return
	}
	pr2, err := replay.ParseFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "second parse failed: %v\n", err)
		os.Exit(1)
	}
	b2, _ := pr2.Facts.CanonicalJSON()
	fmt.Printf("\n[second pass] hash=%s json_bytes=%d elapsed=%.3fs user_cpu=%.3fs system_cpu=%.3fs peak_heap=%dMiB\n",
		pr2.Hash, len(b2), pr2.Metrics.ElapsedSec, pr2.Metrics.UserCPUSec, pr2.Metrics.SystemCPUSec, pr2.Metrics.PeakHeapMiB)
	fmt.Printf("deterministic: %v\n", pr2.Hash == first)
}

func printOne(path, out string) string {
	pr, err := replay.ParseFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "parse failed: %v\n", err)
		os.Exit(1)
	}
	b, _ := pr.Facts.CanonicalJSON()
	fmt.Printf("hash=%s\n", pr.Hash)
	fmt.Printf("json_bytes=%d\n", len(b))
	fmt.Printf("elapsed_sec=%.3f user_cpu_sec=%.3f system_cpu_sec=%.3f peak_heap_mib=%d num_gc=%d goroutines=%d raw_bytes=%d\n",
		pr.Metrics.ElapsedSec, pr.Metrics.UserCPUSec, pr.Metrics.SystemCPUSec,
		pr.Metrics.PeakHeapMiB, pr.Metrics.NumGC, pr.Metrics.Goroutines, pr.Metrics.RawDecompressed)
	fmt.Printf("meta: build=%d server=%q last_tick=%d last_net_tick=%d max_combat_log_ts=%.1f\n",
		pr.Facts.Meta.GameBuild, pr.Facts.Meta.ServerName, pr.Facts.Meta.LastTick, pr.Facts.Meta.LastNetTick, pr.Facts.Meta.MaxCombatLogTimestampSec)
	fmt.Printf("combat_log_total=%d message_types=%d combat_types=%d heroes=%d timeline=%d item_use_kinds=%d\n",
		pr.Facts.CombatLogTotal, len(pr.Facts.MessageCounts), len(pr.Facts.CombatLogTypeCounts),
		len(pr.Facts.Heroes), len(pr.Facts.Timeline), len(pr.Facts.ItemUses))
	if out != "" {
		if err := os.WriteFile(out, append(b, '\n'), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "write out: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("wrote %s\n", out)
	}
	if pr.Metrics.ParseError != "" {
		fmt.Printf("parser_error=%q\n", pr.Metrics.ParseError)
	}
	fmt.Printf("availability: available=%d deferred=%d unavailable=%d\n",
		len(pr.Facts.Availability.Available), len(pr.Facts.Availability.Deferred), len(pr.Facts.Availability.Unavailable))
	return pr.Hash
}

type acquireRecord struct {
	MatchID   string `json:"match_id"`
	Source    string `json:"source"`
	ReplayURL string `json:"replay_url"`
	Cluster   int64  `json:"cluster"`
	// ReplayFormatVersion is OpenDota's per-replay `version` field (the demo
	// protocol/format revision), NOT the gameplay patch. It is retained for
	// provenance because it identifies the replay-binary format.
	ReplayFormatVersion int64 `json:"replay_format_version"`
	// PatchID is OpenDota's gameplay `patch` catalog id; PatchName is its
	// human-readable Dota version (e.g. id 60 -> "7.41"). These are the
	// authoritative gameplay-patch fields.
	PatchID                  int64     `json:"patch_id"`
	PatchName                string    `json:"patch_name"`
	League                   string    `json:"league"`
	LeagueTier               string    `json:"league_tier"`
	RadiantTeam              string    `json:"radiant_team"`
	DireTeam                 string    `json:"dire_team"`
	DurationSec              int64     `json:"duration_sec"`
	PlayerCount              int       `json:"player_count"`
	CompressedBytes          int64     `json:"compressed_bytes"`
	DecompressedBytes        int64     `json:"decompressed_bytes"`
	CompressedSHA256         string    `json:"compressed_sha256"`
	DecompressedSHA256       string    `json:"decompressed_sha256"`
	CompressionActual        string    `json:"compression_actual"`
	DownloadedAt             time.Time `json:"downloaded_at"`
	DecompressedAt           time.Time `json:"decompressed_at"`
	TransportScheme          string    `json:"transport_scheme"`
	UnauthenticatedTransport bool      `json:"unauthenticated_transport"`
	SourceQuality            string    `json:"source_quality"`
	IdentityCorrelation      string    `json:"identity_correlation"`
}

func cmdAcquire(args []string) {
	fs := flag.NewFlagSet("acquire", flag.ExitOnError)
	dataDir := fs.String("data-dir", "data", "canonical local data root")
	fs.Parse(reorderFlags(args, nil))
	if fs.NArg() < 1 {
		fs.Usage()
		os.Exit(2)
	}
	matchID := fs.Arg(0)
	if err := validateMatchID(matchID); err != nil {
		fail("%v", err)
	}
	replaysDir := filepath.Join(*dataDir, "replays")
	factsDir := filepath.Join(*dataDir, "replay-facts")
	for _, d := range []string{replaysDir, factsDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			fail("mkdir %s: %v", d, err)
		}
	}

	meta, err := fetchOpenDotaMatch(matchID)
	if err != nil {
		fail("opendota metadata: %v", err)
	}
	if err := validateReplayURL(meta.ReplayURL, matchID); err != nil {
		fail("replay_url: %v", err)
	}

	bz2Path := filepath.Join(replaysDir, matchID+".dem.bz2")
	demPath := filepath.Join(replaysDir, matchID+".dem")
	dlAt := time.Now().UTC()
	if err := boundedDownload(meta.ReplayURL, bz2Path, maxCompressedBytes, matchID); err != nil {
		fail("download: %v", err)
	}
	if err := validateMagic(bz2Path, zstdMagic); err != nil {
		fail("download magic: %v", err)
	}
	cBytes, cSHA, err := hashAndSize(bz2Path)
	if err != nil {
		fail("hash compressed: %v", err)
	}

	decompAt := time.Now().UTC()
	dBytes, dSHA, compression, err := zstdDecompressBounded(bz2Path, demPath, maxDecompressedBytes)
	if err != nil {
		fail("decompress: %v", err)
	}
	if err := validateMagic(demPath, pbDEMS2Magic); err != nil {
		fail("decompressed magic: %v", err)
	}

	rec := newAcquireRecord(matchID, meta)
	rec.Cluster = meta.Cluster
	rec.ReplayFormatVersion = meta.ReplayFormatVersion
	rec.PatchID, rec.PatchName = meta.PatchID, meta.PatchName
	rec.League, rec.LeagueTier = meta.LeagueName, meta.LeagueTier
	rec.RadiantTeam, rec.DireTeam = meta.RadiantName, meta.DireName
	rec.DurationSec, rec.PlayerCount = meta.Duration, meta.PlayerCount
	rec.CompressedBytes, rec.DecompressedBytes = cBytes, dBytes
	rec.CompressedSHA256, rec.DecompressedSHA256 = cSHA, dSHA
	rec.CompressionActual, rec.DownloadedAt, rec.DecompressedAt = compression, dlAt, decompAt
	recPath := filepath.Join(factsDir, matchID+".acquire.json")
	if err := writeAcquireRecord(recPath, rec); err != nil {
		fail("write acquire record: %v", err)
	}
	fmt.Printf("acquired match %s\n", matchID)
	fmt.Printf("  replay_url=%s\n", meta.ReplayURL)
	fmt.Printf("  league=%q tier=%q radiant=%s dire=%s replay_format_version=%d patch_id=%d patch_name=%q duration=%ds players=%d\n",
		meta.LeagueName, meta.LeagueTier, meta.RadiantName, meta.DireName,
		meta.ReplayFormatVersion, meta.PatchID, meta.PatchName, meta.Duration, meta.PlayerCount)
	fmt.Printf("  compressed=%d bytes sha256=%s\n", cBytes, cSHA[:16])
	fmt.Printf("  decompressed=%d bytes sha256=%s compression=%s\n", dBytes, dSHA[:16], compression)
	fmt.Printf("  dem=%s\n", demPath)
	fmt.Printf("  record=%s\n", recPath)
	if rec.UnauthenticatedTransport {
		fmt.Printf("  warning=unauthenticated offline transport; source remains untrusted pending parser/metadata identity correlation\n")
	}
}

func newAcquireRecord(matchID string, meta matchMeta) acquireRecord {
	u, _ := url.Parse(meta.ReplayURL)
	scheme := u.Scheme
	return acquireRecord{
		MatchID: matchID, Source: "opendota-metadata+valve-public-cdn", ReplayURL: meta.ReplayURL,
		TransportScheme: scheme, UnauthenticatedTransport: scheme == "http",
		SourceQuality:       "untrusted_pending_identity_correlation",
		IdentityCorrelation: "pending_m1_parser_match_build_time_reconciliation",
	}
}

func cmdBatch(args []string) {
	fs := flag.NewFlagSet("batch", flag.ExitOnError)
	dataDir := fs.String("data-dir", "data", "canonical local data root")
	retryAll := fs.Bool("retry-all", false, "reprocess even succeeded entries")
	maxRetries := fs.Int("max-retries", 1, "parse attempts before terminal failure")
	fs.Parse(reorderFlags(args, map[string]bool{"retry-all": true}))
	if fs.NArg() < 1 {
		fs.Usage()
		os.Exit(2)
	}
	manifestPath := fs.Arg(0)
	mb, err := os.ReadFile(manifestPath)
	if err != nil {
		fail("read manifest: %v", err)
	}
	var man replay.Manifest
	if err := json.Unmarshal(mb, &man); err != nil {
		fail("decode manifest: %v", err)
	}
	r := &replay.Runner{
		StatePath:  filepath.Join(*dataDir, "replay-facts", "batch-state.json"),
		FactsDir:   filepath.Join(*dataDir, "replay-facts"),
		MaxRetries: *maxRetries,
		RetryAll:   *retryAll,
	}
	st, errs := r.Run(&man)
	for _, e := range errs {
		fmt.Fprintf(os.Stderr, "batch error: %v\n", e)
	}
	fmt.Printf("batch manifest=%s entries=%d\n", manifestPath, len(man.Entries))
	if st == nil {
		os.Exit(1)
	}
	counts := map[replay.StageStatus]int{}
	for _, id := range st.SortedKeys() {
		e := st.Entries[id]
		counts[e.Status]++
		fmt.Printf("  %-20s %-16s attempts=%d facts=%s content=%s err=%q\n",
			id, e.Status, e.Attempts, shortHash(e.FactsHash), shortHash(e.ContentSHA256), e.LastError)
	}
	fmt.Printf("summary: succeeded=%d queued=%d running=%d failed=%d\n",
		counts[replay.StatusSucceeded], counts[replay.StatusQueued],
		counts[replay.StatusRunning], counts[replay.StatusFailedTerminal])
	if len(errs) > 0 {
		os.Exit(1)
	}
}

type matchMeta struct {
	ReplayURL           string
	Cluster             int64
	ReplayFormatVersion int64  // OpenDota `version`: demo protocol/format revision
	PatchID             int64  // OpenDota `patch`: gameplay patch catalog id
	PatchName           string // resolved from /api/constants/patch (e.g. "7.41")
	LeagueName          string
	LeagueTier          string
	RadiantName         string
	DireName            string
	Duration            int64
	PlayerCount         int
}

func fetchOpenDotaMatch(matchID string) (matchMeta, error) {
	resp, err := httpClient.Get(fmt.Sprintf(opendotaMatchURL, matchID))
	if err != nil {
		return matchMeta{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return matchMeta{}, fmt.Errorf("opendota status %d", resp.StatusCode)
	}
	var raw struct {
		ReplayURL   string `json:"replay_url"`
		Cluster     int64  `json:"cluster"`
		Version     int64  `json:"version"`
		Patch       int64  `json:"patch"`
		Duration    int64  `json:"duration"`
		RadiantName string `json:"radiant_name"`
		DireName    string `json:"dire_name"`
		League      struct {
			Name string `json:"name"`
			Tier string `json:"tier"`
		} `json:"league"`
		Players []struct {
			AccountID int64 `json:"account_id"`
		} `json:"players"`
	}
	// Bound the metadata body to avoid an oversized/malicious response.
	dec := json.NewDecoder(io.LimitReader(resp.Body, maxMetadataBytes))
	if err := dec.Decode(&raw); err != nil {
		return matchMeta{}, err
	}
	patchName, _ := fetchPatchName(raw.Patch)
	if patchName == "" && raw.Patch > 0 {
		patchName = fmt.Sprintf("patch_%d", raw.Patch) // honest fallback, never invented
	}
	return matchMeta{
		ReplayURL: raw.ReplayURL, Cluster: raw.Cluster,
		ReplayFormatVersion: raw.Version, PatchID: raw.Patch, PatchName: patchName,
		LeagueName: raw.League.Name, LeagueTier: raw.League.Tier,
		RadiantName: raw.RadiantName, DireName: raw.DireName,
		Duration: raw.Duration, PlayerCount: len(raw.Players),
	}, nil
}

// fetchPatchName resolves an OpenDota patch catalog id to its Dota version
// name (e.g. 60 -> "7.41") from /api/constants/patch. A missing/unknown id
// yields an empty string, which the caller maps to an honest fallback.
func fetchPatchName(patchID int64) (string, error) {
	if patchID <= 0 {
		return "", nil
	}
	resp, err := httpClient.Get(opendotaPatchCatalogURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("patch catalog status %d", resp.StatusCode)
	}
	var catalog []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxMetadataBytes)).Decode(&catalog); err != nil {
		return "", err
	}
	for _, p := range catalog {
		if p.ID == patchID {
			return p.Name, nil
		}
	}
	return "", nil
}

// boundedDownload streams a validated Valve replay URL to a randomized,
// exclusive adjacent temp file with a hard size limit. Every redirect and the
// final URL are allowlisted. The temp is hashed, magic-validated, fsynced, and
// closed before atomic rename; the parent directory is then fsynced. A failed
// directory sync is surfaced as an uncertain commit after identity reconciliation.
func boundedDownload(rawURL, dest string, maxBytes int64, expectedMatchID string) error {
	if err := validateReplayURL(rawURL, expectedMatchID); err != nil {
		return err
	}
	initial, _ := url.Parse(rawURL)
	client := *httpClient
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxReplayRedirects {
			return fmt.Errorf("too many redirects (max %d)", maxReplayRedirects)
		}
		// Validate the raw Location value before net/http normalization can erase
		// an empty query, fragment, or port delimiter.
		if req.Response != nil {
			location := req.Response.Header.Get("Location")
			if err := validateReplayURL(location, expectedMatchID); err != nil {
				return fmt.Errorf("redirect Location: %w", err)
			}
		}
		if err := validateReplayURL(req.URL.String(), expectedMatchID); err != nil {
			return fmt.Errorf("redirect URL: %w", err)
		}
		if req.URL.Scheme != initial.Scheme {
			return fmt.Errorf("redirect crosses transport policy from %s to %s", initial.Scheme, req.URL.Scheme)
		}
		return nil
	}
	resp, err := client.Get(rawURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	finalURL := rawURL
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}
	if err := validateReplayURL(finalURL, expectedMatchID); err != nil {
		return fmt.Errorf("final response URL: %w", err)
	}
	finalParsed, _ := url.Parse(finalURL)
	if finalParsed.Scheme != initial.Scheme {
		return fmt.Errorf("final response crosses transport policy from %s to %s", initial.Scheme, finalParsed.Scheme)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("valve cdn status %d", resp.StatusCode)
	}
	f, err := atomicfile.NewTemp(dest, 0o644)
	if err != nil {
		return err
	}
	tmp := f.Name()
	committed := false
	defer func() {
		if !committed {
			_ = f.Close()
			_ = os.Remove(tmp)
		}
	}()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return err
	}
	if n > maxBytes {
		return fmt.Errorf("download exceeds %d bytes (got %d)", maxBytes, n)
	}
	digest := hex.EncodeToString(h.Sum(nil))
	if err := atomicfile.CommitWithOps(f, dest, func(path string) error {
		return validateFileIdentity(path, zstdMagic, n, digest)
	}, acquisitionAtomicOps); err != nil {
		return err
	}
	committed = true
	return nil
}

// zstdDecompressBounded streams src (zstd) to a randomized adjacent temp with a
// hard decompressed-size limit. It hashes and PBDEMS2-validates the temp before
// atomic replacement plus directory sync. A zstd bomb is rejected at maxBytes;
// post-rename sync uncertainty is reconciled by full identity and surfaced.
func zstdDecompressBounded(src, dst string, maxBytes int64) (int64, string, string, error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, "", "", err
	}
	defer in.Close()
	zr, err := zstd.NewReader(in)
	if err != nil {
		return 0, "", "", err
	}
	defer zr.Close()
	out, err := atomicfile.NewTemp(dst, 0o644)
	if err != nil {
		return 0, "", "", err
	}
	tmp := out.Name()
	committed := false
	defer func() {
		if !committed {
			_ = out.Close()
			_ = os.Remove(tmp)
		}
	}()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(zr, maxBytes+1))
	if err != nil {
		return 0, "", "", err
	}
	if n > maxBytes {
		return 0, "", "", fmt.Errorf("decompressed exceeds %d bytes (got %d)", maxBytes, n)
	}
	dSHA := hex.EncodeToString(h.Sum(nil))
	if err := atomicfile.CommitWithOps(out, dst, func(path string) error {
		return validateFileIdentity(path, pbDEMS2Magic, n, dSHA)
	}, acquisitionAtomicOps); err != nil {
		return 0, "", "", err
	}
	committed = true
	return n, dSHA, "zstd (file suffix .bz2 is historical)", nil
}

func writeAcquireRecord(path string, rec acquireRecord) error {
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.WriteFileWithOps(path, append(b, '\n'), 0o644, acquisitionAtomicOps)
}

func validateFileIdentity(path string, magic []byte, expectedSize int64, expectedSHA string) error {
	if err := validateMagic(path, magic); err != nil {
		return err
	}
	size, digest, err := hashAndSize(path)
	if err != nil {
		return err
	}
	if size != expectedSize || digest != expectedSHA {
		return fmt.Errorf("identity mismatch: size=%d sha256=%s, want size=%d sha256=%s", size, digest, expectedSize, expectedSHA)
	}
	return nil
}

func hashAndSize(path string) (int64, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return 0, "", err
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}

// validateMatchID ensures a match ID is numeric and bounded so it cannot contain
// path separators or be used to escape the data root filename slot.
func validateMatchID(id string) error {
	if id == "" || len(id) > maxMatchIDLen {
		return fmt.Errorf("match id must be 1-%d chars", maxMatchIDLen)
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return fmt.Errorf("match id must be numeric, got %q", id)
		}
	}
	return nil
}

// validateReplayURL enforces the approved offline replay transport boundary:
// HTTP or HTTPS, exact Valve replay host/path shape, no credentials/ports/query/
// fragment, and optional correlation to the requested match ID.
func validateReplayURL(rawURL, expectedMatchID string) error {
	if rawURL == "" {
		return fmt.Errorf("no replay_url (OpenDota may have purged the match)")
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("scheme %q not allowed", u.Scheme)
	}
	if u.User != nil {
		return fmt.Errorf("embedded credentials not allowed")
	}
	if u.ForceQuery || u.RawQuery != "" || u.Fragment != "" || strings.Contains(rawURL, "#") {
		return fmt.Errorf("query and fragment not allowed")
	}
	host := u.Hostname()
	if !looksLikeValveReplayHost(host) {
		return fmt.Errorf("host %q not in Valve replay CDN allowlist", host)
	}
	if u.Host != host {
		return fmt.Errorf("authority %q must equal approved hostname %q with no explicit port", u.Host, host)
	}
	matchID, err := replayPathMatchID(u.EscapedPath())
	if err != nil {
		return err
	}
	if expectedMatchID != "" && matchID != expectedMatchID {
		return fmt.Errorf("replay path match %q does not equal requested match %q", matchID, expectedMatchID)
	}
	return nil
}

func replayPathMatchID(path string) (string, error) {
	const prefix, suffix = "/570/", ".dem.bz2"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return "", fmt.Errorf("path %q is not approved Valve replay shape /570/<match>_<salt>.dem.bz2", path)
	}
	stem := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	parts := strings.Split(stem, "_")
	if len(parts) != 2 || validateMatchID(parts[0]) != nil || validateMatchID(parts[1]) != nil {
		return "", fmt.Errorf("path %q has nonnumeric or malformed match/salt", path)
	}
	return parts[0], nil
}

// looksLikeValveReplayHost matches replay<digits>.valve.net exactly.
func looksLikeValveReplayHost(host string) bool {
	if !strings.HasSuffix(host, ".valve.net") {
		return false
	}
	prefix := strings.TrimSuffix(host, ".valve.net")
	if !strings.HasPrefix(prefix, "replay") {
		return false
	}
	rest := strings.TrimPrefix(prefix, "replay")
	if rest == "" {
		return false
	}
	for _, r := range rest {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// validateMagic reads the first len(magic) bytes of path and compares.
func validateMagic(path string, magic []byte) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	buf := make([]byte, len(magic))
	if _, err := io.ReadFull(f, buf); err != nil {
		return fmt.Errorf("read magic: %w", err)
	}
	if !bytes.Equal(buf, magic) {
		return fmt.Errorf("magic mismatch: got %x, want %x", buf, magic)
	}
	return nil
}

func shortHash(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	os.Exit(1)
}

// reorderFlags moves flag tokens before positional tokens so the stdlib flag
// package (which stops at the first non-flag) still sees them when users write
// `cmd <positional> --flag`. bools names flags that do not consume a value.
func reorderFlags(args []string, bools map[string]bool) []string {
	var flags, pos []string
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			pos = append(pos, a)
			i++
			continue
		}
		key := strings.TrimLeft(a, "-")
		if idx := strings.IndexByte(key, '='); idx >= 0 {
			flags = append(flags, a)
			i++
			continue
		}
		if bools[key] {
			flags = append(flags, a)
			i++
			continue
		}
		flags = append(flags, a)
		if i+1 < len(args) {
			flags = append(flags, args[i+1])
			i += 2
		} else {
			i++
		}
	}
	return append(flags, pos...)
}
