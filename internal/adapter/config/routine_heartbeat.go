package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

var (
	errRoutineWorldTokenUnavailable     = errors.New("heartbeat.routine.world_read_token_file is unavailable or not owner-only")
	errRoutineWorldTokenInvalid         = errors.New("heartbeat.routine.world_read_token_file must contain a bounded bearer token")
	errRoutineWorldLifeTokenUnavailable = errors.New("heartbeat.routine.world_life_token_file is unavailable or not owner-only")
	errRoutineWorldLifeTokenInvalid     = errors.New("heartbeat.routine.world_life_token_file must contain a bounded bearer token")
)

func (c *Config) validateRoutineHeartbeat() error {
	routine := c.Heartbeat.Routine
	if err := c.validateRoutineMedia(); err != nil {
		return err
	}
	if !routine.Enabled {
		return nil
	}
	if !c.Heartbeat.Enabled {
		return errors.New("heartbeat.routine.enabled requires heartbeat.enabled=true")
	}
	if !c.Heartbeat.AgentSchedulerEnabled {
		return errors.New("heartbeat.routine.enabled requires heartbeat.agent_scheduler_enabled=true")
	}
	if !c.BoardCommunication.Enabled {
		return errors.New("heartbeat.routine.enabled requires board_communication.enabled=true")
	}
	if !c.LocalAgentOps.Enabled || strings.TrimSpace(c.LocalAgentOps.UserID) == "" ||
		strings.TrimSpace(c.LocalAgentOps.UserID) != c.LocalAgentOps.UserID ||
		!localAgentOpsUserIDPattern.MatchString(c.LocalAgentOps.UserID) {
		return errors.New("heartbeat.routine.enabled requires an exact local_agent_ops.user_id owner")
	}
	if len(routine.SharedRooms) == 0 && len(routine.PrivateRooms) == 0 {
		if routine.WorldURL != "" || routine.WorldID != "" || routine.TimelineID != "" || routine.TimelineEpoch != "" || routine.WorldReadTokenFile != "" || routine.WorldLifeTokenFile != "" || len(routine.WorldActors) != 0 {
			return errors.New("heartbeat.routine WORLD read requires at least one explicitly configured shared_rooms or private_rooms entry")
		}
	}
	seenRooms := make(map[string]struct{}, len(routine.SharedRooms))
	for _, roomID := range routine.SharedRooms {
		if !validRoutineWorldID(roomID) {
			return errors.New("heartbeat.routine.shared_rooms must contain exact WORLD room IDs")
		}
		if _, exists := seenRooms[roomID]; exists {
			return errors.New("heartbeat.routine.shared_rooms must not contain duplicate room IDs")
		}
		seenRooms[roomID] = struct{}{}
	}
	for actorID, roomID := range routine.PrivateRooms {
		if !routineActorID(actorID) {
			return errors.New("heartbeat.routine.private_rooms keys must be canonical CORE actor IDs")
		}
		if !validRoutineWorldID(roomID) {
			return errors.New("heartbeat.routine.private_rooms values must be exact WORLD room IDs")
		}
	}

	worldConfigured := routine.WorldURL != "" || routine.WorldID != "" || routine.TimelineID != "" ||
		routine.TimelineEpoch != "" || routine.WorldReadTokenFile != "" || routine.WorldLifeTokenFile != "" || len(routine.WorldActors) != 0
	if !worldConfigured {
		return nil
	}
	if routine.WorldURL == "" || routine.WorldID == "" || routine.TimelineID == "" || routine.TimelineEpoch == "" || routine.WorldReadTokenFile == "" {
		return errors.New("heartbeat.routine WORLD read requires world_url, world_id, timeline_id, timeline_epoch, and world_read_token_file")
	}
	if err := validateRoutineWorldURL(routine.WorldURL); err != nil {
		return err
	}
	if !validRoutineWorldID(routine.WorldID) {
		return errors.New("heartbeat.routine.world_id must be a nonempty exact identifier")
	}
	if !validRoutineWorldID(routine.TimelineID) {
		return errors.New("heartbeat.routine.timeline_id must be a nonempty exact identifier")
	}
	if !validRoutineWorldID(routine.TimelineEpoch) {
		return errors.New("heartbeat.routine.timeline_epoch must be a nonempty exact identifier")
	}
	if len(routine.WorldActors) == 0 {
		return errors.New("heartbeat.routine.world_actors must explicitly bind at least one CORE actor to a WORLD actor")
	}
	seenWorldActors := make(map[string]struct{}, len(routine.WorldActors))
	for coreActorID, worldActorID := range routine.WorldActors {
		if !routineActorID(coreActorID) {
			return errors.New("heartbeat.routine.world_actors keys must be canonical CORE actor IDs")
		}
		if !validRoutineWorldID(worldActorID) {
			return errors.New("heartbeat.routine.world_actors values must be exact WORLD actor IDs")
		}
		if _, exists := seenWorldActors[worldActorID]; exists {
			return errors.New("heartbeat.routine.world_actors values must be unique")
		}
		seenWorldActors[worldActorID] = struct{}{}
	}
	for actorID := range routine.PrivateRooms {
		if _, bound := routine.WorldActors[actorID]; !bound {
			return errors.New("heartbeat.routine.private_rooms actors must be present in world_actors when WORLD reads are enabled")
		}
	}
	if err := validateRoutineWorldTokenFile(routine.WorldReadTokenFile); err != nil {
		return err
	}
	if routine.WorldLifeTokenFile != "" {
		lifeToken, err := ReadRoutineWorldLifeBearerToken(routine.WorldLifeTokenFile)
		if err != nil {
			return err
		}
		readToken, err := ReadRoutineWorldBearerToken(routine.WorldReadTokenFile)
		if err != nil {
			return err
		}
		if lifeToken == readToken {
			return errors.New("heartbeat.routine.world_life_token_file must use a bearer token distinct from world_read_token_file")
		}
	}
	return nil
}

func routineActorID(actorID string) bool {
	switch actorID {
	case "mio", "shiro", "midori", "kuro", "aka", "ao", "kin", "gin":
		return true
	default:
		return false
	}
}

func validRoutineWorldID(value string) bool {
	if value == "" || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			return false
		}
	}
	return true
}

func validateRoutineWorldURL(raw string) error {
	parsed, err := url.Parse(raw)
	if raw == "" || strings.TrimSpace(raw) != raw || err != nil || parsed == nil || parsed.Opaque != "" ||
		parsed.Host == "" || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery ||
		parsed.Fragment != "" || parsed.RawFragment != "" {
		return errors.New("heartbeat.routine.world_url must be an absolute HTTP(S) URL without userinfo, query, or fragment")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return errors.New("heartbeat.routine.world_url must use HTTP or HTTPS")
	}
	if scheme == "http" && !isRoutineLoopbackHost(parsed.Hostname()) {
		return errors.New("heartbeat.routine.world_url permits HTTP only for loopback hosts")
	}
	if strings.HasSuffix(parsed.Host, ":") {
		return errors.New("heartbeat.routine.world_url has an invalid port")
	}
	if port := parsed.Port(); port != "" {
		for _, char := range port {
			if char < '0' || char > '9' {
				return errors.New("heartbeat.routine.world_url has an invalid port")
			}
		}
		if len(port) > 5 {
			return errors.New("heartbeat.routine.world_url has an invalid port")
		}
		var number int
		for _, char := range port {
			number = number*10 + int(char-'0')
		}
		if number < 1 || number > 65535 {
			return errors.New("heartbeat.routine.world_url has an invalid port")
		}
	}
	return nil
}

func isRoutineLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ReadRoutineWorldBearerToken reads the exact owner-only WORLD read token
// during CORE startup. Diagnostics never include token bytes or filesystem
// permission details.
func ReadRoutineWorldBearerToken(path string) (string, error) {
	return readRoutineWorldBearerToken(path, "heartbeat.routine.world_read_token_file", errRoutineWorldTokenUnavailable, errRoutineWorldTokenInvalid)
}

// ReadRoutineWorldLifeBearerToken reads the distinct owner-only WORLD life
// token during CORE startup. It is never used by read-only operations.
func ReadRoutineWorldLifeBearerToken(path string) (string, error) {
	return readRoutineWorldBearerToken(path, "heartbeat.routine.world_life_token_file", errRoutineWorldLifeTokenUnavailable, errRoutineWorldLifeTokenInvalid)
}

func readRoutineWorldBearerToken(path, setting string, unavailable, invalid error) (string, error) {
	if path == "" || strings.TrimSpace(path) != path || strings.ContainsRune(path, '\x00') ||
		!filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", fmt.Errorf("%s must be an absolute clean path", setting)
	}
	handle, err := openConfidentialFile(setting, path, unavailable)
	if err != nil {
		return "", unavailable
	}
	defer handle.Close()
	contents, err := io.ReadAll(io.LimitReader(handle, maxStorageHostBearerTokenBytes+1))
	if err != nil || len(contents) == 0 || len(contents) > maxStorageHostBearerTokenBytes {
		return "", invalid
	}
	if bytes.HasSuffix(contents, []byte("\r\n")) {
		contents = contents[:len(contents)-2]
	} else if bytes.HasSuffix(contents, []byte("\n")) {
		contents = contents[:len(contents)-1]
	}
	if !validStorageHostBearerToken(contents) {
		return "", invalid
	}
	return string(contents), nil
}

func validateRoutineWorldTokenFile(path string) error {
	_, err := ReadRoutineWorldBearerToken(path)
	return err
}
