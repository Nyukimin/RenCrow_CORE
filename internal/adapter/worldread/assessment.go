package worldread

import (
	"context"
	"fmt"
	"time"
)

// WorkPresentation is the D-13 work presentation WORLD publishes for one
// bound Actor. Kind and the task ID lists are WORLD-owned meaning: CORE keeps
// them as exact IDs and does not copy WORLD's enum or activity vocabulary
// into this adapter. A queued list alone never means the Actor moved, and a
// waiting list never starts new work; both are planning input only and claim
// no place, reservation, or measured geometry.
type WorkPresentation struct {
	Kind            string
	PresenceTaskIDs []string
	RunningTaskIDs  []string
	QueuedTaskIDs   []string
	WaitingTaskIDs  []string
}

// Assessment is WORLD's read-only meaning layer for one bound Actor.
// ExecutableActivityIDs is WORLD's owner-filtered candidate set (D-11, D-28):
// it is a public suggestion only. Nothing here selects, starts, moves, or
// occupies anything, and no field asserts a real location, reservation, or
// measured geometry (D-17/D-18 remain open).
type Assessment struct {
	SchemaVersion         int
	WorldID               string
	TimelineID            string
	TimelineEpoch         string
	ActorID               string
	AssessedAt            time.Time
	WorkSetState          string
	PolicyState           string
	WorkPresentation      WorkPresentation
	PermittedActivityIDs  []string
	ExecutableActivityIDs []string
	NewActivityAllowed    bool
	BlockedReason         string
	WorkSuccess           bool
}

type assessmentDTO struct {
	SchemaVersion         int                  `json:"schema_version"`
	WorldID               string               `json:"world_id"`
	TimelineID            string               `json:"timeline_id"`
	TimelineEpoch         string               `json:"timeline_epoch"`
	ActorID               string               `json:"actor_id"`
	AssessedAt            time.Time            `json:"assessed_at"`
	WorkSetState          string               `json:"work_set_state"`
	PolicyState           string               `json:"policy_state"`
	WorkPresentation      *workPresentationDTO `json:"work_presentation"`
	PermittedActivityIDs  *[]string            `json:"permitted_activity_ids"`
	ExecutableActivityIDs *[]string            `json:"executable_activity_ids"`
	NewActivityAllowed    bool                 `json:"new_activity_allowed"`
	BlockedReason         string               `json:"blocked_reason"`
	WorkSuccess           bool                 `json:"work_success"`
}

type workPresentationDTO struct {
	Kind            string    `json:"kind"`
	PresenceTaskIDs *[]string `json:"presence_task_ids"`
	RunningTaskIDs  *[]string `json:"running_task_ids"`
	QueuedTaskIDs   *[]string `json:"queued_task_ids"`
	WaitingTaskIDs  *[]string `json:"waiting_task_ids"`
}

// Assessment reads the assessment route for exactly one Actor inside CORE's
// configured view scope. An Actor outside the allowlist is rejected before any
// request is sent. A response that mixes scopes, omits a list, or carries a
// malformed ID fails as ErrContract rather than being read as an empty set. It
// also fails when the response claims work success or publishes an executable
// ID outside the permitted set, so CORE never consumes a candidate the current
// LifePolicy does not allow (D-07, D-08, D-10, D-11).
func (c *Client) Assessment(ctx context.Context, actorID string) (Assessment, error) {
	if c == nil || c.httpClient == nil || c.base == nil {
		return Assessment{}, &ReadError{Op: "assessment", Err: ErrInvalidConfig}
	}
	if ctx == nil {
		return Assessment{}, &ReadError{Op: "assessment", Err: ErrInvalidConfig}
	}
	if !validID(actorID) {
		return Assessment{}, &ReadError{Op: "assessment", Err: fmt.Errorf("%w: actor ID is invalid", ErrInvalidConfig)}
	}
	if _, allowed := c.actors[actorID]; !allowed {
		return Assessment{}, &ReadError{Op: "assessment", Err: fmt.Errorf("%w: actor %q is outside the configured view scope", ErrForbidden, actorID)}
	}
	actorTransport, err := encodeTransportID(actorID)
	if err != nil {
		return Assessment{}, &ReadError{Op: "assessment", Err: fmt.Errorf("%w: actor transport ID: %v", ErrInvalidConfig, err)}
	}

	var dto assessmentDTO
	resource := "actors/" + actorTransport + "/assessment"
	if err := c.get(ctx, c.urlFor(resource, nil), &dto, "assessment"); err != nil {
		return Assessment{}, err
	}
	if err := validateAssessment(actorID, c, &dto); err != nil {
		return Assessment{}, &ReadError{Op: "assessment", Err: err}
	}
	return Assessment{
		SchemaVersion:         dto.SchemaVersion,
		WorldID:               dto.WorldID,
		TimelineID:            dto.TimelineID,
		TimelineEpoch:         dto.TimelineEpoch,
		ActorID:               dto.ActorID,
		AssessedAt:            dto.AssessedAt,
		WorkSetState:          dto.WorkSetState,
		PolicyState:           dto.PolicyState,
		WorkPresentation:      copyPresentation(dto.WorkPresentation),
		PermittedActivityIDs:  copyIDs(*dto.PermittedActivityIDs),
		ExecutableActivityIDs: copyIDs(*dto.ExecutableActivityIDs),
		NewActivityAllowed:    dto.NewActivityAllowed,
		BlockedReason:         dto.BlockedReason,
		WorkSuccess:           dto.WorkSuccess,
	}, nil
}

func validateAssessment(actorID string, c *Client, dto *assessmentDTO) error {
	if dto.SchemaVersion != schemaVersion {
		return fmt.Errorf("%w: schema_version", ErrContract)
	}
	if dto.WorldID != c.worldIDValue || dto.TimelineID != c.timelineIDValue || dto.TimelineEpoch != c.epoch {
		return fmt.Errorf("%w: response scope does not match the configured scope", ErrContract)
	}
	if dto.ActorID != actorID {
		return fmt.Errorf("%w: actor_id does not match the requested actor", ErrContract)
	}
	if dto.AssessedAt.IsZero() {
		return fmt.Errorf("%w: assessed_at is missing", ErrContract)
	}
	if !validID(dto.WorkSetState) {
		return fmt.Errorf("%w: work_set_state is invalid", ErrContract)
	}
	if !validID(dto.PolicyState) {
		return fmt.Errorf("%w: policy_state is invalid", ErrContract)
	}
	if dto.BlockedReason != "" && !validID(dto.BlockedReason) {
		return fmt.Errorf("%w: blocked_reason is invalid", ErrContract)
	}
	if dto.WorkPresentation == nil {
		return fmt.Errorf("%w: work_presentation is missing", ErrContract)
	}
	if !validID(dto.WorkPresentation.Kind) {
		return fmt.Errorf("%w: work_presentation.kind is invalid", ErrContract)
	}
	for name, list := range map[string]*[]string{
		"work_presentation.presence_task_ids": dto.WorkPresentation.PresenceTaskIDs,
		"work_presentation.running_task_ids":  dto.WorkPresentation.RunningTaskIDs,
		"work_presentation.queued_task_ids":   dto.WorkPresentation.QueuedTaskIDs,
		"work_presentation.waiting_task_ids":  dto.WorkPresentation.WaitingTaskIDs,
		"permitted_activity_ids":              dto.PermittedActivityIDs,
		"executable_activity_ids":             dto.ExecutableActivityIDs,
	} {
		if err := validateIDList(name, list); err != nil {
			return err
		}
	}
	// D-07/D-08: WORLD never establishes work success. An empty or expired
	// WorkSet is "no assignment" or unknown, and only a CORE work result can
	// claim success, so a response carrying true is a contract violation rather
	// than a fact CORE may consume.
	if dto.WorkSuccess {
		return fmt.Errorf("%w: work_success must stay false; only a CORE work result establishes success", ErrContract)
	}
	// D-10/D-11: the published executable set is WORLD's owner-filtered subset
	// of the permitted set. An ID outside it would let a consumer start work the
	// current LifePolicy does not allow, so it fails as a contract violation
	// instead of being read as a granted candidate.
	permitted := make(map[string]struct{}, len(*dto.PermittedActivityIDs))
	for _, id := range *dto.PermittedActivityIDs {
		permitted[id] = struct{}{}
	}
	for _, id := range *dto.ExecutableActivityIDs {
		if _, ok := permitted[id]; !ok {
			return fmt.Errorf("%w: executable_activity_ids carries %q outside permitted_activity_ids", ErrContract, id)
		}
	}
	return nil
}

func validateIDList(name string, list *[]string) error {
	if list == nil {
		return fmt.Errorf("%w: %s is missing", ErrContract, name)
	}
	for _, id := range *list {
		if !validID(id) {
			return fmt.Errorf("%w: %s carries an invalid ID", ErrContract, name)
		}
	}
	return nil
}

func copyPresentation(dto *workPresentationDTO) WorkPresentation {
	return WorkPresentation{
		Kind:            dto.Kind,
		PresenceTaskIDs: copyIDs(*dto.PresenceTaskIDs),
		RunningTaskIDs:  copyIDs(*dto.RunningTaskIDs),
		QueuedTaskIDs:   copyIDs(*dto.QueuedTaskIDs),
		WaitingTaskIDs:  copyIDs(*dto.WaitingTaskIDs),
	}
}

func copyIDs(ids []string) []string {
	out := make([]string, len(ids))
	copy(out, ids)
	return out
}
