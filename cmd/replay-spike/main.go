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

	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay"
	"github.com/klauspost/compress/zstd"
)

const (
	opendotaMatchURL     = "https://api.opendota.com/api/matches/%s"
	opendotaPatchCatalogURL = "https://api.opendota.com/api/constants/patch"
	// Bounds defending an untrusted-input acquisition surface.
	maxMatchIDLen    = 20  // Steam match IDs are uint64 <= 20 digits
	maxCompressedBytes  = 1 << 30 // 1 GiB compressed replay ceiling
	maxDecompressedBytes = 4 << 30 // 4 GiB decompressed demo ceiling
	maxMetadataBytes     = 4 << 20 // 4 MiB metadata/patch-catalog body ceiling
)

var (
	zstdMagic      = []byte{0x28, 0xb5, 0x2f, 0xfd}
	pbDEMS2Magic   = []byte{'P', 'B', 'D', 'E', 'M', 'S', '2', 0x00}
	// httpClient is the single bounded client used for all acquisition calls.
	httpClient = &http.Client{Timeout: 90 * time.Second}
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
	MatchID             string    `json:"match_id"`
	Source              string    `json:"source"`
	ReplayURL           string    `json:"replay_url"`
	Cluster             int64     `json:"cluster"`
	// ReplayFormatVersion is OpenDota's per-replay `version` field (the demo
	// protocol/format revision), NOT the gameplay patch. It is retained for
	// provenance because it identifies the replay-binary format.
	ReplayFormatVersion int64     `json:"replay_format_version"`
	// PatchID is OpenDota's gameplay `patch` catalog id; PatchName is its
	// human-readable Dota version (e.g. id 60 -> "7.41"). These are the
	// authoritative gameplay-patch fields.
	PatchID             int64     `json:"patch_id"`
	PatchName           string    `json:"patch_name"`
	League              string    `json:"league"`
	LeagueTier          string    `json:"league_tier"`
	RadiantTeam         string    `json:"radiant_team"`
	DireTeam            string    `json:"dire_team"`
	DurationSec         int64     `json:"duration_sec"`
	PlayerCount         int       `json:"player_count"`
	CompressedBytes     int64     `json:"compressed_bytes"`
	DecompressedBytes   int64     `json:"decompressed_bytes"`
	CompressedSHA256   string    `json:"compressed_sha256"`
	DecompressedSHA256 string    `json:"decompressed_sha256"`
	CompressionActual   string    `json:"compression_actual"`
	DownloadedAt        time.Time `json:"downloaded_at"`
	DecompressedAt      time.Time `json:"decompressed_at"`
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
	if err := validateReplayURL(meta.ReplayURL); err != nil {
		fail("replay_url: %v", err)
	}

	bz2Path := filepath.Join(replaysDir, matchID+".dem.bz2")
	demPath := filepath.Join(replaysDir, matchID+".dem")
	dlAt := time.Now().UTC()
	if err := boundedDownload(meta.ReplayURL, bz2Path, maxCompressedBytes); err != nil {
		removePartial(bz2Path)
		fail("download: %v", err)
	}
	if err := validateMagic(bz2Path, zstdMagic); err != nil {
		removePartial(bz2Path)
		fail("download magic: %v", err)
	}
	cBytes, cSHA, err := hashAndSize(bz2Path)
	if err != nil {
		removePartial(bz2Path)
		fail("hash compressed: %v", err)
	}

	decompAt := time.Now().UTC()
	dBytes, dSHA, compression, err := zstdDecompressBounded(bz2Path, demPath, maxDecompressedBytes)
	if err != nil {
		removePartial(demPath)
		removePartial(demPath + ".tmp")
		fail("decompress: %v", err)
	}
	if err := validateMagic(demPath, pbDEMS2Magic); err != nil {
		removePartial(demPath)
		fail("decompressed magic: %v", err)
	}

	rec := acquireRecord{
		MatchID: matchID, Source: "opendota-metadata+valve-public-cdn",
		ReplayURL: meta.ReplayURL, Cluster: meta.Cluster,
		ReplayFormatVersion: meta.ReplayFormatVersion,
		PatchID: meta.PatchID, PatchName: meta.PatchName,
		League: meta.LeagueName, LeagueTier: meta.LeagueTier,
		RadiantTeam: meta.RadiantName, DireTeam: meta.DireName,
		DurationSec: meta.Duration, PlayerCount: meta.PlayerCount,
		CompressedBytes: cBytes, DecompressedBytes: dBytes,
		CompressedSHA256: cSHA, DecompressedSHA256: dSHA,
		CompressionActual: compression, DownloadedAt: dlAt, DecompressedAt: decompAt,
	}
	recPath := filepath.Join(factsDir, matchID+".acquire.json")
	rb, _ := json.MarshalIndent(rec, "", "  ")
	if err := os.WriteFile(recPath, append(rb, '\n'), 0o644); err != nil {
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
	ReplayFormatVersion int64 // OpenDota `version`: demo protocol/format revision
	PatchID             int64 // OpenDota `patch`: gameplay patch catalog id
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

// boundedDownload streams url to a temp file at dest+".tmp" with a hard size
// limit, fsyncs, closes, and atomically renames over dest. Partial temp files
// are removed on any failure path so a short/oversized body never looks
// canonical.
func boundedDownload(url, dest string, maxBytes int64) error {
	resp, err := httpClient.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("valve cdn status %d", resp.StatusCode)
	}
	tmp := dest + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	cleanup := func() { _ = os.Remove(tmp) }
	n, err := io.Copy(f, io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		f.Close()
		cleanup()
		return err
	}
	if n > maxBytes {
		f.Close()
		cleanup()
		return fmt.Errorf("download exceeds %d bytes (got %d)", maxBytes, n)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		cleanup()
		return err
	}
	if err := f.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmp, dest); err != nil {
		cleanup()
		return err
	}
	return nil
}

// zstdDecompressBounded streams src (zstd) to dst with a hard decompressed-size
// limit, fsyncs, closes, and atomically renames. A zstd bomb is rejected when
// the decompressed stream exceeds maxBytes.
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
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return 0, "", "", err
	}
	cleanup := func() { _ = os.Remove(tmp) }
	n, err := io.Copy(out, io.LimitReader(zr, maxBytes+1))
	if err != nil {
		out.Close()
		cleanup()
		return 0, "", "", err
	}
	if n > maxBytes {
		out.Close()
		cleanup()
		return 0, "", "", fmt.Errorf("decompressed exceeds %d bytes (got %d)", maxBytes, n)
	}
	if err := out.Sync(); err != nil {
		out.Close()
		cleanup()
		return 0, "", "", err
	}
	if err := out.Close(); err != nil {
		cleanup()
		return 0, "", "", err
	}
	if err := os.Rename(tmp, dst); err != nil {
		cleanup()
		return 0, "", "", err
	}
	dBytes, dSHA, err := hashAndSize(dst)
	if err != nil {
		return 0, "", "", err
	}
	if dBytes != n {
		return 0, "", "", fmt.Errorf("size mismatch %d vs %d", n, dBytes)
	}
	return n, dSHA, "zstd (file suffix .bz2 is historical)", nil
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

// validateReplayURL ensures the scheme is http/https and the host is a Valve
// replay CDN host, rejecting redirects to untrusted hosts from the metadata
// source before any network fetch.
func validateReplayURL(rawURL string) error {
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
	host := u.Hostname()
	if !looksLikeValveReplayHost(host) {
		return fmt.Errorf("host %q not in Valve replay CDN allowlist", host)
	}
	return nil
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

func removePartial(path string) {
	if path == "" {
		return
	}
	if _, err := os.Stat(path); err == nil {
		_ = os.Remove(path)
	}
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