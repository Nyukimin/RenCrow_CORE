package config

import (
	"fmt"
	"strings"
	"time"

	domainmeeting "github.com/Nyukimin/RenCrow_CORE/internal/domain/taskmeeting"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	// Configuration bounds the operational completion window to 1 minute–3 hours.
	// The domain separately validates that the persisted deadline is representable.
	taskMeetingDeadlineMaxSecs = int64(3 * 60 * 60)
	taskMeetingDeadlineMinSecs = int64(60)
)

// DailyReportMeetingsConfig is the single editable source for the meeting
// roster, schedule, rooms, owner scope, and bounded completion window.
type DailyReportMeetingsConfig struct {
	Enabled                  bool                      `yaml:"enabled"`
	Revision                 uint64                    `yaml:"revision"`
	OwnerUserID              string                    `yaml:"owner_user_id"`
	DataScope                string                    `yaml:"data_scope"`
	CoordinatorActorID       string                    `yaml:"coordinator_actor_id"`
	Actors                   []DailyReportMeetingActor `yaml:"actors"`
	LivingRoomRef            string                    `yaml:"living_room_ref"`
	DiningRoomRef            string                    `yaml:"dining_room_ref"`
	Timezone                 string                    `yaml:"timezone"`
	Times                    []string                  `yaml:"times"`
	MeetingDeadlineAfterSecs int64                     `yaml:"meeting_deadline_after_seconds"`
}

type DailyReportMeetingActor struct {
	CoreActorID  string `yaml:"core_actor_id"`
	WorldActorID string `yaml:"world_actor_id"`
}

// TaskTiming projects the configured meeting recurrence into the common
// Task/Scheduler timing contract without maintaining a second cadence.
func (c DailyReportMeetingsConfig) TaskTiming() modulecore.TaskTiming {
	return modulecore.TaskTiming{
		Timezone:       c.Timezone,
		AllowedWindows: []modulecore.TaskTimeWindow{},
		Recurrence: &modulecore.TaskRecurrence{
			Frequency: modulecore.TaskRecurrenceDaily,
			Times:     append([]string(nil), c.Times...),
			Weekdays:  []time.Weekday{},
		},
	}
}

func (c *Config) validateDailyReportMeetingsConfig() error {
	meeting := c.Heartbeat.Routine.DailyReportMeetings
	if !meeting.Enabled {
		return nil
	}
	const path = "heartbeat.routine.daily_report_meetings"
	if meeting.Revision == 0 {
		return fmt.Errorf("%s.revision must be positive when enabled", path)
	}
	if !c.LocalAgentOps.Enabled || meeting.OwnerUserID == "" || meeting.OwnerUserID != c.LocalAgentOps.UserID {
		return fmt.Errorf("%s.owner_user_id must match the enabled local_agent_ops user registration", path)
	}
	if meeting.DataScope != "user" {
		return fmt.Errorf("%s.data_scope must be user", path)
	}
	if !isCanonicalMeetingActorID(meeting.CoordinatorActorID) {
		return fmt.Errorf("%s.coordinator_actor_id must be a canonical actor ID", path)
	}
	if len(meeting.Actors) != 8 {
		return fmt.Errorf("%s.actors must contain exactly 8 explicit actor mappings", path)
	}
	coreActors := make(map[string]struct{}, len(meeting.Actors))
	worldActors := make(map[string]struct{}, len(meeting.Actors))
	coordinatorFound := false
	for index, actor := range meeting.Actors {
		if !isCanonicalMeetingActorID(actor.CoreActorID) {
			return fmt.Errorf("%s.actors[%d].core_actor_id must be a canonical actor ID", path, index)
		}
		if actor.WorldActorID == "" || strings.TrimSpace(actor.WorldActorID) != actor.WorldActorID {
			return fmt.Errorf("%s.actors[%d].world_actor_id must be an explicit exact ID", path, index)
		}
		if _, duplicate := coreActors[actor.CoreActorID]; duplicate {
			return fmt.Errorf("%s.actors contains duplicate CORE actor %q", path, actor.CoreActorID)
		}
		if _, duplicate := worldActors[actor.WorldActorID]; duplicate {
			return fmt.Errorf("%s.actors contains duplicate WORLD actor %q", path, actor.WorldActorID)
		}
		coreActors[actor.CoreActorID] = struct{}{}
		worldActors[actor.WorldActorID] = struct{}{}
		coordinatorFound = coordinatorFound || actor.CoreActorID == meeting.CoordinatorActorID
	}
	if !coordinatorFound {
		return fmt.Errorf("%s.coordinator_actor_id must be one of the configured 8 actors", path)
	}
	if meeting.LivingRoomRef == "" || strings.TrimSpace(meeting.LivingRoomRef) != meeting.LivingRoomRef ||
		meeting.DiningRoomRef == "" || strings.TrimSpace(meeting.DiningRoomRef) != meeting.DiningRoomRef ||
		meeting.LivingRoomRef == meeting.DiningRoomRef {
		return fmt.Errorf("%s must configure distinct exact living_room_ref and dining_room_ref values", path)
	}
	if err := domainmeeting.ValidateDailyMeetingTiming(meeting.TaskTiming()); err != nil {
		return fmt.Errorf("%s timing: %w", path, err)
	}
	if meeting.MeetingDeadlineAfterSecs < taskMeetingDeadlineMinSecs || meeting.MeetingDeadlineAfterSecs > taskMeetingDeadlineMaxSecs {
		return fmt.Errorf("%s.meeting_deadline_after_seconds must be between %d and %d", path, taskMeetingDeadlineMinSecs, taskMeetingDeadlineMaxSecs)
	}
	world := c.WorldProjection
	if strings.TrimSpace(world.BaseURL) == "" || strings.TrimSpace(world.TokenEnv) == "" ||
		strings.TrimSpace(world.WorldID) == "" || strings.TrimSpace(world.TimelineID) == "" || strings.TrimSpace(world.TimelineEpoch) == "" {
		return fmt.Errorf("%s requires an explicit world_projection read scope", path)
	}
	return nil
}

func isCanonicalMeetingActorID(value string) bool {
	if value == "" || strings.TrimSpace(value) != value || strings.ToLower(value) != value {
		return false
	}
	for _, char := range value {
		if char <= ' ' {
			return false
		}
	}
	return true
}
