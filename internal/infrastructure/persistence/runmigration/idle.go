package runmigration

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	idle "github.com/Nyukimin/RenCrow_CORE/internal/application/idlechat"
	core "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func idleExtension(role string) string {
	if role == "story" || role == "dialogue" {
		return ".jsonl"
	}
	return ".json"
}
func textField(m map[string]json.RawMessage, k string) string {
	var s string
	_ = json.Unmarshal(m[k], &s)
	return s
}
func timeField(m map[string]json.RawMessage, k string) time.Time {
	var t time.Time
	_ = json.Unmarshal(m[k], &t)
	return t
}
func setField(m map[string]json.RawMessage, k string, v any) { b, _ := json.Marshal(v); m[k] = b }

func (c *cohort) rewriteIdentity(m map[string]json.RawMessage, owner string) error {
	field := "run_id"
	value := textField(m, field)
	for _, legacy := range []string{"generation_id", "trace_run_id"} {
		if v := textField(m, legacy); v != "" {
			if value != "" {
				return errors.New("multiple execution identity fields")
			}
			field = legacy
			value = v
		}
		delete(m, legacy)
	}
	if value == "" {
		return errors.New("missing execution reference")
	}
	r, e := c.resolve(owner, field, value)
	if e != nil {
		return e
	}
	if actor := textField(m, "actor_id"); owner != "" && actor != "" && !strings.EqualFold(actor, r.Assignee) {
		return errors.New("projection actor mismatch")
	}
	if taskID := textField(m, "task_id"); taskID != "" && taskID != string(r.TaskID) {
		return errors.New("projection Task mismatch")
	}
	setField(m, "task_id", r.TaskID)
	setField(m, "run_id", r.RunID)
	return nil
}

func (c *cohort) idleRecord(role string, b []byte) ([]byte, error) {
	var m map[string]json.RawMessage
	if e := strictJSON(b, &m); e != nil {
		return nil, e
	}
	value := textField(m, "run_id")
	field := "run_id"
	if value == "" {
		value = textField(m, "generation_id")
		field = "generation_id"
	}
	if _, e := c.resolve("idlechat", field, value); e != nil {
		actor := textField(m, "initiated_by")
		started := timeField(m, "created")
		if started.IsZero() {
			started = timeField(m, "created_at")
		}
		ended := timeField(m, "updated_at")
		if ended.IsZero() {
			ended = started
		}
		status := "completed"
		if role == "story" || role == "dialogue" {
			status = textField(m, "production_status")
		}
		if _, e = c.bind("idlechat", field, value, textField(m, "task_id"), actor, status, started, ended); e != nil {
			return nil, e
		}
	}
	if e := c.rewriteIdentity(m, "idlechat"); e != nil {
		return nil, e
	}
	if role != "story" {
		r, ok := c.runs[core.RunID(textField(m, "run_id"))]
		if !ok {
			return nil, errors.New("missing output Run")
		}
		actor := textField(m, "initiated_by")
		if actor == "" || !strings.EqualFold(actor, r.Assignee) {
			return nil, errors.New("IdleChat initiator and Run actor disagree")
		}
	}
	out, _ := json.Marshal(m)
	var typed any
	switch role {
	case "word":
		typed = &idle.WordPreparedTopic{}
	case "forecast":
		typed = &idle.PreparedTopic{}
	case "story":
		typed = &idle.StoryEpisodeArtifact{}
	case "dialogue":
		typed = &idle.DialogueEpisodeArtifact{}
	}
	if typed == nil {
		return nil, errors.New("unknown IdleChat artifact role")
	}
	if e := strictJSON(out, typed); e != nil {
		return nil, e
	}
	c.counts[role]++
	return out, nil
}

func (c *cohort) readIdle(input map[string][]byte) (map[string][]byte, error) {
	out := map[string][]byte{}
	for _, role := range []string{"word", "forecast", "story", "dialogue"} {
		p := c.inventory.Roles[role]
		if p == "" {
			continue
		}
		b := input[p]
		if role == "story" || role == "dialogue" {
			for _, line := range jsonLines(b) {
				x, e := c.idleRecord(role, line)
				if e != nil {
					return nil, e
				}
				out[role] = append(out[role], append(x, '\n')...)
			}
			if _, ok := out[role]; !ok {
				out[role] = []byte{}
			}
			continue
		}
		var stock struct {
			Stock map[string][]json.RawMessage `json:"stock"`
		}
		if e := strictJSON(b, &stock); e != nil {
			return nil, e
		}
		for key, items := range stock.Stock {
			for i, item := range items {
				x, e := c.idleRecord(role, item)
				if e != nil {
					return nil, e
				}
				items[i] = x
			}
			stock.Stock[key] = items
		}
		out[role] = marshalLine(stock)
	}
	if p := c.inventory.Roles["checkpoints"]; p != "" {
		var file struct {
			Version     int                        `json:"version"`
			Checkpoints map[string]json.RawMessage `json:"checkpoints"`
		}
		if e := strictJSON(input[p], &file); e != nil {
			return nil, e
		}
		if file.Version != 1 {
			return nil, errors.New("unsupported checkpoint version")
		}
		for key, b := range file.Checkpoints {
			var m map[string]json.RawMessage
			if e := strictJSON(b, &m); e != nil {
				return nil, e
			}
			if textField(m, "key") != key {
				return nil, errors.New("checkpoint key mismatch")
			}
			if e := c.rewriteIdentity(m, "idlechat"); e != nil {
				return nil, e
			}
			for _, artifactKey := range []string{"story_artifact", "dialogue_artifact"} {
				if raw := m[artifactKey]; len(raw) > 0 && string(raw) != "null" {
					var a map[string]json.RawMessage
					if e := strictJSON(raw, &a); e != nil {
						return nil, e
					}
					if e := c.rewriteIdentity(a, "idlechat"); e != nil {
						return nil, e
					}
					if textField(a, "task_id") != textField(m, "task_id") || textField(a, "run_id") != textField(m, "run_id") {
						return nil, errors.New("checkpoint artifact ownership mismatch")
					}
					setField(m, artifactKey, a)
				}
			}
			if revision := m["story_revision"]; len(revision) > 0 && string(revision) != "null" {
				var fields map[string]json.RawMessage
				if e := strictJSON(revision, &fields); e != nil {
					return nil, e
				}
				r, e := c.resolve("idlechat", "run_id", textField(fields, "source_run_id"))
				if e != nil {
					return nil, e
				}
				if string(r.TaskID) != textField(m, "task_id") {
					return nil, errors.New("revision source Task mismatch")
				}
				setField(fields, "source_run_id", r.RunID)
				setField(m, "story_revision", fields)
			}
			raw, _ := json.Marshal(m)
			var cp idle.GenerationCheckpoint
			if e := strictJSON(raw, &cp); e != nil {
				return nil, e
			}
			selected := c.runs[cp.RunID]
			for id, candidate := range c.runs {
				if id != cp.RunID && candidate.TaskID == cp.TaskID && !candidate.StartedAt.Before(selected.StartedAt) {
					// Adoption of an already issued successor belongs to the live
					// checkpoint owner. Do not guess across a stopped cohort or
					// rewrite immutable start evidence during identity migration.
					return nil, errors.New("checkpoint requires owner successor reconciliation before migration")
				}
			}
			file.Checkpoints[key] = raw
			c.counts["checkpoints"]++
		}
		out["checkpoints"] = marshalLine(file)
	}
	return out, nil
}
