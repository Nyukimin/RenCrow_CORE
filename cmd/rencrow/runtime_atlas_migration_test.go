package main

import (
	"context"
	"errors"
	"testing"

	backlogapp "github.com/Nyukimin/RenCrow_CORE/internal/application/backlog"
	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
)

type startupAtlasFailingBacklogStore struct {
	err error
}

func (s startupAtlasFailingBacklogStore) List(context.Context, int) ([]domainbacklog.Item, error) {
	return nil, s.err
}

func (startupAtlasFailingBacklogStore) Save(context.Context, domainbacklog.Item) error {
	return nil
}

func TestPrepareAtlasLifecycleServiceFailsClosedOnMigrationError(t *testing.T) {
	wantErr := errors.New("legacy backlog read failed")
	service := backlogapp.NewService(startupAtlasFailingBacklogStore{err: wantErr}, nil)

	prepared, migrated, err := prepareAtlasLifecycleService(context.Background(), service)
	if prepared != nil || migrated || !errors.Is(err, wantErr) {
		t.Fatalf("prepared=%v migrated=%t err=%v, want nil service and propagated migration failure", prepared, migrated, err)
	}
}
