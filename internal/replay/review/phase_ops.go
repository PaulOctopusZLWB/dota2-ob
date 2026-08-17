package review

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// PhaseInterval is the canonical phase interval value used by review
// operations and the effective overlay. Only the three official phases are
// valid; reset is an episode/transition reason, never a phase.
type PhaseInterval struct {
	StartGameSecond int    `json:"start_game_second"`
	EndGameSecond   int    `json:"end_game_second"`
	GlobalPhase     string `json:"global_phase"`
	RoundIndex      int    `json:"round_index,omitempty"`
	EventRef        string `json:"event_ref,omitempty"`
}

// PhaseOp is the typed review operation on a phase interval.
type PhaseOp string

const (
	OpAccept  PhaseOp = "accept"  // confirm a machine boundary unchanged
	OpMove    PhaseOp = "move"    // change boundary start/end
	OpRelabel PhaseOp = "relabel" // change only the phase label
	OpAdd     PhaseOp = "add"     // insert a new interval
	OpDelete  PhaseOp = "delete"  // remove an interval
	OpSplit   PhaseOp = "split"   // split one interval at a boundary
	OpMerge   PhaseOp = "merge"   // merge two adjacent intervals
)

// ValidPhases are the only legal phase labels.
var ValidPhases = []string{"laning", "midgame", "decisive"}

// ParseEventRef normalizes a phase interval reference. Accepted forms:
//
//	"interval@123-456", "interval@123", "123-456". Returns (start, end, ok).
func ParseEventRef(ref string) (int, int, bool) {
	s := strings.TrimSpace(ref)
	s = strings.TrimPrefix(s, "interval@")
	if i := strings.IndexByte(s, '-'); i >= 0 {
		st, err1 := strconv.Atoi(strings.TrimSpace(s[:i]))
		en, err2 := strconv.Atoi(strings.TrimSpace(s[i+1:]))
		if err1 == nil && err2 == nil && st >= 0 && en > st {
			return st, en, true
		}
		return 0, 0, false
	}
	st, err := strconv.Atoi(s)
	if err != nil {
		return 0, 0, false
	}
	return st, -1, true // open end; caller resolves against machine intervals
}

// CanonicalEventRef builds the canonical interval reference.
func CanonicalEventRef(iv PhaseInterval) string {
	return fmt.Sprintf("interval@%d-%d", iv.StartGameSecond, iv.EndGameSecond)
}

// PhaseOpReq is the typed phase-review mutation payload.
type PhaseOpReq struct {
	MatchID string  `json:"match_id"`
	Author  string  `json:"author"`
	Reason  string  `json:"reason"`
	Op      PhaseOp `json:"operation"`
	// EventRef is the target machine interval for accept/move/relabel/
	// delete/split, or the merge-left interval for merge.
	EventRef string `json:"event_ref"`
	// Effective is the resulting interval for accept/move/relabel/add.
	Effective *PhaseInterval `json:"effective_value,omitempty"`
	// SplitSecond is the boundary second for split.
	SplitSecond *int `json:"split_second,omitempty"`
	// MergeRight is the second interval to merge for merge.
	MergeRight string `json:"merge_right,omitempty"`
	// EligibleSeconds bounds validation (match end).
	EligibleSeconds int `json:"eligible_seconds,omitempty"`
}

// ValidatePhaseLabel checks a phase label against the official set.
func ValidatePhaseLabel(p string) error {
	for _, v := range ValidPhases {
		if p == v {
			return nil
		}
	}
	return fmt.Errorf("invalid_phase_label:%s (want laning|midgame|decisive)", p)
}

// ValidateIntervalShape checks bounds and shape of an effective interval.
func ValidateIntervalShape(iv *PhaseInterval, eligible int) error {
	if iv == nil {
		return fmt.Errorf("effective_interval_required")
	}
	if err := ValidatePhaseLabel(iv.GlobalPhase); err != nil {
		return err
	}
	if iv.StartGameSecond < 0 || iv.EndGameSecond <= iv.StartGameSecond {
		return fmt.Errorf("invalid_interval_bounds:%d-%d", iv.StartGameSecond, iv.EndGameSecond)
	}
	if eligible > 0 && iv.EndGameSecond > eligible {
		return fmt.Errorf("interval_exceeds_eligible_seconds:%d>%d", iv.EndGameSecond, eligible)
	}
	return nil
}

// PhaseOverlay applies typed operations to a machine interval stream and
// returns the validated effective stream (or an error). Machine intervals are
// never mutated. Overlap/gap/order and official-phase invariants are enforced.
type PhaseOverlay struct {
	Machine []PhaseInterval
}

// FromJSON decodes machine intervals from raw JSON messages.
func FromJSON(raw []json.RawMessage) ([]PhaseInterval, error) {
	out := make([]PhaseInterval, 0, len(raw))
	for _, b := range raw {
		var iv PhaseInterval
		if err := json.Unmarshal(b, &iv); err != nil {
			return nil, fmt.Errorf("decode machine interval: %w", err)
		}
		out = append(out, iv)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartGameSecond < out[j].StartGameSecond })
	return out, nil
}

// ToJSON encodes intervals to raw JSON messages with canonical refs.
func ToJSON(intervals []PhaseInterval) []json.RawMessage {
	out := make([]json.RawMessage, 0, len(intervals))
	for i := range intervals {
		intervals[i].EventRef = CanonicalEventRef(intervals[i])
		b, _ := json.Marshal(intervals[i])
		out = append(out, b)
	}
	return out
}

// Apply runs one typed operation and returns the new effective stream. The
// machine stream is preserved; the returned stream is validated.
func (o *PhaseOverlay) Apply(req PhaseOpReq) ([]PhaseInterval, error) {
	work := append([]PhaseInterval(nil), o.Machine...)
	sort.Slice(work, func(i, j int) bool { return work[i].StartGameSecond < work[j].StartGameSecond })
	eligible := req.EligibleSeconds
	if eligible <= 0 {
		for _, iv := range work {
			if iv.EndGameSecond > eligible {
				eligible = iv.EndGameSecond
			}
		}
	}
	switch req.Op {
	case OpAccept:
		// Confirm a machine boundary; effective equals machine (no change).
		if req.EventRef == "" {
			return nil, fmt.Errorf("event_ref_required")
		}
		st, _, ok := ParseEventRef(req.EventRef)
		if !ok {
			return nil, fmt.Errorf("invalid_event_ref:%s", req.EventRef)
		}
		if !o.hasInterval(work, st) {
			return nil, fmt.Errorf("machine_interval_not_found:%s", req.EventRef)
		}
		return work, nil
	case OpMove, OpRelabel:
		if req.EventRef == "" || req.Effective == nil {
			return nil, fmt.Errorf("event_ref_and_effective_required")
		}
		if err := ValidateIntervalShape(req.Effective, eligible); err != nil {
			return nil, err
		}
		st, _, ok := ParseEventRef(req.EventRef)
		if !ok {
			return nil, fmt.Errorf("invalid_event_ref:%s", req.EventRef)
		}
		idx := o.indexOf(work, st)
		if idx < 0 {
			return nil, fmt.Errorf("machine_interval_not_found:%s", req.EventRef)
		}
		replaced := *req.Effective
		replaced.EventRef = CanonicalEventRef(replaced)
		work[idx] = replaced
	case OpAdd:
		if req.Effective == nil {
			return nil, fmt.Errorf("effective_interval_required")
		}
		if err := ValidateIntervalShape(req.Effective, eligible); err != nil {
			return nil, err
		}
		work = append(work, *req.Effective)
	case OpDelete:
		if req.EventRef == "" {
			return nil, fmt.Errorf("event_ref_required")
		}
		st, _, ok := ParseEventRef(req.EventRef)
		if !ok {
			return nil, fmt.Errorf("invalid_event_ref:%s", req.EventRef)
		}
		idx := o.indexOf(work, st)
		if idx < 0 {
			return nil, fmt.Errorf("machine_interval_not_found:%s", req.EventRef)
		}
		work = append(work[:idx], work[idx+1:]...)
	case OpSplit:
		if req.EventRef == "" || req.SplitSecond == nil {
			return nil, fmt.Errorf("event_ref_and_split_second_required")
		}
		st, _, ok := ParseEventRef(req.EventRef)
		if !ok {
			return nil, fmt.Errorf("invalid_event_ref:%s", req.EventRef)
		}
		idx := o.indexOf(work, st)
		if idx < 0 {
			return nil, fmt.Errorf("machine_interval_not_found:%s", req.EventRef)
		}
		s := *req.SplitSecond
		if s <= work[idx].StartGameSecond || s >= work[idx].EndGameSecond {
			return nil, fmt.Errorf("invalid_split_second:%d outside %d-%d", s, work[idx].StartGameSecond, work[idx].EndGameSecond)
		}
		left := work[idx]
		right := work[idx]
		left.EndGameSecond = s
		right.StartGameSecond = s
		right.RoundIndex = work[idx].RoundIndex
		work = append(work[:idx], append([]PhaseInterval{left, right}, work[idx+1:]...)...)
	case OpMerge:
		if req.EventRef == "" || req.MergeRight == "" {
			return nil, fmt.Errorf("event_ref_and_merge_right_required")
		}
		ls, _, ok := ParseEventRef(req.EventRef)
		if !ok {
			return nil, fmt.Errorf("invalid_event_ref:%s", req.EventRef)
		}
		rs, _, ok2 := ParseEventRef(req.MergeRight)
		if !ok2 {
			return nil, fmt.Errorf("invalid_merge_right:%s", req.MergeRight)
		}
		li := o.indexOf(work, ls)
		ri := o.indexOf(work, rs)
		if li < 0 || ri < 0 {
			return nil, fmt.Errorf("machine_interval_not_found:%s,%s", req.EventRef, req.MergeRight)
		}
		if li > ri {
			li, ri = ri, li
		}
		// Adjacency required; merge into one interval.
		if work[li].EndGameSecond != work[ri].StartGameSecond {
			return nil, fmt.Errorf("merge_intervals_not_adjacent:%d-%d != %d-%d",
				work[li].StartGameSecond, work[li].EndGameSecond,
				work[ri].StartGameSecond, work[ri].EndGameSecond)
		}
		if work[li].GlobalPhase != work[ri].GlobalPhase {
			return nil, fmt.Errorf("merge_phase_mismatch:%s vs %s", work[li].GlobalPhase, work[ri].GlobalPhase)
		}
		merged := work[li]
		merged.EndGameSecond = work[ri].EndGameSecond
		work = append(work[:li], append([]PhaseInterval{merged}, work[ri+1:]...)...)
	default:
		return nil, fmt.Errorf("unknown_phase_operation:%s", req.Op)
	}
	if err := validateStream(work); err != nil {
		return nil, err
	}
	return work, nil
}

func (o *PhaseOverlay) hasInterval(intervals []PhaseInterval, start int) bool {
	return o.indexOf(intervals, start) >= 0
}

func (o *PhaseOverlay) indexOf(intervals []PhaseInterval, start int) int {
	for i := range intervals {
		if intervals[i].StartGameSecond == start {
			return i
		}
	}
	return -1
}

// validateStream enforces the official phase stream invariants: ordered,
// non-overlapping, gap-free over [0, eligible], and laning never re-enters
// after exit.
func validateStream(intervals []PhaseInterval) error {
	sort.Slice(intervals, func(i, j int) bool { return intervals[i].StartGameSecond < intervals[j].StartGameSecond })
	prevEnd := 0
	seenLaning := false
	sawLaningExit := false
	for i := range intervals {
		iv := &intervals[i]
		if err := ValidatePhaseLabel(iv.GlobalPhase); err != nil {
			return err
		}
		if iv.StartGameSecond < prevEnd {
			return fmt.Errorf("phase_intervals_overlap:%d-%d", iv.StartGameSecond, iv.EndGameSecond)
		}
		if iv.StartGameSecond > prevEnd {
			return fmt.Errorf("phase_stream_gap:%d before %d", prevEnd, iv.StartGameSecond)
		}
		prevEnd = iv.EndGameSecond
		if iv.GlobalPhase == "laning" {
			if sawLaningExit {
				return fmt.Errorf("laning_reentry_after_exit")
			}
			seenLaning = true
		} else if seenLaning && iv.GlobalPhase != "laning" {
			sawLaningExit = true
		}
	}
	return nil
}
