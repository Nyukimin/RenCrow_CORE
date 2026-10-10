package llm

// ByteRange is a half-open byte range in the original UTF-8 source.
type ByteRange struct {
	Start uint64 `json:"start"`
	End   uint64 `json:"end"`
}

// PromptSourceRef identifies the exact original bytes behind a prompt block.
// It is internal CORE metadata and is not part of the LLM provider payload.
type PromptSourceRef struct {
	Owner             string    `json:"owner"`
	SourceID          string    `json:"source_id"`
	RawHash           string    `json:"raw_hash"`
	ProjectionVersion string    `json:"projection_version"`
	Range             ByteRange `json:"range"`
	Origin            string    `json:"origin"`
	Sequence          uint64    `json:"sequence"`
}

// ClonePromptSourceRef returns an owned copy so prompt assembly cannot mutate
// the source reference held by its caller.
func ClonePromptSourceRef(source *PromptSourceRef) *PromptSourceRef {
	if source == nil {
		return nil
	}
	cloned := *source
	return &cloned
}
