package llm

func CloneGenerateRequest(req GenerateRequest) GenerateRequest {
	req.Messages = CloneMessages(req.Messages)
	req.ProviderOptions = cloneStringAnyMap(req.ProviderOptions)
	return req
}

func CloneMessages(in []Message) []Message {
	if in == nil {
		return nil
	}
	out := make([]Message, 0, len(in))
	for _, msg := range in {
		msg.Parts = CloneMessageParts(msg.Parts)
		out = append(out, msg)
	}
	return out
}

func CloneMessageParts(in []MessagePart) []MessagePart {
	if in == nil {
		return nil
	}
	out := make([]MessagePart, 0, len(in))
	for _, part := range in {
		if part.Data != nil {
			part.Data = append([]byte(nil), part.Data...)
		}
		out = append(out, part)
	}
	return out
}

func cloneStringAnyMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
