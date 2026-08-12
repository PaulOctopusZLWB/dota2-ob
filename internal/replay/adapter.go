package replay

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/dotabuff/manta"
	"github.com/dotabuff/manta/dota"
)

// Metrics are the measured parse costs reported alongside the facts.
type Metrics struct {
	ElapsedSec      float64 `json:"elapsed_sec"`
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
	p.Callbacks.OnCMsgSource1LegacyGameEvent(func(m *dota.CMsgSource1LegacyGameEvent) error { c.MessageCounts["Source1LegacyGameEvent"]++; return nil })

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
	var peak uint64
	go func() {
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

	t0 := time.Now()
	perr := p.Start()
	close(stop)
	elapsed := time.Since(t0)

	c.LastTick = p.Tick
	c.LastNetTick = p.NetTick
	c.GameBuild = p.GameBuild
	c.MaxTimestamp = maxTs

	m := Metrics{ElapsedSec: elapsed.Seconds(), RawDecompressed: rawDecompressedBytes}
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