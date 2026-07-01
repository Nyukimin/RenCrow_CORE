package chat

import "testing"

func TestNormalizeInputAppliesViewerDefaults(t *testing.T) {
	got := NormalizeInput(Input{Text: "hi"})
	if got.Channel != DefaultViewerChannel || got.UserID != DefaultViewerUserID || got.To != DefaultViewerRecipient {
		t.Fatalf("viewer defaults were not applied: %+v", got)
	}
}

func TestNormalizeInputTrimsExistingIdentity(t *testing.T) {
	got := NormalizeInput(Input{Channel: " line ", UserID: " user-1 ", Text: "hi"})
	if got.Channel != "line" || got.UserID != "user-1" {
		t.Fatalf("identity was not normalized: %+v", got)
	}
}
