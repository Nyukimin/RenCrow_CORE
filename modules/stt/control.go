package stt

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
)

const maxUtteranceIDLength = 64

// Control is a decoded WebSocket control message. Every field except Type is
// optional so that clients written before the utterance protocol keep working.
type Control struct {
	Type        string
	UtteranceID string
	SampleRate  int
	Channels    int
	Format      string
}

// ParseControl decodes a JSON control message. A payload that is not a JSON
// object with a non-empty string "type" is audio, not control. A malformed
// optional field is dropped rather than turning the frame into audio.
func ParseControl(payload []byte) (Control, bool) {
	if len(payload) == 0 || payload[0] != '{' {
		return Control{}, false
	}
	var obj map[string]any
	if err := json.Unmarshal(payload, &obj); err != nil {
		return Control{}, false
	}
	controlType, _ := obj["type"].(string)
	controlType = strings.TrimSpace(controlType)
	if controlType == "" {
		return Control{}, false
	}
	utteranceID, _ := obj["utterance_id"].(string)
	format, _ := obj["format"].(string)
	return Control{
		Type:        controlType,
		UtteranceID: sanitizeUtteranceID(utteranceID),
		SampleRate:  intField(obj["sample_rate"]),
		Channels:    intField(obj["channels"]),
		Format:      strings.TrimSpace(format),
	}, true
}

// ParseControlMessage returns only the control type.
func ParseControlMessage(payload []byte) (string, bool) {
	control, ok := ParseControl(payload)
	return control.Type, ok
}

// AudioFormatError is a message when the start control declares audio other
// than 16 kHz mono pcm_s16le, or "" when it is acceptable. Omitted fields are
// accepted: the legacy client announced the same format it always sent.
func (c Control) AudioFormatError() string {
	if c.SampleRate != 0 && c.SampleRate != DefaultSampleRate {
		return fmt.Sprintf("unsupported sample_rate %d: 16000 Hz is required", c.SampleRate)
	}
	if c.Channels != 0 && c.Channels != 1 {
		return fmt.Sprintf("unsupported channels %d: mono is required", c.Channels)
	}
	if c.Format != "" && !strings.EqualFold(c.Format, "pcm_s16le") {
		return fmt.Sprintf("unsupported format %q: pcm_s16le is required", c.Format)
	}
	return ""
}

// IsFinalizeControl reports whether a control type ends the utterance.
// "final_pending" is the legacy name of "stop".
func IsFinalizeControl(controlType string) bool {
	switch strings.TrimSpace(controlType) {
	case "stop", "final_pending":
		return true
	default:
		return false
	}
}

func intField(v any) int {
	if f, ok := v.(float64); ok {
		return int(f)
	}
	return 0
}

// sanitizeUtteranceID keeps a client-chosen id only when it is short printable
// text; otherwise CORE assigns one and reports it in speech_start.
func sanitizeUtteranceID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" || len(id) > maxUtteranceIDLength {
		return ""
	}
	for _, r := range id {
		if unicode.IsControl(r) {
			return ""
		}
	}
	return id
}
