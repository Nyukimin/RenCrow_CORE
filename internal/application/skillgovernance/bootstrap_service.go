package skillgovernance

import (
	"context"
	"fmt"
	"time"

	domainskill "github.com/Nyukimin/RenCrow_CORE/internal/domain/skillgovernance"
)

type BootstrapStore interface {
	ListSkillManifests(ctx context.Context, limit int) ([]domainskill.SkillManifest, error)
	SaveSkillTriggerLog(ctx context.Context, log domainskill.SkillTriggerLog) error
}

type BootstrapService struct {
	store BootstrapStore
	now   func() time.Time
}

func NewBootstrapService(store BootstrapStore) *BootstrapService {
	return &BootstrapService{store: store, now: time.Now}
}

func (s *BootstrapService) WithNow(now func() time.Time) *BootstrapService {
	if now != nil {
		s.now = now
	}
	return s
}

func (s *BootstrapService) Record(ctx context.Context, task domainskill.TaskContext, usedSkillIDs []string) ([]domainskill.SkillTriggerLog, error) {
	if s == nil || s.store == nil {
		return nil, nil
	}
	manifests, err := s.store.ListSkillManifests(ctx, 1000)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	logs := domainskill.BuildBootstrapTriggerLogs(manifests, task, usedSkillIDs, now, func(index int, skillID string) string {
		return fmt.Sprintf("evt_skill_bootstrap_%d_%d", now.UnixNano(), index+1)
	})
	for _, log := range logs {
		if err := s.store.SaveSkillTriggerLog(ctx, log); err != nil {
			return nil, err
		}
	}
	return logs, nil
}
