// Package parser adapts the pinned dotabuff/manta extraction library into the
// repository's raw observation stream. It streams a Source 2 demo once,
// resolving string-table names eagerly, and writes each raw observation to a
// raw.Writer without loading the demo into memory.
package parser

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/dotabuff/manta"
	"github.com/dotabuff/manta/dota"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/replay/raw"
)

const combatLogNamesTable = "CombatLogNames"

// Result reports extraction outcome and measured cost.
type Result struct {
	LastTick    uint32
	LastNetTick uint32
	GameBuild   uint32
	CombatTotal uint64
	Events      int64
	BytesRead   int64
	ElapsedSec  float64
	PeakHeapMiB uint64
	Outcome     string
	Error       string
}

// ParseStream parses a decompressed Source 2 demo stream with the manta
// adapter, writing raw observations to w. The reader must start at the
// PBDEMS2 magic; the outer zstd shell must already be removed.
func ParseStream(r io.Reader, w *raw.Writer, bytesTotal int64) (*Result, error) {
	p, err := manta.NewStreamParser(r)
	if err != nil {
		return nil, fmt.Errorf("replay/parser: create parser: %w", err)
	}

	counts := map[string]uint64{}
	var maxCombatTS float64
	combatTotal := uint64(0)
	var combatSeq int64

	lookup := func(idx uint32) string {
		s, ok := p.LookupStringByIndex(combatLogNamesTable, int32(idx))
		if !ok {
			return ""
		}
		return s
	}

	// Track hero entity snapshots. Hero entities are created at spawn,
	// deleted on death, and recreated on respawn; updates arrive at the
	// engine cadence. Only hero units are retained to keep the stream lean.
	p.OnEntity(func(e *manta.Entity, op manta.EntityOp) error {
		cls := e.GetClassName()
		if strings.HasPrefix(cls, "CDOTA_Unit_Hero_") {
			hs := &raw.HeroState{
				Tick:      p.Tick,
				HeroIndex: e.GetIndex(),
				Class:     cls,
			}
			if v, ok := e.GetInt32("m_iPlayerID"); ok {
				hs.PlayerID = &v
			}
			if v, ok := e.GetInt32("m_iTeamNum"); ok {
				hs.TeamNum = &v
			}
			if v, ok := e.GetFloat32("CBodyComponent.m_vecX"); ok {
				f := float64(v)
				hs.PosX = &f
			}
			if v, ok := e.GetFloat32("CBodyComponent.m_vecY"); ok {
				f := float64(v)
				hs.PosY = &f
			}
			if v, ok := e.GetFloat32("CBodyComponent.m_vecZ"); ok {
				f := float64(v)
				hs.PosZ = &f
			}
			if v, ok := e.GetInt32("m_iHealth"); ok {
				i := int64(v)
				hs.Health = &i
			}
			if v, ok := e.GetInt32("m_iMaxHealth"); ok {
				i := int64(v)
				hs.MaxHealth = &i
			}
			if v, ok := e.GetFloat32("m_flMana"); ok {
				f := float64(v)
				hs.Mana = &f
			}
			if v, ok := e.GetFloat32("m_flMaxMana"); ok {
				f := float64(v)
				hs.MaxMana = &f
			}
			if v, ok := e.GetInt32("m_iCurrentLevel"); ok {
				hs.Level = &v
			}
			if v, ok := e.GetInt32("m_iCurrentXP"); ok {
				i := int64(v)
				hs.XP = &i
			}
			if v, ok := e.GetFloat32("m_flRespawnTime"); ok {
				f := float64(v)
				hs.RespawnTime = &f
			}
			if v, ok := e.GetBool("m_bAlive"); ok {
				hs.Alive = &v
			}
			if v, ok := e.GetBool("m_bBuyBackDisabled"); ok {
				b := !v
				hs.BuybackAvailable = &b
			}
			if v, ok := e.GetUint32("m_iNetWorth"); ok {
				hs.Networth = &v
			}
			return w.Write(&raw.Event{Kind: raw.KindHeroState, HeroState: hs})
		}
		if strings.HasPrefix(cls, "CDOTATeam") {
			ts := &raw.TeamState{Tick: p.Tick, TeamIndex: e.GetIndex()}
			if v, ok := e.GetInt32("m_iTeamNum"); ok {
				ts.TeamNum = &v
			}
			ts.Name, _ = e.GetString("m_szTeamname")
			ts.Tag, _ = e.GetString("m_szTag")
			if v, ok := e.GetUint32("m_unTournamentTeamID"); ok {
				ts.TournamentID = &v
			}
			if v, ok := e.GetInt32("m_iScore"); ok {
				i := int64(v)
				ts.Score = &i
			}
			if v, ok := e.GetInt32("m_iTowerKills"); ok {
				i := int64(v)
				ts.TowerKills = &i
			}
			if v, ok := e.GetInt32("m_iBarracksKills"); ok {
				i := int64(v)
				ts.BarracksKills = &i
			}
			return w.Write(&raw.Event{Kind: raw.KindTeamState, TeamState: ts})
		}
		if strings.HasPrefix(cls, "CDOTA_PlayerResource") && op.Flag(manta.EntityOpCreated) {
			return dumpPlayerResource(w, e)
		}
		return nil
	})

	p.Callbacks.OnCDemoFileHeader(func(m *dota.CDemoFileHeader) error {
		return w.Write(&raw.Event{Kind: raw.KindHeader, Header: &raw.Header{ServerName: m.GetServerName()}})
	})
	p.Callbacks.OnCDemoFileInfo(func(m *dota.CDemoFileInfo) error {
		fi := &raw.FileInfo{}
		if d := m.GetGameInfo().GetDota(); d != nil {
			fi.MatchID = d.GetMatchId()
			fi.GameMode = d.GetGameMode()
			fi.GameWinner = d.GetGameWinner()
			fi.LeagueID = d.GetLeagueid()
			fi.RadiantTeamID = d.GetRadiantTeamId()
			fi.DireTeamID = d.GetDireTeamId()
			fi.RadiantTag = d.GetRadiantTeamTag()
			fi.DireTag = d.GetDireTeamTag()
			fi.EndTime = d.GetEndTime()
			fi.PicksBans = int32(len(d.GetPicksBans()))
			for _, pi := range d.GetPlayerInfo() {
				fi.Players = append(fi.Players, raw.PlayerInfo{
					HeroName:     pi.GetHeroName(),
					PlayerName:   pi.GetPlayerName(),
					IsFakeClient: pi.GetIsFakeClient(),
					SteamID:      pi.GetSteamid(),
					GameTeam:     pi.GetGameTeam(),
				})
			}
		}
		fi.PlaybackTicks = m.GetPlaybackTicks()
		fi.PlaybackTime = m.GetPlaybackTime()
		return w.Write(&raw.Event{Kind: raw.KindFileInfo, FileInfo: fi})
	})
	p.Callbacks.OnCSVCMsg_ServerInfo(func(m *dota.CSVCMsg_ServerInfo) error {
		// The game build is extracted by manta from the game dir; the engine
		// event carries the raw server info for provenance.
		return w.Write(&raw.Event{Kind: raw.KindEngine, Engine: &raw.Engine{
			GameDir: m.GetGameDir(),
			MapName: m.GetMapName(),
		}})
	})
	p.Callbacks.OnCNETMsg_Tick(func(m *dota.CNETMsg_Tick) error { return nil })

	p.Callbacks.OnCMsgDOTACombatLogEntry(func(m *dota.CMsgDOTACombatLogEntry) error {
		combatSeq++
		combatTotal++
		e := &raw.Combat{
			Seq:    combatSeq,
			Type:   dota.DOTA_COMBATLOG_TYPES_name[int32(m.GetType())],
			TypeID: int32(m.GetType()),
			TS:     float64(m.GetTimestamp()),
			TSRaw:  float64(m.GetTimestampRaw()),
			Tick:   p.Tick,
		}
		if m.GetTimestamp() > float32(maxCombatTS) {
			maxCombatTS = float64(m.GetTimestamp())
		}
		if m.AttackerName != nil {
			u := *m.AttackerName
			e.AttackerIdx = &u
			e.AttackerName = lookup(u)
		}
		if m.TargetName != nil {
			u := *m.TargetName
			e.TargetIdx = &u
			e.TargetName = lookup(u)
		}
		if m.InflictorName != nil {
			u := *m.InflictorName
			e.InflictorIdx = &u
			e.InflictorName = lookup(u)
		}
		if m.IsAttackerHero != nil {
			v := *m.IsAttackerHero
			e.IsAttackerHero = &v
		}
		if m.IsTargetHero != nil {
			v := *m.IsTargetHero
			e.IsTargetHero = &v
		}
		if m.IsTargetBuilding != nil {
			v := *m.IsTargetBuilding
			e.IsTargetBuilding = &v
		}
		if m.Value != nil {
			v := int64(*m.Value)
			e.Value = &v
		}
		if m.Health != nil {
			v := int64(*m.Health)
			e.Health = &v
		}
		if m.GoldReason != nil {
			v := *m.GoldReason
			e.GoldReason = &v
		}
		if m.XpReason != nil {
			v := *m.XpReason
			e.XpReason = &v
		}
		if m.LastHits != nil {
			v := *m.LastHits
			e.LastHits = &v
		}
		if m.AttackerTeam != nil {
			v := *m.AttackerTeam
			e.AttackerTeam = &v
		}
		if m.TargetTeam != nil {
			v := *m.TargetTeam
			e.TargetTeam = &v
		}
		e.AssistPlayers = m.GetAssistPlayers()
		if m.Networth != nil {
			v := *m.Networth
			e.Networth = &v
		}
		if m.BuildingType != nil {
			v := *m.BuildingType
			e.BuildingType = &v
		}
		if m.ObsWardsPlaced != nil {
			v := *m.ObsWardsPlaced
			e.ObsWardsPlaced = &v
		}
		if m.RuneType != nil {
			v := *m.RuneType
			e.RuneType = &v
		}
		if m.DamageType != nil {
			v := *m.DamageType
			e.DamageType = &v
		}
		if m.IsHealSave != nil {
			v := *m.IsHealSave
			e.IsHealSave = &v
		}
		if m.IsUltimateAbility != nil {
			v := *m.IsUltimateAbility
			e.IsUltimate = &v
		}
		if m.LocationX != nil {
			v := float64(*m.LocationX)
			e.LocationX = &v
		}
		if m.LocationY != nil {
			v := float64(*m.LocationY)
			e.LocationY = &v
		}
		if m.ModifierDuration != nil {
			v := float64(*m.ModifierDuration)
			e.ModifierDuration = &v
		}
		if m.AbilityLevel != nil {
			v := *m.AbilityLevel
			e.AbilityLevel = &v
		}
		if m.IsAbilityToggleOn != nil {
			v := *m.IsAbilityToggleOn
			e.IsAbilityToggleOn = &v
		}
		if m.GetType() == dota.DOTA_COMBATLOG_TYPES_DOTA_COMBATLOG_GAME_STATE {
			if err := w.Write(&raw.Event{Kind: raw.KindGameState, GameState: &raw.GameState{
				CombatTS: float64(m.GetTimestamp()),
				Tick:     p.Tick,
				State:    int64(m.GetValue()),
			}}); err != nil {
				return err
			}
		}
		return w.Write(&raw.Event{Kind: raw.KindCombat, Combat: e})
	})

	// Peak heap sampler.
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

	t0 := time.Now()
	perr := p.Start()
	elapsed := time.Since(t0)
	close(stop)
	<-stopped

	res := &Result{
		LastTick:    p.Tick,
		LastNetTick: p.NetTick,
		GameBuild:   p.GameBuild,
		CombatTotal: combatTotal,
		Events:      w.EventsWritten(),
		BytesRead:   bytesTotal,
		ElapsedSec:  elapsed.Seconds(),
		Outcome:     "ok",
	}
	if peak > 0 {
		res.PeakHeapMiB = atomic.LoadUint64(&peak) / (1 << 20)
	} else {
		var s runtime.MemStats
		runtime.ReadMemStats(&s)
		res.PeakHeapMiB = s.Alloc / (1 << 20)
	}

	if perr != nil {
		res.Outcome = "parse_error"
		res.Error = perr.Error()
		_ = w.Write(&raw.Event{Kind: raw.KindParseFailed, ParseFailed: &raw.ParseFailed{Error: perr.Error()}})
		return res, fmt.Errorf("replay/parser: %w", perr)
	}

	_ = w.Write(&raw.Event{Kind: raw.KindParseDone, ParseDone: &raw.ParseDone{
		LastTick:      p.Tick,
		LastNetTick:   p.NetTick,
		GameBuild:     p.GameBuild,
		MessageCounts: counts,
		CombatTotal:   combatTotal,
		BytesRead:     bytesTotal,
		ElapsedSec:    0,
		Outcome:       "ok",
	}})
	return res, nil
}

// ParseFile parses a decompressed .dem file into raw events.
func ParseFile(path string, w *raw.Writer) (*Result, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ParseStream(f, w, info.Size())
}

// dumpPlayerResource emits the player-resource binding slots.
func dumpPlayerResource(w *raw.Writer, e *manta.Entity) error {
	for slot := int32(0); slot < 64; slot++ {
		prefix := fmt.Sprintf("m_vecPlayerData.%04d", slot)
		pr := &raw.PlayerResource{Slot: slot}
		found := false
		if v, ok := e.GetUint64(prefix + ".m_iPlayerSteamID"); ok && v != 0 {
			pr.SteamID64 = v
			found = true
		}
		if v, ok := e.GetString(prefix + ".m_iszPlayerName"); ok {
			pr.PlayerName = v
			if v != "" {
				found = true
			}
		}
		if v, ok := e.GetInt32(prefix + ".m_iPlayerTeam"); ok {
			pr.Team = &v
		}
		if v, ok := e.GetInt32(prefix + ".m_nPlayerSlot"); ok {
			pr.PlayerSlot = &v
		}
		if !found {
			continue
		}
		if err := w.Write(&raw.Event{Kind: raw.KindPlayerRes, PlayerRes: pr}); err != nil {
			return err
		}
	}
	return nil
}
