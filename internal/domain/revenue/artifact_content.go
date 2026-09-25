package revenue

import (
	"encoding/json"
	"fmt"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// DailyRoutineReportBody and ChannelDraftBody are the single declaration of what the
// content digest of a revenue artifact covers. Neither artifact holds one content
// string, so its content is a body projection encoded as compact JSON in this fixed key
// order (IDENTITY_CANONICAL Step13 follow-up).
//
// The report body is the reported day, its summary and the counts the producer derived
// from the day's records, plus the suggested actions it wrote at the same time: those
// values are stored content, generated together with the summary by
// BuildDailyRoutineReport, and are never recomputed to fill a gap later. The draft body
// is the channel, the subject and the draft text itself.
//
// Identity (artifact_id, artifact_kind, workstream_id, trace_id, opportunity_id,
// source_artifact_id), lifecycle and policy state (status, external_send_applied),
// created_at, content_hash itself and superseded_by are excluded by construction, so
// reminting an ArtifactID or appending a supersession later never changes the digest and
// two artifacts holding the same body share one digest. Keeping status and
// external_send_applied out of the digest separates content from policy state and does
// not weaken the existing rule that both artifacts must not apply an external send. A
// new ArtifactID is never reused as a hash.
//
// The fields carry no omitempty tag so every key is always present in the digested
// bytes. encoding/json writes a nil slice as null and an empty slice as [], and the
// contract is that a missing list and an empty list are the same content, so the
// projections below normalize nil to an empty list rather than leaving it to the
// encoder. Changing a count, adding an action or reordering the list changes the bytes,
// and the JSON string encoder fixes the escaping of quotes, newlines and non-ASCII text.
type DailyRoutineReportBody struct {
	Date             string   `json:"date"`
	Summary          string   `json:"summary"`
	MarketResearch   int      `json:"market_research_count"`
	SNSPosts         int      `json:"sns_post_count"`
	Products         int      `json:"product_count"`
	CustomerVoices   int      `json:"customer_voice_count"`
	RevenueEvents    int      `json:"revenue_event_count"`
	PaidCustomers    int      `json:"paid_customer_count"`
	BlockedDecisions int      `json:"blocked_decision_count"`
	SuggestedActions []string `json:"suggested_actions"`
}

type ChannelDraftBody struct {
	Channel string `json:"channel"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

// digestedBody projects the report onto its content. It changes no element and no order;
// it only replaces a nil action list with an empty one so one content keeps one digest
// whether the producer wrote nothing or wrote nothing at all for that list.
func (item DailyRoutineReport) digestedBody() DailyRoutineReportBody {
	actions := item.SuggestedActions
	if actions == nil {
		actions = []string{}
	}
	return DailyRoutineReportBody{
		Date:             item.Date,
		Summary:          item.Summary,
		MarketResearch:   item.MarketResearch,
		SNSPosts:         item.SNSPosts,
		Products:         item.Products,
		CustomerVoices:   item.CustomerVoices,
		RevenueEvents:    item.RevenueEvents,
		PaidCustomers:    item.PaidCustomers,
		BlockedDecisions: item.BlockedDecisions,
		SuggestedActions: actions,
	}
}

func (item ChannelDraft) digestedBody() ChannelDraftBody {
	return ChannelDraftBody{Channel: item.Channel, Subject: item.Subject, Body: item.Body}
}

// DailyRoutineReportBodyBytes returns the exact bytes that the content digest of a
// report covers. The digest form itself is owned by modules/core.
func DailyRoutineReportBodyBytes(item DailyRoutineReport) []byte {
	return mustEncodeRevenueBody("revenue daily routine report body", item.digestedBody())
}

// ChannelDraftBodyBytes returns the exact bytes that the content digest of a draft
// covers.
func ChannelDraftBodyBytes(item ChannelDraft) []byte {
	return mustEncodeRevenueBody("revenue channel draft body", item.digestedBody())
}

// ComputeDailyRoutineReportContentHash returns the canonical content digest of a report
// body, so a producer never invents the value by hand.
func ComputeDailyRoutineReportContentHash(item DailyRoutineReport) string {
	return modulecore.ContentHashOf(DailyRoutineReportBodyBytes(item))
}

// ComputeChannelDraftContentHash returns the canonical content digest of a draft body.
func ComputeChannelDraftContentHash(item ChannelDraft) string {
	return modulecore.ContentHashOf(ChannelDraftBodyBytes(item))
}

func mustEncodeRevenueBody(what string, body any) []byte {
	encoded, err := json.Marshal(body)
	if err != nil {
		// encoding/json only fails for values it cannot encode (invalid map keys,
		// channels, cycles). These bodies are strings, ints and a string list, so
		// reaching this branch is a programming error, and falling back to empty bytes
		// would publish a digest of bytes that were never persisted instead of the real
		// content.
		panic(fmt.Sprintf("encode %s: %v", what, err))
	}
	return encoded
}
