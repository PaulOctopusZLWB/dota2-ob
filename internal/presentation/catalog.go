package presentation

import "github.com/PaulOctopusZLWB/dota2-ob/internal/contracts"

const (
	catalogVersion     = "ti15.broadcast-messages.v1"
	terminologyVersion = "dota.zh-cn.broadcast.v1"
)

var audienceKeys = []string{
	"insight.draft_context",
	"insight.economy_lead",
	"insight.item_timing",
	"insight.lane_checkpoint",
	"insight.live_visible_change",
	"insight.objective_exchange",
	"insight.teamfight_readiness",
}

// CatalogVersion pins the audience-facing message catalog used to construct
// OverlayStateV1 claims.
func CatalogVersion() string { return catalogVersion }

// TerminologyVersion pins the reviewed Dota broadcast terminology table.
func TerminologyVersion() string { return terminologyVersion }

// AudienceKeys returns the canonical, sorted message-key set. The returned
// slice is a copy so callers cannot mutate the catalog.
func AudienceKeys() []string { return append([]string(nil), audienceKeys...) }

// HasMessage reports whether a locale contains a template for a canonical
// audience key. zh-CN and the en-US development fallback deliberately have
// identical key sets.
func HasMessage(locale, key string) bool {
	if locale != "zh-CN" && locale != "en-US" {
		return false
	}
	for _, candidate := range audienceKeys {
		if candidate == key {
			return true
		}
	}
	return false
}

// ResolveTerm resolves a stable language-neutral identifier to reviewed
// broadcast text. Official player handles are display identifiers and are
// preserved byte-for-byte; the final OverlayStateV1 validator still rejects
// unsafe text.
func ResolveTerm(locale, kind, key string) (string, bool) {
	if locale != "zh-CN" {
		locale = "en-US"
	}
	if kind == "player_handle" {
		return key, key != ""
	}
	zh, en, ok := term(kind, key)
	if !ok {
		return "", false
	}
	if locale == "zh-CN" {
		return zh, true
	}
	return en, true
}

func term(kind, key string) (string, string, bool) {
	switch kind {
	case "team":
		switch key {
		case "radiant":
			return "天辉", "Radiant", true
		case "dire":
			return "夜魇", "Dire", true
		}
	case "role":
		switch key {
		case "carry":
			return "一号位", "Carry", true
		case "mid":
			return "中单", "Mid", true
		case "offlane":
			return "三号位", "Offlane", true
		case "support":
			return "四号位", "Support", true
		case "hard_support":
			return "五号位", "Hard Support", true
		}
	case "hero":
		switch key {
		case "npc_dota_hero_axe":
			return "斧王", "Axe", true
		case "npc_dota_hero_antimage":
			return "敌法师", "Anti-Mage", true
		case "npc_dota_hero_crystal_maiden":
			return "水晶室女", "Crystal Maiden", true
		}
	case "item":
		switch key {
		case "item_blink":
			return "闪烁匕首", "Blink Dagger", true
		case "item_black_king_bar":
			return "黑皇杖", "Black King Bar", true
		case "item_refresher":
			return "刷新球", "Refresher Orb", true
		}
	case "ability":
		switch key {
		case "axe_berserkers_call":
			return "狂战士之吼", "Berserker's Call", true
		case "crystal_maiden_freezing_field":
			return "极寒领域", "Freezing Field", true
		}
	case "metric":
		switch key {
		case "net_worth":
			return "经济", "Net Worth", true
		case "level":
			return "等级", "Level", true
		case "item_timing":
			return "关键装备时间", "Key Item Timing", true
		}
	case "objective":
		switch key {
		case "roshan":
			return "肉山", "Roshan", true
		case "tormentor":
			return "魔方", "Tormentor", true
		case "tower":
			return "防御塔", "Tower", true
		}
	}
	return "", "", false
}

type renderValues struct {
	player, hero, role, team, item, objective string
	netWorthLead, checkpointMinute            string
	netWorthDelta, timingDelta, readyCount    string
	radiantNetWorthDelta, direNetWorthDelta   string
}

func render(locale string, candidate contracts.InsightCandidateV1) (contracts.OverlayClaimV1, error) {
	if locale != "zh-CN" {
		locale = "en-US"
	}
	values, err := readParameters(locale, candidate)
	if err != nil {
		return contracts.OverlayClaimV1{}, err
	}
	zh := locale == "zh-CN"
	switch candidate.LocalizationKey {
	case "insight.draft_context":
		if zh {
			return contracts.OverlayClaimV1{Title: values.player + " 的" + values.hero, Body: values.role + "英雄池样本为 " + unsigned(candidate.SampleSize) + " 场；仅作当前版本选人背景。", AssetKey: "draft"}, nil
		}
		return contracts.OverlayClaimV1{Title: values.player + " on " + values.hero, Body: values.role + " sample: " + unsigned(candidate.SampleSize) + " matches on the current patch.", AssetKey: "draft"}, nil
	case "insight.economy_lead":
		if zh {
			return contracts.OverlayClaimV1{Title: values.team + "建立经济领先", Body: "当前已观测经济领先 " + values.netWorthLead + "；领先不等同于胜势。", AssetKey: "economy"}, nil
		}
		return contracts.OverlayClaimV1{Title: values.team + " builds a net-worth lead", Body: "Observed lead: " + values.netWorthLead + "; a lead is not a win-probability claim.", AssetKey: "economy"}, nil
	case "insight.item_timing":
		if zh {
			return contracts.OverlayClaimV1{Title: values.player + " 的" + values.item, Body: "关键装备比基准提前 " + values.timingDelta + " 秒，下一轮资源交换值得关注。", AssetKey: "item"}, nil
		}
		return contracts.OverlayClaimV1{Title: values.player + "'s " + values.item, Body: "The key item arrived " + values.timingDelta + " seconds ahead of baseline.", AssetKey: "item"}, nil
	case "insight.lane_checkpoint":
		if zh {
			return contracts.OverlayClaimV1{Title: values.team + chineseMinute(values.checkpointMinute) + "分钟对线检查点", Body: "相对可比基准偏差 " + values.netWorthDelta + " 经济；结论仅覆盖已观测状态。", AssetKey: "lane"}, nil
		}
		return contracts.OverlayClaimV1{Title: values.team + " " + values.checkpointMinute + "-minute lane checkpoint", Body: "Net-worth delta versus the comparable baseline: " + values.netWorthDelta + ".", AssetKey: "lane"}, nil
	case "insight.objective_exchange":
		if zh {
			return contracts.OverlayClaimV1{Title: values.team + "拿下" + values.objective, Body: "本轮可见资源交换净变化 " + values.netWorthDelta + " 经济。", AssetKey: "objective"}, nil
		}
		return contracts.OverlayClaimV1{Title: values.team + " secures " + values.objective, Body: "Observed net change across this objective exchange: " + values.netWorthDelta + ".", AssetKey: "objective"}, nil
	case "insight.live_visible_change":
		if zh {
			return contracts.OverlayClaimV1{Title: "可见比赛状态发生变化", Body: "GSI 可见经济变化：天辉 " + values.radiantNetWorthDelta + "，夜魇 " + values.direNetWorthDelta + "；不归因目标或所有者。", AssetKey: "economy"}, nil
		}
		return contracts.OverlayClaimV1{Title: "Visible match state changed", Body: "GSI-visible net-worth changes: Radiant " + values.radiantNetWorthDelta + ", Dire " + values.direNetWorthDelta + "; no objective or owner is attributed.", AssetKey: "economy"}, nil
	case "insight.teamfight_readiness":
		if zh {
			return contracts.OverlayClaimV1{Title: values.team + "团战资源就绪", Body: values.readyCount + " 名英雄的可见关键资源已就绪；不推断战争迷雾信息。", AssetKey: "teamfight"}, nil
		}
		return contracts.OverlayClaimV1{Title: values.team + " teamfight resources ready", Body: values.readyCount + " heroes have visible key resources ready; no fogged state is inferred.", AssetKey: "teamfight"}, nil
	}
	return contracts.OverlayClaimV1{}, presentationError("unknown_localization_key")
}

func chineseMinute(value string) string {
	switch value {
	case "10":
		return "十"
	case "15":
		return "十五"
	default:
		return value
	}
}

func readParameters(locale string, candidate contracts.InsightCandidateV1) (renderValues, error) {
	var values renderValues
	want := parameterSpec(candidate.LocalizationKey)
	if want == nil || len(candidate.Parameters) != len(want) {
		return values, presentationError("incompatible_parameters")
	}
	for _, spec := range want {
		value, ok := parameterValue(candidate.Parameters, spec.name, spec.kind)
		if !ok {
			return values, presentationError("incompatible_parameters")
		}
		if spec.kind != "decimal" {
			value, ok = ResolveTerm(locale, spec.kind, value)
			if !ok {
				return values, presentationError("unknown_terminology")
			}
		}
		switch spec.name {
		case "player":
			values.player = value
		case "hero":
			values.hero = value
		case "role":
			values.role = value
		case "team":
			values.team = value
		case "item":
			values.item = value
		case "objective":
			values.objective = value
		case "net_worth_lead":
			values.netWorthLead = value
		case "checkpoint_minute":
			values.checkpointMinute = value
		case "net_worth_delta":
			values.netWorthDelta = value
		case "timing_delta_seconds":
			values.timingDelta = value
		case "ready_count":
			values.readyCount = value
		case "radiant_net_worth_delta":
			values.radiantNetWorthDelta = value
		case "dire_net_worth_delta":
			values.direNetWorthDelta = value
		}
	}
	return values, nil
}

type parameterDefinition struct{ name, kind string }

func parameterSpec(key string) []parameterDefinition {
	switch key {
	case "insight.draft_context":
		return []parameterDefinition{{"player", "player_handle"}, {"hero", "hero"}, {"role", "role"}}
	case "insight.economy_lead":
		return []parameterDefinition{{"team", "team"}, {"net_worth_lead", "decimal"}}
	case "insight.item_timing":
		return []parameterDefinition{{"player", "player_handle"}, {"item", "item"}, {"timing_delta_seconds", "decimal"}}
	case "insight.lane_checkpoint":
		return []parameterDefinition{{"team", "team"}, {"checkpoint_minute", "decimal"}, {"net_worth_delta", "decimal"}}
	case "insight.objective_exchange":
		return []parameterDefinition{{"team", "team"}, {"objective", "objective"}, {"net_worth_delta", "decimal"}}
	case "insight.live_visible_change":
		return []parameterDefinition{{"radiant_net_worth_delta", "decimal"}, {"dire_net_worth_delta", "decimal"}}
	case "insight.teamfight_readiness":
		return []parameterDefinition{{"team", "team"}, {"ready_count", "decimal"}}
	}
	return nil
}

func parameterValue(parameters []contracts.TypedParameterV1, name, kind string) (string, bool) {
	for _, parameter := range parameters {
		if parameter.Name != name {
			continue
		}
		if parameter.Type != kind {
			return "", false
		}
		if kind == "decimal" {
			if parameter.DecimalValue == nil || parameter.StringValue != nil || parameter.BooleanValue != nil || !parameter.DecimalValue.Valid() {
				return "", false
			}
			return string(*parameter.DecimalValue), true
		}
		if parameter.StringValue == nil || parameter.DecimalValue != nil || parameter.BooleanValue != nil {
			return "", false
		}
		return *parameter.StringValue, true
	}
	return "", false
}

func unsigned(value uint64) string {
	if value == 0 {
		return "0"
	}
	var buffer [20]byte
	index := len(buffer)
	for value > 0 {
		index--
		buffer[index] = byte('0' + value%10)
		value /= 10
	}
	return string(buffer[index:])
}
