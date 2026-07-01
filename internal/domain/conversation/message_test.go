package conversation

import (
	"testing"
	"time"
)

func TestNewMessage(t *testing.T) {
	msg := NewMessage(SpeakerUser, "Hello", nil)

	if msg.Speaker != SpeakerUser {
		t.Errorf("Expected speaker %s, got %s", SpeakerUser, msg.Speaker)
	}

	if msg.Msg != "Hello" {
		t.Errorf("Expected message 'Hello', got '%s'", msg.Msg)
	}

	if msg.Timestamp.IsZero() {
		t.Error("Expected non-zero timestamp")
	}

	if msg.Meta == nil {
		t.Error("Expected non-nil meta (empty map)")
	}

	if len(msg.Meta) != 0 {
		t.Errorf("Expected empty meta map, got %d entries", len(msg.Meta))
	}
}

func TestNewMessageWithMeta(t *testing.T) {
	meta := map[string]interface{}{
		"tool_name": "web_search",
		"query":     "Go language",
	}

	msg := NewMessage(SpeakerTool, "Search results", meta)

	if msg.Speaker != SpeakerTool {
		t.Errorf("Expected speaker %s, got %s", SpeakerTool, msg.Speaker)
	}

	if msg.Meta == nil {
		t.Fatal("Expected non-nil meta")
	}

	if msg.Meta["tool_name"] != "web_search" {
		t.Errorf("Expected tool_name 'web_search', got '%v'", msg.Meta["tool_name"])
	}
}

func TestSpeakerConstants(t *testing.T) {
	speakers := []Speaker{
		SpeakerUser,
		SpeakerMio,
		SpeakerShiro,
		SpeakerAka,
		SpeakerAo,
		SpeakerGin,
		SpeakerSystem,
		SpeakerTool,
		SpeakerMemory,
	}

	expected := []string{
		"user",
		"mio",
		"shiro",
		"aka",
		"ao",
		"gin",
		"system",
		"tool",
		"memory",
	}

	for i, speaker := range speakers {
		if string(speaker) != expected[i] {
			t.Errorf("Expected speaker %s, got %s", expected[i], speaker)
		}
	}
}

func TestMessageTimestamp(t *testing.T) {
	before := time.Now()
	msg := NewMessage(SpeakerUser, "Test", nil)
	after := time.Now()

	if msg.Timestamp.Before(before) || msg.Timestamp.After(after) {
		t.Error("Timestamp should be between before and after")
	}
}
