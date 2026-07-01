package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/config"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/audiorouter"
)

func runAudioRouter(ctx context.Context, cfg *config.Config, configPath string, args []string) error {
	player := audiorouter.NewWASAPIPlayer()
	statusStore := audiorouter.NewStatusStore(filepath.Join(filepath.Dir(configPath), "audio_router_status.json"))
	if len(args) > 0 {
		switch args[0] {
		case "devices":
			return printAudioDevices(ctx, player)
		case "status":
			return printAudioRouterStatus(statusStore)
		default:
			return fmt.Errorf("unknown audio-router subcommand: %s", args[0])
		}
	}
	if cfg == nil || !cfg.AudioRouter.Enabled {
		return fmt.Errorf("audio_router.enabled=true is required")
	}

	deviceMap := make(map[string]string, len(cfg.AudioRouter.DeviceMap))
	for characterID, dev := range cfg.AudioRouter.DeviceMap {
		deviceMap[characterID] = dev.DeviceID
	}

	router := audiorouter.NewRouter(audiorouter.RouterConfig{
		DeviceMap:       deviceMap,
		Buffer:          time.Duration(cfg.AudioRouter.BufferMS) * time.Millisecond,
		QueueDepth:      8,
		DownloadTimeout: time.Duration(cfg.AudioRouter.DownloadTimeoutMS) * time.Millisecond,
	}, player, audiorouter.NewHTTPDownloader(time.Duration(cfg.AudioRouter.DownloadTimeoutMS)*time.Millisecond))
	router.Start(ctx)
	writeStatus := func() {
		if err := statusStore.Write(router.Status()); err != nil {
			log.Printf("audio_router_status_write_error err=%v", err)
		}
	}
	writeStatus()
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				writeStatus()
				return
			case <-ticker.C:
				writeStatus()
			}
		}
	}()

	client := audiorouter.NewSSEClient(audiorouter.SSEClientConfig{
		URL:            cfg.AudioRouter.SSEURL,
		ConnectTimeout: time.Duration(cfg.AudioRouter.ConnectTimeoutMS) * time.Millisecond,
		RetryDelay:     time.Duration(cfg.AudioRouter.RetryDelayMS) * time.Millisecond,
		OnConnect: func() {
			router.SetConnected(true)
			writeStatus()
		},
		OnDisconnect: func(err error) {
			router.SetConnected(false)
			writeStatus()
		},
	})

	log.Printf("[picoclaw-agent] AudioRouter connecting to %s", cfg.AudioRouter.SSEURL)
	return client.Run(ctx, func(id int64, ev audiorouter.Event) error {
		router.UpdateLastEventID(id)
		if err := router.Enqueue(ev); err != nil {
			log.Printf("audio_router_enqueue_error session=%s chunk=%d character=%s err=%v", ev.SessionID, ev.ChunkIndex, ev.CharacterID, err)
		}
		writeStatus()
		return nil
	})
}

func printAudioDevices(ctx context.Context, player audiorouter.Player) error {
	devices, err := player.ListDevices(ctx)
	if err != nil {
		return err
	}
	sort.Slice(devices, func(i, j int) bool {
		return devices[i].Name < devices[j].Name
	})
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(devices)
}

func printAudioRouterStatus(store *audiorouter.StatusStore) error {
	status, err := store.Read()
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(status)
}
