package superagent

import (
	"encoding/json"
	"fmt"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// contextPackBody is the single declaration of what the ContextPack content digest
// covers. The pack holds no single content string, so its content is the summary,
// the included sources and the token estimate of the stored context, encoded as
// compact JSON in this fixed key order (IDENTITY_CANONICAL Step13 follow-up). The
// token estimate belongs to the body because the producer writes it together with
// the summary from the same request context, so it is stored content and not
// identity metadata. Identity fields (artifact_id, artifact_kind, task_id, run_id,
// workstream_id), created_at, content_hash itself and superseded_by are excluded by
// construction: reminting an ArtifactID or appending a supersession later never
// changes the digest, and two packs holding the same body share one digest. A new
// ArtifactID is never reused as a hash.
//
// The fields carry no omitempty tag so every key is always present in the digested
// bytes. encoding/json writes a nil slice as null and an empty slice as [], and the
// contract is that a missing list and an empty list are the same content, so
// ContextPackBodyBytes normalizes nil to an empty list here rather than leaving it
// to the encoder. Adding, dropping or reordering a source changes the bytes, and the
// JSON string encoder fixes the escaping of quotes, newlines and non-ASCII text.
type contextPackBody struct {
	Summary         string   `json:"summary"`
	IncludedSources []string `json:"included_sources"`
	TokenEstimate   int      `json:"token_estimate"`
}

// digestedBody projects the pack onto the digested content. It changes no element and
// no order; it only replaces a nil source list with an empty one so one content keeps
// one digest whether a run collected nothing or nothing was recorded for that list.
func (item ContextPack) digestedBody() contextPackBody {
	sources := item.IncludedSources
	if sources == nil {
		sources = []string{}
	}
	return contextPackBody{
		Summary:         item.Summary,
		IncludedSources: sources,
		TokenEstimate:   item.TokenEstimate,
	}
}

// ContextPackBodyBytes returns the exact bytes that the content digest of a
// ContextPack covers. The digest form itself is owned by modules/core.
func ContextPackBodyBytes(item ContextPack) []byte {
	encoded, err := json.Marshal(item.digestedBody())
	if err != nil {
		// encoding/json only fails for values it cannot encode (invalid map keys,
		// channels, cycles). This body is a string, a string list and an int, so
		// reaching this branch is a programming error, and falling back to empty
		// bytes would publish a digest of bytes that were never persisted instead of
		// the real content.
		panic(fmt.Sprintf("encode superagent context pack body: %v", err))
	}
	return encoded
}

// ComputeContextPackContentHash returns the canonical content digest of a ContextPack
// body, so a producer never invents the value by hand.
func ComputeContextPackContentHash(item ContextPack) string {
	return modulecore.ContentHashOf(ContextPackBodyBytes(item))
}
