package config

import (
	"strings"
	"testing"

	domainmeeting "github.com/Nyukimin/RenCrow_CORE/internal/domain/taskmeeting"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestDailyReportMeetingsConfigDisabledByDefault(t *testing.T) {
	cfg := &Config{}
	if cfg.Heartbeat.Routine.DailyReportMeetings.Enabled {
		t.Fatal("meeting routine must default disabled")
	}
	if err := cfg.validateDailyReportMeetingsConfig(); err != nil {
		t.Fatalf("disabled meeting config validation: %v", err)
	}
}

func TestDailyReportMeetingsBoardPublicationRequiresCoordinatorScope(t *testing.T) {
	cfg := validMeetingConfig()
	cfg.Heartbeat.Routine.DailyReportMeetings.CoordinatorActorID = "mio"
	cfg.BoardCommunication.Enabled = true
	cfg.BoardCommunication.PrincipalID = "rencrow-comm"
	cfg.BoardCommunication.ChannelIDs = []string{"rencrow"}
	cfg.Heartbeat.Routine.DailyReportMeetings.BoardPublication = &domainmeeting.BoardPublication{ChannelID: "rencrow", SenderPrincipalID: "rencrow-comm", Recipients: []string{}}
	if err := cfg.validateDailyReportMeetingsConfig(); err != nil {
		t.Fatalf("explicit coordinator publication route rejected: %v", err)
	}
	for _, test := range []struct {
		name string
		edit func(*Config)
	}{
		{"disabled BOARD", func(c *Config) { c.BoardCommunication.Enabled = false }},
		{"foreign channel", func(c *Config) { c.Heartbeat.Routine.DailyReportMeetings.BoardPublication.ChannelID = "elsewhere" }},
		{"foreign sender", func(c *Config) {
			c.Heartbeat.Routine.DailyReportMeetings.BoardPublication.SenderPrincipalID = "outsider"
		}},
		{"implicit recipients", func(c *Config) { c.Heartbeat.Routine.DailyReportMeetings.BoardPublication.Recipients = nil }},
		{"sender recipient", func(c *Config) {
			c.Heartbeat.Routine.DailyReportMeetings.BoardPublication.Recipients = []string{"rencrow-comm"}
		}},
		{"unsorted recipients", func(c *Config) {
			c.Heartbeat.Routine.DailyReportMeetings.BoardPublication.Recipients = []string{"z", "a"}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			copyConfig := *cfg
			publication := *cfg.Heartbeat.Routine.DailyReportMeetings.BoardPublication
			copyConfig.Heartbeat.Routine.DailyReportMeetings.BoardPublication = &publication
			test.edit(&copyConfig)
			if err := copyConfig.validateDailyReportMeetingsConfig(); err == nil {
				t.Fatal("invalid publication route accepted")
			}
		})
	}
}

func TestDailyReportMeetingsConfigProjectsCommonTaskTiming(t *testing.T) {
	meeting := validDailyReportMeetingsConfig()
	timing := meeting.TaskTiming()
	if err := timing.Validate(); err != nil {
		t.Fatalf("projected TaskTiming.Validate(): %v", err)
	}
	if timing.Timezone != "Asia/Tokyo" || timing.Recurrence == nil || timing.Recurrence.Frequency != modulecore.TaskRecurrenceDaily ||
		len(timing.Recurrence.Times) != 3 || strings.Join(timing.Recurrence.Times, ",") != "08:00,12:00,17:00" || timing.Recurrence.Weekdays == nil {
		t.Fatalf("projected timing=%+v", timing)
	}
}

func TestDailyReportMeetingsConfigRequiresCompleteExplicitBindings(t *testing.T) {
	cases := []struct {
		name string
		edit func(*Config)
		want string
	}{
		{name: "missing owner registration", edit: func(c *Config) { c.LocalAgentOps.Enabled = false }, want: "owner_user_id"},
		{name: "owner mismatch", edit: func(c *Config) { c.LocalAgentOps.UserID = "another-owner" }, want: "owner_user_id"},
		{name: "wrong scope", edit: func(c *Config) { c.Heartbeat.Routine.DailyReportMeetings.DataScope = "public" }, want: "data_scope"},
		{name: "wrong actor count", edit: func(c *Config) {
			c.Heartbeat.Routine.DailyReportMeetings.Actors = c.Heartbeat.Routine.DailyReportMeetings.Actors[:7]
		}, want: "exactly 8"},
		{name: "duplicate core actor", edit: func(c *Config) {
			c.Heartbeat.Routine.DailyReportMeetings.Actors[1].CoreActorID = c.Heartbeat.Routine.DailyReportMeetings.Actors[0].CoreActorID
		}, want: "duplicate CORE actor"},
		{name: "duplicate world actor", edit: func(c *Config) {
			c.Heartbeat.Routine.DailyReportMeetings.Actors[1].WorldActorID = c.Heartbeat.Routine.DailyReportMeetings.Actors[0].WorldActorID
		}, want: "duplicate WORLD actor"},
		{name: "coordinator outside roster", edit: func(c *Config) { c.Heartbeat.Routine.DailyReportMeetings.CoordinatorActorID = "outsider" }, want: "one of the configured 8"},
		{name: "missing room", edit: func(c *Config) { c.Heartbeat.Routine.DailyReportMeetings.LivingRoomRef = "" }, want: "distinct exact"},
		{name: "same rooms", edit: func(c *Config) {
			c.Heartbeat.Routine.DailyReportMeetings.DiningRoomRef = c.Heartbeat.Routine.DailyReportMeetings.LivingRoomRef
		}, want: "distinct exact"},
		{name: "wrong timezone", edit: func(c *Config) { c.Heartbeat.Routine.DailyReportMeetings.Timezone = "UTC" }, want: "Asia/Tokyo"},
		{name: "wrong times", edit: func(c *Config) { c.Heartbeat.Routine.DailyReportMeetings.Times = []string{"08:00", "12:00"} }, want: "exactly 08:00,12:00,17:00"},
		{name: "unbounded duration", edit: func(c *Config) { c.Heartbeat.Routine.DailyReportMeetings.MeetingDeadlineAfterSecs = 4 * 60 * 60 }, want: "between 60 and 10800"},
		{name: "missing world scope", edit: func(c *Config) { c.WorldProjection.TimelineEpoch = "" }, want: "world_projection read scope"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validMeetingConfig()
			tc.edit(cfg)
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() error=%v, want substring %q", err, tc.want)
			}
		})
	}
}

func validDailyReportMeetingsConfig() DailyReportMeetingsConfig {
	actors := make([]DailyReportMeetingActor, 8)
	for index, actor := range []string{"mio", "shiro", "heavy", "wild", "coder1", "coder2", "coder3", "coder4"} {
		actors[index] = DailyReportMeetingActor{CoreActorID: actor, WorldActorID: "world-" + actor}
	}
	return DailyReportMeetingsConfig{
		Enabled: true, Revision: 1, OwnerUserID: "owner-1", DataScope: "user",
		CoordinatorActorID: "shiro", Actors: actors,
		LivingRoomRef: "room-living", DiningRoomRef: "room-dining",
		Timezone: "Asia/Tokyo", Times: []string{"08:00", "12:00", "17:00"}, MeetingDeadlineAfterSecs: 3600,
	}
}

func validMeetingConfig() *Config {
	return &Config{
		Server:          ServerConfig{Port: 8080},
		Session:         SessionConfig{StorageDir: "./data/sessions"},
		DCI:             DCIConfig{Enabled: boolConfigPtr(false)},
		Coder1:          CoderConfig{Name: "coder1"},
		Coder2:          CoderConfig{Name: "coder2"},
		Coder3:          CoderConfig{Name: "coder3"},
		Coder4:          CoderConfig{Name: "coder4"},
		LocalAgentOps:   LocalAgentOpsConfig{Enabled: true, UserID: "owner-1", AuthTokenFile: "/tmp/rencrow-test-token"},
		WorldProjection: WorldProjectionConfig{BaseURL: "http://127.0.0.1:9000", TokenEnv: "WORLD_READ_TOKEN", WorldID: "world-1", TimelineID: "timeline-1", TimelineEpoch: "epoch-1"},
		Heartbeat:       HeartbeatConfig{Routine: RoutineHeartbeatConfig{DailyReportMeetings: validDailyReportMeetingsConfig()}},
	}
}
