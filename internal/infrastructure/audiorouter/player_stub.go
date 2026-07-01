//go:build !windows

package audiorouter

import (
	"context"
	"fmt"
	"time"
)

type WASAPIPlayer struct{}

func NewWASAPIPlayer() *WASAPIPlayer {
	return &WASAPIPlayer{}
}

func (p *WASAPIPlayer) ListDevices(ctx context.Context) ([]Device, error) {
	_ = ctx
	return nil, fmt.Errorf("audio router device enumeration is only supported on Windows")
}

func (p *WASAPIPlayer) PlayWAV(ctx context.Context, deviceID string, wavData []byte, buffer time.Duration) error {
	_, _, _, _ = ctx, deviceID, wavData, buffer
	return fmt.Errorf("audio router playback is only supported on Windows")
}
