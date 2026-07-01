package audiorouter

import (
	"path/filepath"
	"testing"
	"time"
)

func TestStatusStoreWriteAndRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audio_router_status.json")
	store := NewStatusStore(path)

	want := Status{
		Connected:   true,
		LastEventID: 42,
		Characters: map[string]CharacterStatus{
			"mio": {
				DeviceID:     "mio-device",
				QueueDepth:   1,
				LastEventKey: "s1:0",
				LastPlayedAt: time.Unix(1700000000, 0).UTC(),
			},
		},
	}
	if err := store.Write(want); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	got, err := store.Read()
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if got.LastEventID != want.LastEventID || !got.Connected {
		t.Fatalf("unexpected status: %+v", got)
	}
	if got.Characters["mio"].DeviceID != "mio-device" {
		t.Fatalf("unexpected character status: %+v", got.Characters["mio"])
	}
}
