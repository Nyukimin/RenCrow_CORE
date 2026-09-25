package browsertrace

import (
	"encoding/json"
	"fmt"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// apiCoverageReportBody is the single declaration of what the APICoverageReport
// content digest covers. The report holds no single content string, so its content
// is the four body lists encoded as compact JSON in this fixed key order
// (IDENTITY_CANONICAL Step13 follow-up). Identity fields (artifact_id,
// artifact_kind, task_id, run_id, actor_id), created_at, content_hash itself and
// superseded_by are excluded by construction: reminting an ArtifactID or appending
// a supersession later never changes the digest, and two artifacts holding the same
// body share one digest. A new ArtifactID is never reused as a hash.
//
// The fields carry no omitempty tag so every key is always present in the digested
// bytes. encoding/json writes a nil slice as null and an empty slice as [], and the
// contract is that a missing list and an empty list are the same content, so
// APICoverageReportBodyBytes normalizes nil to an empty list here rather than leaving
// it to the encoder. Adding, dropping or reordering an entry changes the bytes, and the
// JSON string encoder fixes the escaping of quotes, newlines and non-ASCII text.
type apiCoverageReportBody struct {
	ObservedFlows         []string `json:"observed_flows"`
	ObservedEndpoints     []string `json:"observed_endpoints"`
	MissingFlows          []string `json:"missing_flows"`
	RecommendedNextTraces []string `json:"recommended_next_traces"`
}

// digestedBody projects the report onto the digested content. It changes no element and
// no order; it only replaces a nil list with an empty one so one content keeps one
// digest whether a trace produced nothing or nothing was recorded for that list.
func (item APICoverageReport) digestedBody() apiCoverageReportBody {
	return apiCoverageReportBody{
		ObservedFlows:         digestedList(item.ObservedFlows),
		ObservedEndpoints:     digestedList(item.ObservedEndpoints),
		MissingFlows:          digestedList(item.MissingFlows),
		RecommendedNextTraces: digestedList(item.RecommendedNextTraces),
	}
}

func digestedList(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}

// APICoverageReportBodyBytes returns the exact bytes that the content digest of an
// APICoverageReport covers. The digest form itself is owned by modules/core.
func APICoverageReportBodyBytes(item APICoverageReport) []byte {
	encoded, err := json.Marshal(item.digestedBody())
	if err != nil {
		// encoding/json only fails for values it cannot encode (invalid map keys,
		// channels, cycles). This body is four string lists, so reaching this branch
		// is a programming error, and falling back to empty bytes would publish a
		// digest of bytes that were never persisted instead of the real content.
		panic(fmt.Sprintf("encode browsertrace coverage body: %v", err))
	}
	return encoded
}

// ComputeAPICoverageReportContentHash returns the canonical content digest of an
// APICoverageReport body, so a producer never invents the value by hand.
func ComputeAPICoverageReportContentHash(item APICoverageReport) string {
	return modulecore.ContentHashOf(APICoverageReportBodyBytes(item))
}
