package review

import (
	"encoding/json"
	"errors"
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
	OpAccept  PhaseOp = "accept"  // confirm a current boundary unchanged
	OpMove    PhaseOp = "move"    // change one shared boundary, adjust both neighbors
	OpRelabel PhaseOp = "relabel" // change only the phase label
	OpAdd     PhaseOp = "add"     // subdivide/replace an already covered range
	OpDelete  PhaseOp = "delete"  // absorb into an explicitly selected adjacent interval
	OpSplit   PhaseOp = "split"   // split one interval at a boundary
	OpMerge   PhaseOp = "merge"   // merge two adjacent intervals with an explicit label
)

// ValidPhases are the only legal phase labels.
var ValidPhases = []string{"laning", "midgame", "decisive"}

// PhaseOpShapeVersion is the explicit request-shape contract. It is bumped
// whenever an operation's parameter shape changes so that a persisted v2
// correction can be replayed deterministically.
const PhaseOpShapeVersion = "phase-op.v2"

// ParseEventRef normalizes a phase interval reference. Accepted forms:
//
//	"interval@123-456", "interval@123", "123-456". Returns (start, end, ok).
//	An open-end ref ("interval@123") resolves against the current stream
//	(spec requirement: every event_ref resolves against the current stream).
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
	return st, -1, true // open end; caller resolves against the current stream
}

// CanonicalEventRef builds the canonical interval reference.
func CanonicalEventRef(iv PhaseInterval) string {
	return fmt.Sprintf("interval@%d-%d", iv.StartGameSecond, iv.EndGameSecond)
}

// PhaseOpReq is the typed phase-review mutation payload. EventRef and
// merge_right/absorb_into always resolve against the current effective stream
// (never against phases.json once an overlay exists).
type PhaseOpReq struct {
	MatchID string  `json:"match_id"`
	Author  string  `json:"author"`
	Reason  string  `json:"reason"`
	Op      PhaseOp `json:"operation"`
	// ShapeVersion pins the request-shape contract; empty defaults to v2.
	ShapeVersion string `json:"shape_version,omitempty"`
	// EventRef is the target current interval for accept/move/relabel/
	// delete/split, or the merge-left/absorbing interval for merge/delete.
	EventRef string `json:"event_ref"`
	// Effective is the resulting interval for accept/move/relabel/add/merge.
	Effective *PhaseInterval `json:"effective_value,omitempty"`
	// SplitSecond is the boundary second for split.
	SplitSecond *int `json:"split_second,omitempty"`
	// MergeRight is the second adjacent interval to merge for merge.
	MergeRight string `json:"merge_right,omitempty"`
	// AbsorbInto is the explicitly selected adjacent interval that absorbs the
	// deleted interval for delete.
	AbsorbInto string `json:"absorb_into,omitempty"`
	// EvidenceIDs links the correction to supporting fact/episode/phase
	// lineage identifiers.
	EvidenceIDs []string `json:"evidence_ids,omitempty"`
	// EligibleSeconds is the authoritative match-end bound for validation. It
	// is always resolved server-side from phases.json; the client value is
	// ignored in favor of the authoritative one.
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
	if eligible > 0 && (iv.StartGameSecond < 0 || iv.EndGameSecond > eligible) {
		return fmt.Errorf("interval_exceeds_eligible_seconds:%d-%d (eligible %d)", iv.StartGameSecond, iv.EndGameSecond, eligible)
	}
	return nil
}

// ErrStalePrecondition is the sentinel for a failed current-state
// precondition: the operation references a target that no longer exists in
// the current effective stream. The API maps it to 409 (conflict) rather than
// 400 so a stale client never silently succeeds against a moved stream.
var ErrStalePrecondition = errors.New("stale_current_state_precondition")

// IsStale reports whether an Apply error is a stale-precondition failure.
func IsStale(err error) bool { return errors.Is(err, ErrStalePrecondition) }

func staleErrorf(format string, args ...interface{}) error {
	return fmt.Errorf("%w: %s", ErrStalePrecondition, fmt.Sprintf(format, args...))
}

// PhaseOverlay applies typed operations to a base stream (the current
// effective stream when one exists, otherwise the immutable machine stream).
// Machine intervals are never mutated; each operation is one atomic partition
// transform and the resulting stream is validated before it is returned.
type PhaseOverlay struct {
	// Base is the current stream the operation addresses. It is never
	// mutated; Apply always returns a fresh validated copy.
	Base []PhaseInterval
}

// FromJSON decodes intervals from raw JSON messages.
func FromJSON(raw []json.RawMessage) ([]PhaseInterval, error) {
	out := make([]PhaseInterval, 0, len(raw))
	for _, b := range raw {
		var iv PhaseInterval
		if err := json.Unmarshal(b, &iv); err != nil {
			return nil, fmt.Errorf("decode phase interval: %w", err)
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
// base stream is preserved; the returned stream is validated against the
// authoritative eligible_seconds. A reference that cannot be resolved against
// the current stream fails closed with ErrStalePrecondition.
func (o *PhaseOverlay) Apply(req PhaseOpReq) ([]PhaseInterval, error) {
	eligible := req.EligibleSeconds
	if eligible <= 0 {
		return nil, fmt.Errorf("eligible_seconds_required")
	}
	work := append([]PhaseInterval(nil), o.Base...)
	sort.Slice(work, func(i, j int) bool { return work[i].StartGameSecond < work[j].StartGameSecond })
	if len(work) == 0 {
		return nil, fmt.Errorf("empty_phase_stream")
	}
	var err error
	switch req.Op {
	case OpAccept:
		err = o.applyAccept(work, req)
	case OpRelabel:
		err = o.applyRelabel(work, req)
	case OpSplit:
		work, err = o.applySplit(work, req)
	case OpMerge:
		work, err = o.applyMerge(work, req)
	case OpMove:
		err = o.applyMove(work, req)
	case OpAdd:
		work, err = o.applyAdd(work, req)
	case OpDelete:
		work, err = o.applyDelete(work, req)
	default:
		return nil, fmt.Errorf("unknown_phase_operation:%s", req.Op)
	}
	if err != nil {
		return nil, err
	}
	if err := validateStream(work, eligible); err != nil {
		return nil, err
	}
	return work, nil
}

// applyAccept records acceptance of the current target without changing the
// stream. The full stream is still validated.
func (o *PhaseOverlay) applyAccept(work []PhaseInterval, req PhaseOpReq) error {
	idx, _, ok := o.findByRef(work, req.EventRef)
	if !ok {
		return staleErrorf("machine_interval_not_found:%s", req.EventRef)
	}
	_ = idx
	return nil
}

// applyRelabel changes only the selected interval label; boundaries must be
// unchanged.
func (o *PhaseOverlay) applyRelabel(work []PhaseInterval, req PhaseOpReq) error {
	idx, target, ok := o.findByRef(work, req.EventRef)
	if !ok {
		return staleErrorf("machine_interval_not_found:%s", req.EventRef)
	}
	if req.Effective == nil {
		return fmt.Errorf("effective_interval_required")
	}
	if req.Effective.StartGameSecond != target.StartGameSecond || req.Effective.EndGameSecond != target.EndGameSecond {
		return fmt.Errorf("relabel_must_not_change_boundaries:want %d-%d got %d-%d",
			target.StartGameSecond, target.EndGameSecond,
			req.Effective.StartGameSecond, req.Effective.EndGameSecond)
	}
	if err := ValidatePhaseLabel(req.Effective.GlobalPhase); err != nil {
		return err
	}
	target.GlobalPhase = req.Effective.GlobalPhase
	target.EventRef = CanonicalEventRef(target)
	work[idx] = target
	return nil
}

// applySplit divides the selected current interval at the requested second.
func (o *PhaseOverlay) applySplit(work []PhaseInterval, req PhaseOpReq) ([]PhaseInterval, error) {
	idx, target, ok := o.findByRef(work, req.EventRef)
	if !ok {
		return nil, staleErrorf("machine_interval_not_found:%s", req.EventRef)
	}
	if req.SplitSecond == nil {
		return nil, fmt.Errorf("split_second_required")
	}
	s := *req.SplitSecond
	if s <= target.StartGameSecond || s >= target.EndGameSecond {
		return nil, fmt.Errorf("invalid_split_second:%d outside %d-%d", s, target.StartGameSecond, target.EndGameSecond)
	}
	left := target
	right := target
	left.EndGameSecond = s
	right.StartGameSecond = s
	left.EventRef = CanonicalEventRef(left)
	right.EventRef = CanonicalEventRef(right)
	out := append([]PhaseInterval(nil), work[:idx]...)
	out = append(out, left, right)
	out = append(out, work[idx+1:]...)
	return out, nil
}

// applyMerge joins two adjacent current intervals with an explicit,
// deterministic resulting label (the request label, or the left interval's
// label when none is supplied).
func (o *PhaseOverlay) applyMerge(work []PhaseInterval, req PhaseOpReq) ([]PhaseInterval, error) {
	li, l, ok := o.findByRef(work, req.EventRef)
	if !ok {
		return nil, staleErrorf("machine_interval_not_found:%s", req.EventRef)
	}
	ri, r, ok2 := o.findByRef(work, req.MergeRight)
	if !ok2 {
		return nil, staleErrorf("machine_interval_not_found:%s", req.MergeRight)
	}
	if li == ri {
		return nil, fmt.Errorf("merge_requires_two_distinct_intervals")
	}
	if li > ri {
		li, ri = ri, li
		l, r = r, l
	}
	if l.EndGameSecond != r.StartGameSecond {
		return nil, fmt.Errorf("merge_intervals_not_adjacent:%d-%d != %d-%d",
			l.StartGameSecond, l.EndGameSecond, r.StartGameSecond, r.EndGameSecond)
	}
	label := l.GlobalPhase
	if req.Effective != nil && req.Effective.GlobalPhase != "" {
		label = req.Effective.GlobalPhase
	}
	if err := ValidatePhaseLabel(label); err != nil {
		return nil, err
	}
	merged := PhaseInterval{
		StartGameSecond: l.StartGameSecond,
		EndGameSecond:   r.EndGameSecond,
		GlobalPhase:     label,
		RoundIndex:      l.RoundIndex,
		EventRef:        CanonicalEventRef(PhaseInterval{StartGameSecond: l.StartGameSecond, EndGameSecond: r.EndGameSecond}),
	}
	out := append([]PhaseInterval(nil), work[:li]...)
	out = append(out, merged)
	out = append(out, work[ri+1:]...)
	return out, nil
}

// applyMove changes one shared boundary and adjusts both neighboring
// intervals together. The target's other boundary and its label are kept.
func (o *PhaseOverlay) applyMove(work []PhaseInterval, req PhaseOpReq) error {
	idx, target, ok := o.findByRef(work, req.EventRef)
	if !ok {
		return staleErrorf("machine_interval_not_found:%s", req.EventRef)
	}
	if req.Effective == nil {
		return fmt.Errorf("effective_interval_required")
	}
	eff := req.Effective
	if eff.GlobalPhase != "" && eff.GlobalPhase != target.GlobalPhase {
		return fmt.Errorf("move_must_not_relabel:%s vs %s", eff.GlobalPhase, target.GlobalPhase)
	}
	movedStart := eff.StartGameSecond != target.StartGameSecond
	movedEnd := eff.EndGameSecond != target.EndGameSecond
	switch {
	case movedStart && movedEnd:
		return fmt.Errorf("move_must_change_one_boundary_only")
	case movedStart:
		if idx == 0 {
			return fmt.Errorf("move_start_boundary_no_left_neighbor")
		}
		left := &work[idx-1]
		if eff.StartGameSecond <= left.StartGameSecond || eff.StartGameSecond >= target.EndGameSecond {
			return fmt.Errorf("invalid_move_boundary:%d for %d-%d (left %d-%d)",
				eff.StartGameSecond, target.StartGameSecond, target.EndGameSecond, left.StartGameSecond, left.EndGameSecond)
		}
		left.EndGameSecond = eff.StartGameSecond
		left.EventRef = CanonicalEventRef(*left)
		target.StartGameSecond = eff.StartGameSecond
	case movedEnd:
		if idx == len(work)-1 {
			return fmt.Errorf("move_end_boundary_no_right_neighbor")
		}
		right := &work[idx+1]
		if eff.EndGameSecond <= target.StartGameSecond || eff.EndGameSecond >= right.EndGameSecond {
			return fmt.Errorf("invalid_move_boundary:%d for %d-%d (right %d-%d)",
				eff.EndGameSecond, target.StartGameSecond, target.EndGameSecond, right.StartGameSecond, right.EndGameSecond)
		}
		right.StartGameSecond = eff.EndGameSecond
		right.EventRef = CanonicalEventRef(*right)
		target.EndGameSecond = eff.EndGameSecond
	default:
		return fmt.Errorf("move_boundary_unchanged")
	}
	target.EventRef = CanonicalEventRef(target)
	work[idx] = target
	return nil
}

// applyAdd inserts a labelled interval by atomically subdividing/replacing an
// already covered range: the new interval must be contained in a single
// current interval, which is subdivided (or replaced when it exactly matches).
func (o *PhaseOverlay) applyAdd(work []PhaseInterval, req PhaseOpReq) ([]PhaseInterval, error) {
	if req.Effective == nil {
		return nil, fmt.Errorf("effective_interval_required")
	}
	if err := ValidateIntervalShape(req.Effective, req.EligibleSeconds); err != nil {
		return nil, err
	}
	iv := *req.Effective
	covered := -1
	for i := range work {
		if iv.StartGameSecond >= work[i].StartGameSecond && iv.EndGameSecond <= work[i].EndGameSecond {
			covered = i
			break
		}
	}
	if covered < 0 {
		return nil, staleErrorf("add_range_not_covered_by_single_interval:%d-%d", iv.StartGameSecond, iv.EndGameSecond)
	}
	orig := work[covered]
	if iv.StartGameSecond == orig.StartGameSecond && iv.EndGameSecond == orig.EndGameSecond {
		orig.GlobalPhase = iv.GlobalPhase
		orig.EventRef = CanonicalEventRef(orig)
		work[covered] = orig
		return work, nil
	}
	parts := []PhaseInterval{
		{StartGameSecond: orig.StartGameSecond, EndGameSecond: iv.StartGameSecond, GlobalPhase: orig.GlobalPhase, RoundIndex: orig.RoundIndex},
		{StartGameSecond: iv.StartGameSecond, EndGameSecond: iv.EndGameSecond, GlobalPhase: iv.GlobalPhase, RoundIndex: orig.RoundIndex},
		{StartGameSecond: iv.EndGameSecond, EndGameSecond: orig.EndGameSecond, GlobalPhase: orig.GlobalPhase, RoundIndex: orig.RoundIndex},
	}
	kept := make([]PhaseInterval, 0, len(parts))
	for _, p := range parts {
		if p.EndGameSecond > p.StartGameSecond {
			p.EventRef = CanonicalEventRef(p)
			kept = append(kept, p)
		}
	}
	out := append([]PhaseInterval(nil), work[:covered]...)
	out = append(out, kept...)
	out = append(out, work[covered+1:]...)
	return out, nil
}

// applyDelete absorbs the selected interval into an explicitly selected
// adjacent interval rather than leaving uncovered time. The absorbing
// interval keeps its label and expands to cover the deleted range.
func (o *PhaseOverlay) applyDelete(work []PhaseInterval, req PhaseOpReq) ([]PhaseInterval, error) {
	if req.AbsorbInto == "" {
		return nil, fmt.Errorf("absorb_into_required")
	}
	idx, target, ok := o.findByRef(work, req.EventRef)
	if !ok {
		return nil, staleErrorf("machine_interval_not_found:%s", req.EventRef)
	}
	ai, absorber, ok2 := o.findByRef(work, req.AbsorbInto)
	if !ok2 {
		return nil, staleErrorf("machine_interval_not_found:%s", req.AbsorbInto)
	}
	if ai == idx {
		return nil, fmt.Errorf("absorb_into_must_be_adjacent_distinct")
	}
	switch {
	case absorber.EndGameSecond == target.StartGameSecond:
		// Absorber is the immediate left neighbor: it extends to cover the
		// deleted interval, keeping its own label.
		absorber.EndGameSecond = target.EndGameSecond
		absorber.EventRef = CanonicalEventRef(absorber)
		work[ai] = absorber
	case absorber.StartGameSecond == target.EndGameSecond:
		// Absorber is the immediate right neighbor: it extends left to cover
		// the deleted interval, keeping its own label.
		absorber.StartGameSecond = target.StartGameSecond
		absorber.EventRef = CanonicalEventRef(absorber)
		work[ai] = absorber
	default:
		return nil, fmt.Errorf("absorb_into_not_adjacent:%s to %s", req.EventRef, req.AbsorbInto)
	}
	// Remove the deleted interval; if it sat left of the absorber, the
	// absorber index shifts by one after the removal.
	out := append([]PhaseInterval(nil), work[:idx]...)
	out = append(out, work[idx+1:]...)
	return out, nil
}

// findByRef resolves an event_ref (open or closed) against the current stream
// by its start second. Open-end refs resolve to the interval starting at the
// given second.
func (o *PhaseOverlay) findByRef(intervals []PhaseInterval, ref string) (int, PhaseInterval, bool) {
	if ref == "" {
		return -1, PhaseInterval{}, false
	}
	st, en, ok := ParseEventRef(ref)
	if !ok {
		return -1, PhaseInterval{}, false
	}
	for i := range intervals {
		if intervals[i].StartGameSecond == st && (en < 0 || intervals[i].EndGameSecond == en) {
			return i, intervals[i], true
		}
	}
	return -1, PhaseInterval{}, false
}

// validateStream enforces the official phase stream invariants against the
// authoritative eligible_seconds (never inferred from the maximum interval
// end): non-empty, first start exactly 0, final end exactly eligible_seconds,
// positive widths, sorted contiguous gap-free coverage, official phases only,
// and laning never re-enters after exit (midgame <-> decisive re-entry is
// permitted).
func validateStream(intervals []PhaseInterval, eligible int) error {
	if len(intervals) == 0 {
		return fmt.Errorf("phase_stream_empty")
	}
	sort.Slice(intervals, func(i, j int) bool { return intervals[i].StartGameSecond < intervals[j].StartGameSecond })
	if intervals[0].StartGameSecond != 0 {
		return fmt.Errorf("phase_stream_first_start_not_zero:%d", intervals[0].StartGameSecond)
	}
	if last := intervals[len(intervals)-1]; last.EndGameSecond != eligible {
		return fmt.Errorf("phase_stream_final_end_not_eligible:%d != %d", last.EndGameSecond, eligible)
	}
	prevEnd := 0
	seenLaning := false
	sawLaningExit := false
	for i := range intervals {
		iv := &intervals[i]
		if err := ValidatePhaseLabel(iv.GlobalPhase); err != nil {
			return err
		}
		if iv.EndGameSecond <= iv.StartGameSecond {
			return fmt.Errorf("phase_interval_zero_or_negative_width:%d-%d", iv.StartGameSecond, iv.EndGameSecond)
		}
		if i > 0 && iv.StartGameSecond < prevEnd {
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
