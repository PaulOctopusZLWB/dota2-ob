package history

import "time"

// Window names the three distinct historical observation periods M1 keeps
// separate. The engine must never silently mix them.
type Window string

const (
	WindowCurrentPatch Window = "current_patch"
	WindowTrailing90   Window = "trailing_90"
	WindowTrailing180  Window = "trailing_180"
)

// PatchWindow pins the initial accepted gameplay patch. A later patch ID or
// build is a separate window and cannot be mixed silently.
type PatchWindow struct {
	PatchID   string // OpenDota patch id, e.g. "60"
	DotaPatch string // human patch, e.g. "7.41"
}

// CutoffWindow is the immutable window definition derived only from the
// tournament cutoff. It is a pure value; time is supplied by the caller so
// there is no ambient clock inside the history domain.
type CutoffWindow struct {
	Patch        PatchWindow
	HistoryCutoff time.Time
	CurrentPatchStart time.Time
	Trailing90Start   time.Time
	Trailing180Start  time.Time
}

// NewCutoffWindow builds the three window boundaries from the frozen cutoff.
// current_patch starts at the patch release time (caller supplies it); the
// trailing windows are exact 90/180-day offsets. All boundaries are
// half-open: an event whose time t satisfies start <= t <= cutoff belongs to
// the window starting at start.
func NewCutoffWindow(cutoff time.Time, patch PatchWindow, patchRelease time.Time) CutoffWindow {
	return CutoffWindow{
		Patch:             patch,
		HistoryCutoff:     cutoff,
		CurrentPatchStart: patchRelease,
		Trailing90Start:   cutoff.AddDate(0, 0, -90),
		Trailing180Start:  cutoff.AddDate(0, 0, -180),
	}
}

// Includes reports whether an event time is eligible for a window. An event
// at exactly the cutoff is included; an event after the cutoff is rejected
// (late-fact rejection). Zero event times are never eligible.
func (w CutoffWindow) Includes(window Window, eventTime time.Time) bool {
	if eventTime.IsZero() || eventTime.After(w.HistoryCutoff) {
		return false
	}
	switch window {
	case WindowCurrentPatch:
		// Current patch starts at the patch release; treat prerelease events
		// as not current-patch rather than fabricating a date.
		if w.CurrentPatchStart.IsZero() {
			return false
		}
		return !eventTime.Before(w.CurrentPatchStart) && !eventTime.After(w.HistoryCutoff)
	case WindowTrailing90:
		return !eventTime.Before(w.Trailing90Start) && !eventTime.After(w.HistoryCutoff)
	case WindowTrailing180:
		return !eventTime.Before(w.Trailing180Start) && !eventTime.After(w.HistoryCutoff)
	}
	return false
}

// AllWindows is the canonical ordering used when emitting per-window baselines.
func AllWindows() []Window {
	return []Window{WindowCurrentPatch, WindowTrailing90, WindowTrailing180}
}