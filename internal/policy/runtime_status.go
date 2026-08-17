package policy

type RuntimeStatus struct {
	CandidateCapacity int
	Healthy           bool
}

// Runtime construction publishes once before the capture HTTP server starts;
// values are immutable for the process lifetime. Current degradation is owned
// by the capture tracker's synchronized health surface.
var runtimeStatuses = map[string]RuntimeStatus{}

func publishRuntimeStatus(sessionID string, candidateCapacity int, healthy bool) {
	if sessionID == "" {
		return
	}
	runtimeStatuses[sessionID] = RuntimeStatus{CandidateCapacity: candidateCapacity, Healthy: healthy}
}

func RuntimeStatusForSession(sessionID string) (RuntimeStatus, bool) {
	value, ok := runtimeStatuses[sessionID]
	return value, ok
}
