package vtuber

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	"golang.org/x/net/websocket"
)

func TestClientBridge_PublishEmotion(t *testing.T) {
	received := make(chan map[string]any, 1)
	srv := httptest.NewServer(websocket.Handler(func(ws *websocket.Conn) {
		defer ws.Close()
		var msg map[string]any
		if err := websocket.JSON.Receive(ws, &msg); err == nil {
			received <- msg
		}
	}))
	defer srv.Close()

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server url: %v", err)
	}
	host, portStr, err := splitHostPort(u.Host)
	if err != nil {
		t.Fatalf("split host port: %v", err)
	}
	port, _ := strconv.Atoi(portStr)

	bridge := NewClientBridge(ClientConfig{
		Characters: map[string]CharacterConfig{
			"mio": {
				AudioOutput:   "Audio-Out-Mio",
				Host:          host,
				Port:          port,
				ExpressionMap: map[string]string{"happy": "ExpHappy"},
			},
		},
	})

	err = bridge.PublishEmotion(context.Background(), orchestrator.VTuberEmotionRequest{
		CharacterID:  "mio",
		Speaking:     true,
		Valence:      0.4,
		Arousal:      0.7,
		Intensity:    0.6,
		EmotionLabel: "happy",
	})
	if err != nil {
		t.Fatalf("PublishEmotion failed: %v", err)
	}

	select {
	case msg := <-received:
		if msg["type"] != "emotion_tick" {
			t.Fatalf("unexpected type: %v", msg["type"])
		}
		payload, ok := msg["payload"].(map[string]any)
		if !ok {
			encoded, _ := json.Marshal(msg["payload"])
			t.Fatalf("payload was not map: %s", encoded)
		}
		if payload["emotion_label"] != "happy" {
			t.Fatalf("unexpected emotion label: %v", payload["emotion_label"])
		}
		if payload["expression"] != "ExpHappy" {
			t.Fatalf("unexpected expression: %v", payload["expression"])
		}
		if payload["audio_output"] != "Audio-Out-Mio" {
			t.Fatalf("unexpected audio_output: %v", payload["audio_output"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for websocket message")
	}
}

func TestClientBridge_PublishEmotionRequiresConfiguredCharacter(t *testing.T) {
	bridge := NewClientBridge(ClientConfig{})
	err := bridge.PublishEmotion(context.Background(), orchestrator.VTuberEmotionRequest{CharacterID: "mio"})
	if err == nil {
		t.Fatal("expected error for unconfigured character")
	}
}

func splitHostPort(hostport string) (string, string, error) {
	u, err := url.Parse("http://" + hostport)
	if err != nil {
		return "", "", err
	}
	return u.Hostname(), u.Port(), nil
}
