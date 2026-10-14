package stt

import (
	"strings"
	"testing"
)

func TestParseControlIgnoresOversizedPayloads(t *testing.T) {
	small := `{"type":"stop","pad":"` + strings.Repeat("x", 200) + `"}`
	if _, ok := ParseControl([]byte(small)); !ok {
		t.Fatal("a control message of a few hundred bytes must still parse")
	}
	big := `{"type":"stop","pad":"` + strings.Repeat("x", MaxControlMessageBytes) + `"}`
	if len(big) <= MaxControlMessageBytes {
		t.Fatal("test payload must exceed the limit")
	}
	if c, ok := ParseControl([]byte(big)); ok {
		t.Fatalf("a payload over %d bytes is audio, not control; parsed as %+v", MaxControlMessageBytes, c)
	}
}
