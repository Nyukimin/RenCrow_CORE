package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	appprojection "github.com/Nyukimin/RenCrow_CORE/internal/application/worldprojection"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/worldoutbox"
)

const (
	defaultWorldProjectionInterval      = time.Minute
	defaultWorldProjectionValidFor      = 15 * time.Minute
	defaultWorldProjectionRefreshBefore = 5 * time.Minute
	defaultWorldProjectionTimeout       = 5 * time.Second
)

// startWorldProjection starts CORE's outbound WORLD projection loop when the
// feature is enabled. It is fail-closed: an enabled but incomplete
// configuration stops startup instead of silently projecting a partial set.
func startWorldProjection(cfg *config.Config, deps *Dependencies) error {
	if cfg == nil || deps == nil {
		return errors.New("WORLD projection: config and dependencies are required")
	}
	if !cfg.WorldProjection.Enabled {
		return nil
	}
	if deps.taskStore == nil {
		return errors.New("WORLD projection: canonical Task store is not initialized")
	}
	settings := cfg.WorldProjection
	baseURL := strings.TrimSpace(settings.BaseURL)
	tokenEnv := strings.TrimSpace(settings.TokenEnv)
	if baseURL == "" || tokenEnv == "" {
		return errors.New("WORLD projection: base_url and token_env are required")
	}
	token := os.Getenv(tokenEnv)
	if token == "" {
		return fmt.Errorf("WORLD projection: credential environment %s is empty", tokenEnv)
	}
	if strings.TrimSpace(settings.WorldID) == "" || strings.TrimSpace(settings.TimelineID) == "" ||
		strings.TrimSpace(settings.TimelineEpoch) == "" || strings.TrimSpace(settings.SourceEpoch) == "" {
		return errors.New("WORLD projection: world_id, timeline_id, timeline_epoch, and source_epoch are required")
	}
	if len(settings.Actors) == 0 {
		return errors.New("WORLD projection: at least one actor is required")
	}
	actors := make([]appprojection.ProjectionActor, 0, len(settings.Actors))
	for _, actor := range settings.Actors {
		if strings.TrimSpace(actor.ActorID) == "" || strings.TrimSpace(actor.ActorInstanceID) == "" {
			return errors.New("WORLD projection: every actor requires actor_id and actor_instance_id")
		}
		actors = append(actors, appprojection.ProjectionActor{
			ActorID:          actor.ActorID,
			ActorInstanceID:  actor.ActorInstanceID,
			ActorTransportID: actor.ActorTransportID,
			CoreAssignee:     actor.CoreAssignee,
		})
	}
	outboxPath, err := worldProjectionOutboxPath(cfg.WorkspaceDir, settings.OutboxPath)
	if err != nil {
		return err
	}
	store, err := worldoutbox.NewSQLiteStore(outboxPath)
	if err != nil {
		return fmt.Errorf("WORLD projection: open outbox: %w", err)
	}
	publisher, err := appprojection.NewHTTPPublisher(baseURL, token, worldProjectionDuration(settings.TimeoutSeconds, defaultWorldProjectionTimeout))
	if err != nil {
		_ = store.Close()
		return err
	}
	projector := &appprojection.Projector{
		Source:        deps.taskStore,
		Outbox:        store,
		History:       store,
		Revisions:     store,
		Actors:        actors,
		WorldID:       settings.WorldID,
		TimelineID:    settings.TimelineID,
		TimelineEpoch: settings.TimelineEpoch,
		SourceEpoch:   settings.SourceEpoch,
		ValidFor:      worldProjectionDuration(settings.ValidForSeconds, defaultWorldProjectionValidFor),
		RefreshBefore: worldProjectionDuration(settings.RefreshBeforeSeconds, defaultWorldProjectionRefreshBefore),
	}
	worker := &appprojection.Worker{Outbox: store, Publisher: publisher, HeadReader: publisher}
	service := &appprojection.Service{
		Projector: projector,
		Worker:    worker,
		Interval:  worldProjectionDuration(settings.IntervalSeconds, defaultWorldProjectionInterval),
		OnError: func(err error) {
			log.Printf("WORLD projection pass failed: %v", err)
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	deps.worldProjectionCancel = cancel
	deps.worldProjectionDone = done
	deps.worldProjectionClose = store.Close
	go func() {
		defer close(done)
		if err := service.Run(ctx); err != nil {
			log.Printf("WORLD projection loop stopped: %v", err)
		}
	}()
	log.Printf("WORLD projection loop started destination=%s/%s actors=%d", settings.WorldID, settings.TimelineID, len(actors))
	return nil
}

func worldProjectionOutboxPath(workspace, configured string) (string, error) {
	path := strings.TrimSpace(configured)
	if path == "" {
		path = filepath.Join(workspace, "data", "world_projection", "outbox.sqlite")
	}
	if !filepath.IsAbs(path) {
		if strings.TrimSpace(workspace) == "" {
			return "", errors.New("WORLD projection: workspace_dir is required for a relative outbox_path")
		}
		path = filepath.Join(workspace, path)
	}
	path = filepath.Clean(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("WORLD projection: create outbox directory: %w", err)
	}
	return path, nil
}

func worldProjectionDuration(seconds int, fallback time.Duration) time.Duration {
	if seconds <= 0 {
		return fallback
	}
	return time.Duration(seconds) * time.Second
}
