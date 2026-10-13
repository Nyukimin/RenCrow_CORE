package stt

import "testing"

func TestParseControlReadsUtteranceFields(t *testing.T) {
	c, ok := ParseControl([]byte(`{"type":"start","utterance_id":"u-42","sample_rate":16000,"channels":1,"format":"pcm_s16le"}`))
	if !ok {
		t.Fatal("ParseControl() ok = false")
	}
	if c.Type != "start" || c.UtteranceID != "u-42" || c.SampleRate != 16000 || c.Channels != 1 || c.Format != "pcm_s16le" {
		t.Fatalf("ParseControl() = %+v", c)
	}
}

func TestParseControlToleratesLegacyAndOptionalFields(t *testing.T) {
	c, ok := ParseControl([]byte(`{"type":" stop "}`))
	if !ok || c.Type != "stop" || c.UtteranceID != "" || c.SampleRate != 0 {
		t.Fatalf("legacy stop = %+v ok=%t", c, ok)
	}
	c, ok = ParseControl([]byte(`{"type":"config","mimeType":"audio/wav"}`))
	if !ok || c.Type != "config" {
		t.Fatalf("legacy config = %+v ok=%t", c, ok)
	}
	c, ok = ParseControl([]byte(`{"type":"start","sample_rate":"16000"}`))
	if !ok || c.Type != "start" || c.SampleRate != 0 {
		t.Fatalf("a malformed optional field must not turn a control frame into audio: %+v ok=%t", c, ok)
	}
}

func TestParseControlRejectsNonControlPayloads(t *testing.T) {
	for _, payload := range [][]byte{nil, {}, {0, 1, 2}, []byte(`{"type":`), []byte(`{"no_type":1}`), []byte(`[1,2]`), []byte(`"stop"`)} {
		if c, ok := ParseControl(payload); ok {
			t.Fatalf("ParseControl(%q) = %+v, want not a control frame", payload, c)
		}
	}
}

func TestParseControlSanitisesUtteranceID(t *testing.T) {
	long := make([]byte, 65)
	for i := range long {
		long[i] = 'a'
	}
	for name, payload := range map[string]string{
		"too long":     `{"type":"start","utterance_id":"` + string(long) + `"}`,
		"control char": `{"type":"start","utterance_id":"a\u0007b"}`,
		"blank":        `{"type":"start","utterance_id":"   "}`,
	} {
		c, ok := ParseControl([]byte(payload))
		if !ok || c.UtteranceID != "" {
			t.Fatalf("%s: UtteranceID = %q ok=%t, want it dropped", name, c.UtteranceID, ok)
		}
	}
}

func TestControlAudioFormatError(t *testing.T) {
	ok := Control{Type: "start", SampleRate: 16000, Channels: 1, Format: "pcm_s16le"}
	if msg := ok.AudioFormatError(); msg != "" {
		t.Fatalf("supported format rejected: %q", msg)
	}
	if msg := (Control{Type: "start"}).AudioFormatError(); msg != "" {
		t.Fatalf("a start without format fields is the legacy shape and must be accepted: %q", msg)
	}
	for name, c := range map[string]Control{
		"rate":     {Type: "start", SampleRate: 48000},
		"channels": {Type: "start", Channels: 2},
		"format":   {Type: "start", Format: "pcm_f32le"},
	} {
		if msg := c.AudioFormatError(); msg == "" {
			t.Fatalf("%s: unsupported format accepted", name)
		}
	}
}

func TestIsFinalizeControl(t *testing.T) {
	for _, typ := range []string{"stop", "final_pending", " stop "} {
		if !IsFinalizeControl(typ) {
			t.Fatalf("IsFinalizeControl(%q) = false", typ)
		}
	}
	for _, typ := range []string{"start", "cancel", "config", ""} {
		if IsFinalizeControl(typ) {
			t.Fatalf("IsFinalizeControl(%q) = true", typ)
		}
	}
}
