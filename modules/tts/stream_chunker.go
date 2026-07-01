package tts

import "strings"

type StreamChunker struct {
	pending strings.Builder
	emitted bool
}

func (c *StreamChunker) AcceptToken(token string) []string {
	if c == nil || token == "" {
		return nil
	}
	c.pending.WriteString(token)
	chunks := make([]string, 0, 1)
	for {
		chunk, rest, ok := NextTTSChunk(c.pending.String(), false)
		if !ok {
			return chunks
		}
		c.pending.Reset()
		c.pending.WriteString(rest)
		chunks = append(chunks, chunk)
		c.emitted = true
	}
}

func (c *StreamChunker) FinalizeAll(finalText string) []string {
	if c == nil {
		return nil
	}
	if c.emitted {
		chunks := SplitTTSChunks(c.pending.String())
		c.pending.Reset()
		if len(chunks) > 0 {
			c.emitted = true
		}
		return chunks
	}
	c.pending.Reset()
	chunks := SplitTTSChunks(finalText)
	if len(chunks) > 0 {
		c.emitted = true
	}
	return chunks
}

func (c *StreamChunker) FinalizeOne(finalText string) []string {
	if c == nil {
		return nil
	}
	if c.emitted {
		chunk, _, ok := NextTTSChunk(c.pending.String(), true)
		c.pending.Reset()
		if !ok {
			return nil
		}
		c.emitted = true
		return []string{chunk}
	}
	c.pending.Reset()
	if strings.TrimSpace(finalText) == "" {
		return nil
	}
	c.emitted = true
	return []string{finalText}
}

func (c *StreamChunker) Emitted() bool {
	return c != nil && c.emitted
}
