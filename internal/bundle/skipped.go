package bundle

import (
	"encoding/json"
	"fmt"

	"github.com/johnnycube/archivist/internal/domain"
	"github.com/johnnycube/archivist/internal/provider"
)

// SkippedArrayKind is the RawRecord kind of SkippedArrayRecord.
const SkippedArrayKind = "skipped_array"

type skippedArray struct {
	Member string `json:"member"`
	Key    string `json:"key"`
	Count  int    `json:"count"`
}

// SkippedArrayRecord is the record an extractor emits for an array it matched
// the member of but does not understand (StreamJSONArraysReport). The member is
// part of the payload, so the same array name in two members is two records.
func SkippedArrayRecord(member string, sa SkippedArray) provider.RawRecord {
	payload, _ := json.Marshal(skippedArray{Member: member, Key: sa.Key, Count: sa.Count})
	return provider.RawRecord{Kind: SkippedArrayKind, Payload: payload}
}

// ReportSkipped is the skipped callback for extractors (StreamJSONArray,
// StreamJSONArraysReport): every array they pass over in member is emitted as a
// SkippedArrayRecord, which the provider's Normalize turns into its
// "<source>.unknown" event with SkippedArrayEvent.
func ReportSkipped(member string, emit func(provider.RawRecord) error) func(SkippedArray) error {
	return func(sa SkippedArray) error { return emit(SkippedArrayRecord(member, sa)) }
}

// SkippedArrayEvent normalizes a SkippedArrayRecord into the source's
// "<source>.unknown" event — the same type unmatched members are captured as —
// so the data the extractor passed over is visible rather than silently
// dropped. (The elements themselves stay in the preserved original archive.)
func SkippedArrayEvent(sourceID string, raw provider.RawRecord) (domain.Event, error) {
	var sa skippedArray
	if err := json.Unmarshal(raw.Payload, &sa); err != nil {
		return domain.Event{}, err
	}
	return domain.Event{
		SourceID:   sourceID,
		ExternalID: sourceID + ":unknown:" + sa.Member + "#" + sa.Key,
		Type:       sourceID + ".unknown",
		Category:   domain.CategoryOther,
		Title:      fmt.Sprintf("Unmapped export data: %s (%q, %d records)", sa.Member, sa.Key, sa.Count),
		Metadata:   map[string]any{"member": sa.Member, "key": sa.Key, "count": sa.Count},
	}, nil
}
