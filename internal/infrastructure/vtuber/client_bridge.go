package vtuber

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/application/orchestrator"
	"golang.org/x/net/websocket"
)

type CharacterConfig struct {
	AudioOutput   string
	Host          string
	Port          int
	ExpressionMap map[string]string
}

type ClientConfig struct {
	ConnectTimeout time.Duration
	WriteTimeout   time.Duration
	Characters     map[string]CharacterConfig
}

type runtimeConn struct {
	mu   sync.Mutex
	conn *websocket.Conn
}

type ClientBridge struct {
	cfg      ClientConfig
	mu       sync.RWMutex
	runtimes map[string]*runtimeConn
}

func NewClientBridge(cfg ClientConfig) *ClientBridge {
	if cfg.ConnectTimeout <= 0 {
		cfg.ConnectTimeout = 3 * time.Second
	}
	if cfg.WriteTimeout <= 0 {
		cfg.WriteTimeout = 2 * time.Second
	}
	return &ClientBridge{
		cfg:      cfg,
		runtimes: make(map[string]*runtimeConn),
	}
}

func (b *ClientBridge) PublishEmotion(ctx context.Context, req orchestrator.VTuberEmotionRequest) error {
	characterID := strings.TrimSpace(strings.ToLower(req.CharacterID))
	if characterID == "" {
		return fmt.Errorf("character_id is required")
	}
	chCfg, ok := b.cfg.Characters[characterID]
	if !ok {
		return fmt.Errorf("vtuber character %q is not configured", characterID)
	}
	runtime := b.getRuntime(characterID)
	runtime.mu.Lock()
	defer runtime.mu.Unlock()

	conn, err := b.ensureConn(ctx, runtime, chCfg)
	if err != nil {
		return err
	}

	expression := strings.TrimSpace(req.Expression)
	if expression == "" {
		expression = strings.TrimSpace(chCfg.ExpressionMap[req.EmotionLabel])
	}

	msg := map[string]any{
		"type":         "emotion_tick",
		"character":    characterID,
		"timestamp_ms": time.Now().UTC().UnixMilli(),
		"payload": map[string]any{
			"speaking":      boolToInt(req.Speaking),
			"valence":       req.Valence,
			"arousal":       req.Arousal,
			"intensity":     req.Intensity,
			"emotion_label": req.EmotionLabel,
		},
	}
	if expression != "" {
		msg["payload"].(map[string]any)["expression"] = expression
	}
	if strings.TrimSpace(chCfg.AudioOutput) != "" {
		msg["payload"].(map[string]any)["audio_output"] = chCfg.AudioOutput
	}
	if err := b.sendWithDeadline(conn, msg); err != nil {
		_ = conn.Close()
		runtime.conn = nil
		return fmt.Errorf("publish emotion for %s: %w", characterID, err)
	}
	return nil
}

func (b *ClientBridge) getRuntime(characterID string) *runtimeConn {
	b.mu.Lock()
	defer b.mu.Unlock()
	if rt, ok := b.runtimes[characterID]; ok {
		return rt
	}
	rt := &runtimeConn{}
	b.runtimes[characterID] = rt
	return rt
}

func (b *ClientBridge) ensureConn(ctx context.Context, runtime *runtimeConn, cfg CharacterConfig) (*websocket.Conn, error) {
	if runtime.conn != nil {
		return runtime.conn, nil
	}
	conn, err := b.connectWS(ctx, cfg)
	if err != nil {
		return nil, err
	}
	runtime.conn = conn
	return conn, nil
}

func (b *ClientBridge) connectWS(ctx context.Context, cfg CharacterConfig) (*websocket.Conn, error) {
	host := strings.TrimSpace(cfg.Host)
	if host == "" {
		return nil, fmt.Errorf("vts host is empty")
	}
	u := &url.URL{
		Scheme: "ws",
		Host:   net.JoinHostPort(host, strconv.Itoa(cfg.Port)),
		Path:   "/",
	}
	wsCfg, err := websocket.NewConfig(u.String(), "http://localhost/")
	if err != nil {
		return nil, fmt.Errorf("build websocket config: %w", err)
	}

	timeout := b.cfg.ConnectTimeout
	if deadline, ok := ctx.Deadline(); ok {
		if d := time.Until(deadline); d > 0 && d < timeout {
			timeout = d
		}
	}
	dialCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	type dialResult struct {
		conn *websocket.Conn
		err  error
	}
	ch := make(chan dialResult, 1)
	go func() {
		conn, err := websocket.DialConfig(wsCfg)
		ch <- dialResult{conn: conn, err: err}
	}()
	select {
	case <-dialCtx.Done():
		return nil, fmt.Errorf("connect ws timeout: %w", dialCtx.Err())
	case res := <-ch:
		if res.err != nil {
			return nil, fmt.Errorf("connect ws: %w", res.err)
		}
		return res.conn, nil
	}
}

func (b *ClientBridge) sendWithDeadline(conn *websocket.Conn, msg any) error {
	if b.cfg.WriteTimeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(b.cfg.WriteTimeout))
		defer conn.SetDeadline(time.Time{})
	}
	return websocket.JSON.Send(conn, msg)
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
