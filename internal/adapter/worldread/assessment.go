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
	// StopPlanning is present only when the caller named a running activity on
	// the stop_planning_activity_id query. It is a decision for that activity,
	// never an executed stop (D-11).
	StopPlanning *stopPlanningDTO `json:"stop_planning,omitempty"`
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
// Beyond those identity checks it enforces WORLD's closed state vocabulary and
// the presentation and permission-flag consistency of WORLD's assessment
// contract, so an undefined state or a contradictory presentation is never read
// as a life fact (D-07, D-09, D-10, D-13).
func (c *Client) Assessment(ctx context.Context, actorID string) (Assessment, error) {
	if ctx == nil {
		return Assessment{}, &ReadError{Op: "assessment", Err: ErrInvalidConfig}
	}
	resource, err := c.actorAssessmentResource(actorID)
	if err != nil {
		return Assessment{}, &ReadError{Op: "assessment", Err: err}
	}

	var dto assessmentDTO
	if err := c.get(ctx, c.urlFor(resource, nil), &dto, "assessment"); err != nil {
		return Assessment{}, err
	}
	if err := validateAssessment(actorID, c, &dto); err != nil {
		return Assessment{}, &ReadError{Op: "assessment", Err: err}
	}
	// WORLD publishes a stop planning decision only for a named activity. One
	// that arrives on a plain assessment would be a decision nobody asked for and
	// nothing reads, so it fails closed instead of being dropped silently.
	if dto.StopPlanning != nil {
		return Assessment{}, &ReadError{Op: "assessment", Err: fmt.Errorf(
			"%w: an assessment published stop planning without an activity ID", ErrContract)}
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

// actorAssessmentResource checks the configured client, the Actor ID, and
// CORE's configured view scope, then returns the assessment resource path for
// exactly one Actor. The assessment route serves both the plain assessment and
// its stop planning decision, so both read the same identity boundary before any
// request is sent (D-11, D-12).
func (c *Client) actorAssessmentResource(actorID string) (string, error) {
	if c == nil || c.httpClient == nil || c.base == nil {
		return "", ErrInvalidConfig
	}
	if !validID(actorID) {
		return "", fmt.Errorf("%w: actor ID is invalid", ErrInvalidConfig)
	}
	if _, allowed := c.actors[actorID]; !allowed {
		return "", fmt.Errorf("%w: actor %q is outside the configured view scope", ErrForbidden, actorID)
	}
	actorTransport, err := encodeTransportID(actorID)
	if err != nil {
		return "", fmt.Errorf("%w: actor transport ID: %v", ErrInvalidConfig, err)
	}
	return "actors/" + actorTransport + "/assessment", nil
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
	if err := validateAssessmentMeaning(dto); err != nil {
		return err
	}
	return nil
}

// validateAssessmentMeaning enforces WORLD's closed assessment vocabulary and
// the internal consistency of WORLD's assessment contract. work_set_state,
// policy_state, and work_presentation.kind are WORLD-owned meaning: an
// undefined value would be read by CORE as a life fact, and a presentation that
// does not follow from the WorkSet state and task ID lists would move an Actor
// or open a permission the WorkSet does not carry. A denied permission always
// names its reason and an open permission carries none, so the flag and the
// reason never travel together. Each case fails as ErrContract with no partially
// read assessment (D-07, D-09, D-10, D-13).
func validateAssessmentMeaning(dto *assessmentDTO) error {
	switch dto.WorkSetState {
	case "missing", "expired", "empty", "assigned":
	default:
		return fmt.Errorf("%w: work_set_state %q is outside WORLD's vocabulary", ErrContract, dto.WorkSetState)
	}
	switch dto.PolicyState {
	case "missing", "expired", "fresh":
	default:
		return fmt.Errorf("%w: policy_state %q is outside WORLD's vocabulary", ErrContract, dto.PolicyState)
	}
	if dto.NewActivityAllowed == (dto.BlockedReason != "") {
		return fmt.Errorf("%w: new_activity_allowed=%v does not match blocked_reason %q", ErrContract,
			dto.NewActivityAllowed, dto.BlockedReason)
	}
	if err := validatePolicyPermission(dto); err != nil {
		return err
	}
	return validateWorkPresentation(dto.WorkSetState, dto.WorkPresentation)
}

// validatePolicyPermission binds the new-activity permission to the LifePolicy
// state exactly as WORLD derives it (D-09, D-10). A missing policy means CORE
// published no current permission, so nothing is permitted and the block reason
// is fixed. An expired policy keeps only its explicit fallback subset, and a
// fresh policy that permits nothing names whether nothing was allowed or nothing
// is registered. Reading a grant from any other combination would let CORE start
// a life activity the current LifePolicy does not allow, so each case fails as
// ErrContract.
func validatePolicyPermission(dto *assessmentDTO) error {
	granted := len(*dto.PermittedActivityIDs) > 0
	if dto.NewActivityAllowed != granted {
		return fmt.Errorf("%w: new_activity_allowed=%v does not follow from %d permitted activity ID(s)",
			ErrContract, dto.NewActivityAllowed, len(*dto.PermittedActivityIDs))
	}
	switch dto.PolicyState {
	case "missing":
		if granted {
			return fmt.Errorf("%w: a missing life policy permits %q; a new activity requires a current policy",
				ErrContract, (*dto.PermittedActivityIDs)[0])
		}
		if dto.BlockedReason != "policy_missing" {
			return fmt.Errorf("%w: a missing life policy blocks with %q, got %q",
				ErrContract, "policy_missing", dto.BlockedReason)
		}
	case "expired":
		if !granted && dto.BlockedReason != "policy_expired_no_fallback" {
			return fmt.Errorf("%w: an expired life policy without a registered fallback blocks with %q, got %q",
				ErrContract, "policy_expired_no_fallback", dto.BlockedReason)
		}
	case "fresh":
		if !granted && dto.BlockedReason != "no_allowed_activity" && dto.BlockedReason != "no_registered_activity" {
			return fmt.Errorf("%w: a fresh life policy that permits nothing blocks with no_allowed_activity or no_registered_activity, got %q",
				ErrContract, dto.BlockedReason)
		}
	}
	return nil
}

func validateWorkPresentation(workSetState string, presentation *workPresentationDTO) error {
	presence, running := *presentation.PresenceTaskIDs, *presentation.RunningTaskIDs
	queued, waiting := *presentation.QueuedTaskIDs, *presentation.WaitingTaskIDs
	if workSetState != "assigned" {
		if presentation.Kind != "none" {
			return fmt.Errorf("%w: work_presentation.kind %q must be none while the work set is %q",
				ErrContract, presentation.Kind, workSetState)
		}
		if len(presence)+len(running)+len(queued)+len(waiting) != 0 {
			return fmt.Errorf("%w: a %q work set cannot publish task IDs", ErrContract, workSetState)
		}
		return nil
	}
	if len(presence)+len(running)+len(queued)+len(waiting) == 0 {
		return fmt.Errorf("%w: an assigned work set publishes no task", ErrContract)
	}
	// Presence-required work is always one of the running items, and the kind is
	// the single decision WORLD derives from these lists: presence dominates,
	// otherwise running work stays remote, then waiting, then queued.
	runningByID := make(map[string]struct{}, len(running))
	for _, id := range running {
		runningByID[id] = struct{}{}
	}
	for _, id := range presence {
		if _, ok := runningByID[id]; !ok {
			return fmt.Errorf("%w: presence task %q is not a running task", ErrContract, id)
		}
	}
	expected := "queued"
	switch {
	case len(presence) > 0:
		expected = "presence"
	case len(running) > 0:
		expected = "remote"
	case len(waiting) > 0:
		expected = "waiting"
	}
	if presentation.Kind != expected {
		return fmt.Errorf("%w: work_presentation.kind %q does not follow from the task ID lists, which publish %q",
			ErrContract, presentation.Kind, expected)
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
