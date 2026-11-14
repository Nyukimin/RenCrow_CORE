package config

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

const maxRoutineMediaItemsPerActor = 32

var routineMediaIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

func (c *Config) validateRoutineMedia() error {
	if len(c.Heartbeat.Routine.Media) == 0 {
		return nil
	}
	if !c.BrowserActor.Enabled {
		return errors.New("heartbeat.routine.media requires browser_actor.enabled=true")
	}
	for actorID, items := range c.Heartbeat.Routine.Media {
		if !routineActorID(actorID) {
			return errors.New("heartbeat.routine.media keys must be canonical CORE actor IDs")
		}
		if len(items) == 0 || len(items) > maxRoutineMediaItemsPerActor {
			return errors.New("heartbeat.routine.media must contain 1..32 items per actor")
		}
		seenIDs := make(map[string]struct{}, len(items))
		for _, item := range items {
			if !routineMediaIDPattern.MatchString(item.ID) {
				return errors.New("heartbeat.routine.media item id must be a stable path-safe identifier")
			}
			if _, exists := seenIDs[item.ID]; exists {
				return errors.New("heartbeat.routine.media contains duplicate item ids for an actor")
			}
			seenIDs[item.ID] = struct{}{}
			if item.Kind != "video" && item.Kind != "film" && item.Kind != "music" {
				return errors.New("heartbeat.routine.media item kind must be video, film, or music")
			}
			if !validRoutineMediaText(item.Title, 160, false) || !validRoutineMediaText(item.Credit, 200, false) {
				return errors.New("heartbeat.routine.media item title and credit must be nonempty bounded text")
			}
			if err := validateRoutineMediaURL(item.URL); err != nil {
				return err
			}
		}
	}
	return nil
}

func validRoutineMediaText(value string, maxBytes int, allowEmpty bool) bool {
	if value == "" {
		return allowEmpty
	}
	if len(value) > maxBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func validateRoutineMediaURL(raw string) error {
	parsed, err := url.Parse(raw)
	if len(raw) > 2048 || raw == "" || strings.TrimSpace(raw) != raw || err != nil || parsed == nil ||
		!parsed.IsAbs() || parsed.Opaque != "" || parsed.Scheme != "https" || parsed.Host == "" || parsed.Hostname() == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawFragment != "" {
		return errors.New("heartbeat.routine.media item url must be a direct HTTPS URL without credentials, query, or fragment")
	}
	return nil
}
