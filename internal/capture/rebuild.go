package capture

import (
	"github.com/PaulOctopusZLWB/dota2-ob/internal/analytics"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/session"
)

// AnalyzeSession rebuilds accepted legacy artifacts through the canonical
// LiveObservationV1 path while resolving private fields from the same raw row.
func AnalyzeSession(sessionDir, sessionID string) (analytics.Snapshot, error) {
	return analytics.AnalyzeSessionWithNormalizer(sessionDir, sessionID, func(_ int, r analytics.RebuildRecord) (analytics.NormalizedTick, error) {
		record := &session.Record{SchemaVersion: r.SchemaVersion, SessionID: r.SessionID, Sequence: r.Sequence, ReceivedAt: r.ReceivedAt, Source: r.Source, Payload: r.Payload, Raw: r.Raw}
		o, err := MapLiveObservationV1(record)
		if err != nil {
			return analytics.NormalizedTick{}, err
		}
		return LegacyNormalizedTickV1(o, currentRecordResolver(record))
	})
}
