package replay

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"runtime"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/dotabuff/manta"
	"github.com/dotabuff/manta/dota"
)

var source2DemoMagic = []byte{'P', 'B', 'D', 'E', 'M', 'S', '2', 0}

// Source2DemoIdentity is the bounded source-specific identity checked before
// manta sees a replay. SHA-256 identifies bytes; it does not authenticate the
// public transport they came from.
type Source2DemoIdentity struct {
	SHA256 string
	Bytes  int64
	Magic  string
}

// InspectSource2DemoFile proves the input is a binary Source 2 demo beginning
// with PBDEMS2, rather than JSON or another payload renamed with .dem.
func InspectSource2DemoFile(path string) (Source2DemoIdentity, error) {
	f, err := os.Open(path)
	if err != nil {
		return Source2DemoIdentity{}, err
	}
	defer f.Close()
	header := make([]byte, len(source2DemoMagic))
	if _, err := io.ReadFull(f, header); err != nil {
		return Source2DemoIdentity{}, fmt.Errorf("replay: source2 header: %w", err)
	}
	if !bytes.Equal(header, source2DemoMagic) {
		return Source2DemoIdentity{}, fmt.Errorf("replay: source2 magic mismatch")
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return Source2DemoIdentity{}, err
	}
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return Source2DemoIdentity{}, err
	}
	return Source2DemoIdentity{SHA256: hex.EncodeToString(h.Sum(nil)), Bytes: n, Magic: "PBDEMS2\\x00"}, nil
}

// Metrics are the measured parse costs reported alongside the facts.
// UserCPUSec/SystemCPUSec are process CPU seconds from getrusage(RUSAGE_SELF),
// measured across the parse call only, so multi-process hosts do not inflate
// them. PeakHeapMiB is the largest Alloc observed by the 25ms sampler.
type Metrics struct {
	ElapsedSec      float64 `json:"elapsed_sec"`
	UserCPUSec      float64 `json:"user_cpu_sec"`
	SystemCPUSec    float64 `json:"system_cpu_sec"`
	PeakHeapMiB     uint64  `json:"peak_heap_mib"`
	NumGC           uint32  `json:"num_gc"`
	Goroutines      int     `json:"goroutines"`
	RawDecompressed int64   `json:"raw_decompressed_bytes"`
	Outcome         string  `json:"outcome"`
	ParseError      string  `json:"parse_error,omitempty"`
}

// ParseResult bundles the deterministic facts and measured costs.
type ParseResult struct {
	Facts   *ReplayFactsV1
	Hash    string
	Metrics Metrics
}

// ParseStream parses a decompressed Source 2 demo stream with the manta
// adapter and returns deterministic facts plus measured metrics. reader must
// start at the PBDEMS2 magic; the outer zstd/bz2 shell must already be removed.
func ParseStream(reader io.Reader, rawDecompressedBytes int64) (*ParseResult, error) {
	p, err := manta.NewStreamParser(reader)
	if err != nil {
		return nil, fmt.Errorf("replay: create parser: %w", err)
	}

	c := &Collected{MessageCounts: map[string]uint64{}}
	var maxTs float64
	table := "CombatLogNames"
	lookup := func(idx uint32) string {
		s, ok := p.LookupStringByIndex(table, int32(idx))
		if !ok {
			return ""
		}
		return s
	}

	p.Callbacks.OnCDemoFileHeader(func(m *dota.CDemoFileHeader) error {
		c.ServerName = m.GetServerName()
		return nil
	})
	p.Callbacks.OnCDemoPacket(func(m *dota.CDemoPacket) error { c.MessageCounts["CDemoPacket"]++; return nil })
	p.Callbacks.OnCDemoFullPacket(func(m *dota.CDemoFullPacket) error { c.MessageCounts["CDemoFullPacket"]++; return nil })
	p.Callbacks.OnCNETMsg_Tick(func(m *dota.CNETMsg_Tick) error {
		c.MessageCounts["CNETMsg_Tick"]++
		if m.GetTick() > c.LastNetTick {
			c.LastNetTick = m.GetTick()
		}
		return nil
	})
	p.Callbacks.OnCSVCMsg_PacketEntities(func(m *dota.CSVCMsg_PacketEntities) error { c.MessageCounts["CSVCMsg_PacketEntities"]++; return nil })
	p.Callbacks.OnCMsgSource1LegacyGameEvent(func(m *dota.CMsgSource1LegacyGameEvent) error {
		c.MessageCounts["Source1LegacyGameEvent"]++
		return nil
	})

	p.Callbacks.OnCMsgDOTACombatLogEntry(func(m *dota.CMsgDOTACombatLogEntry) error {
		ts := float64(m.GetTimestamp())
		if ts > maxTs {
			maxTs = ts
		}
		name := dota.DOTA_COMBATLOG_TYPES_name[int32(m.GetType())]
		c.Combat = append(c.Combat, CombatEvent{
			Type:         name,
			Timestamp:    ts,
			Attacker:     lookup(m.GetAttackerName()),
			Target:       lookup(m.GetTargetName()),
			Inflictor:    lookup(m.GetInflictorName()),
			Value:        int64(m.GetValue()),
			GoldReason:   m.GetGoldReason(),
			BuildingType: m.GetBuildingType(),
		})
		return nil
	})

	stop := make(chan struct{})
	stopped := make(chan struct{})
	var peak uint64
	go func() {
		defer close(stopped)
		t := time.NewTicker(25 * time.Millisecond)
		defer t.Stop()
		var s runtime.MemStats
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				runtime.ReadMemStats(&s)
				if s.Alloc > peak {
					atomic.StoreUint64(&peak, s.Alloc)
				}
			}
		}
	}()

	var ru0 syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru0); err != nil {
		ru0 = syscall.Rusage{}
	}
	t0 := time.Now()
	perr := p.Start()
	elapsed := time.Since(t0)
	close(stop)
	<-stopped // join the sampler goroutine so resource cleanup is deterministic

	var ru1 syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru1); err != nil {
		ru1 = syscall.Rusage{}
	}

	c.LastTick = p.Tick
	c.LastNetTick = p.NetTick
	c.GameBuild = p.GameBuild
	c.MaxTimestamp = maxTs

	m := Metrics{
		ElapsedSec:      elapsed.Seconds(),
		UserCPUSec:      rusageSeconds(ru1.Utime, ru0.Utime),
		SystemCPUSec:    rusageSeconds(ru1.Stime, ru0.Stime),
		RawDecompressed: rawDecompressedBytes,
	}
	if peak > 0 {
		m.PeakHeapMiB = atomic.LoadUint64(&peak) / (1 << 20)
	} else {
		var s runtime.MemStats
		runtime.ReadMemStats(&s)
		m.PeakHeapMiB = s.Alloc / (1 << 20)
	}
	var s runtime.MemStats
	runtime.ReadMemStats(&s)
	m.NumGC = s.NumGC
	m.Goroutines = runtime.NumGoroutine()

	if perr != nil {
		m.Outcome = "parse_error"
		m.ParseError = perr.Error()
		return &ParseResult{Metrics: m}, perr
	}

	facts := BuildFacts(c)
	hash, err := facts.Hash()
	if err != nil {
		m.Outcome = "hash_error"
		m.ParseError = err.Error()
		return &ParseResult{Metrics: m}, err
	}
	m.Outcome = "ok"
	return &ParseResult{Facts: facts, Hash: hash, Metrics: m}, nil
}

// ParseFile opens a decompressed .dem file and parses it.
func ParseFile(path string) (*ParseResult, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ParseStream(f, info.Size())
}

// rusageSeconds returns the CPU seconds used between two timeval values
// (tv_sec + tv_usec/1e6). rusage Utime/Stime are syscall.Timeval on Linux.
func rusageSeconds(end, start syscall.Timeval) float64 {
	us := int64(end.Sec-start.Sec)*1_000_000 + int64(end.Usec-start.Usec)
	return float64(us) / 1_000_000.0
}
