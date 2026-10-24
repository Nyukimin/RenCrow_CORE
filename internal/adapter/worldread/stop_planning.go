package worldread

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
)

// stopPlanningActivityQuery is WORLD's only input for a stop planning decision:
// the running activity the caller wants a safe-stop decision for. WORLD supplies
// the posture, cleanup, and occupancy meaning from its owner mapping, so CORE
// never sends them (D-11, D-12).
const stopPlanningActivityQuery = "stop_planning_activity_id"

// StopPlanningAction is WORLD's closed continuation vocabulary for one running
// activity.
type StopPlanningAction string

const (
	// StopPlanningContinue keeps the running activity as WORLD left it.
	StopPlanningContinue StopPlanningAction = "continue"
	// StopPlanningStop carries WORLD's planned safe stop: the posture to end in
	// and the bounded cleanup to run. Stopping is a WORLD event; it cancels,
	// fails, or completes no CORE work.
	StopPlanningStop StopPlanningAction = "stop"
	// StopPlanningHold keeps the Actor where it is because WORLD's owner mapping
	// grants no safe interruption point. It never carries a posture, a cleanup
	// action, or an occupancy release.
	StopPlanningHold StopPlanningAction = "hold"
)

// StopDecision is one safe-stop planning decision published by WORLD for the
// activity CORE named. It is a plan, not an effect: reading it stops, moves, or
// releases nothing, and it claims no real location, reservation, or measured
// geometry (D-17, D-18 keep those open).
//
// StopState and StopActions are non-empty only for StopPlanningStop, and only
// that action carries an occupancy decision. RetainOccupancyReason explains a
// release WORLD refused while still planning the stop, because cancelling an
// activity and releasing the physical occupancy it holds are separate operations
// (D-11). ClearWorkPresentation is true only for a fresh empty WorkSet and never
// means work succeeded (D-07, D-08).
type StopDecision struct {
	ActivityID            string
	Action                StopPlanningAction
	Reason                string
	StopState             string
	StopActions           []string
	ReleaseOccupancy      bool
	RetainOccupancyReason string
	ClearWorkPresentation bool
}

// stopPlanningDTO mirrors WORLD's stop_planning object. Every required field is a
// pointer so a missing one fails as a contract violation instead of being read
// as the zero value, and UnmarshalJSON rejects a key WORLD's decision does not
// define, so a consumer can never receive a field that was never granted.
type stopPlanningDTO struct {
	ActivityID            *string   `json:"activity_id"`
	Action                *string   `json:"action"`
	Reason                *string   `json:"reason"`
	StopState             *string   `json:"stop_state"`
	StopActions           *[]string `json:"stop_actions"`
	ReleaseOccupancy      *bool     `json:"release_occupancy"`
	RetainOccupancyReason *string   `json:"retain_occupancy_reason"`
	ClearWorkPresentation *bool     `json:"clear_work_presentation"`
}

func (d *stopPlanningDTO) UnmarshalJSON(raw []byte) error {
	// stopPlanningFields is the same shape without this method, so decoding here
	// stays one pass instead of calling UnmarshalJSON again.
	type stopPlanningFields stopPlanningDTO
	var fields stopPlanningFields
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&fields); err != nil {
		return err
	}
	*d = stopPlanningDTO(fields)
	return nil
}

// StopPlanning reads WORLD's safe-stop planning decision for one running
// activity of one Actor inside CORE's configured view scope. Both IDs are
// checked before any request, and an Actor outside the allowlist is rejected
// without one. The whole assessment envelope is validated first, so a decision
// is never read from a response that mixes scopes or contradicts the LifePolicy
// it was derived from.
//
// CORE keeps the decision exactly as WORLD published it: the reason vocabulary,
// the posture, the cleanup action names, and whether a release is safe stay
// WORLD's owner meaning, so this method re-derives none of them. What it does
// enforce is that the decision is self-consistent, names back the activity that
// was asked about, and stays a plan - see validateStopPlanning (D-11, D-12).
// Nothing here executes a stop, and while D-27 leaves CORE's production read
// route undecided no production caller consumes it yet.
func (c *Client) StopPlanning(ctx context.Context, actorID, activityID string) (StopDecision, error) {
	const op = "stop_planning"
	if ctx == nil {
		return StopDecision{}, &ReadError{Op: op, Err: ErrInvalidConfig}
	}
	resource, err := c.actorAssessmentResource(actorID)
	if err != nil {
		return StopDecision{}, &ReadError{Op: op, Err: err}
	}
	if !validID(activityID) {
		return StopDecision{}, &ReadError{Op: op, Err: fmt.Errorf("%w: activity ID is invalid", ErrInvalidConfig)}
	}

	var dto assessmentDTO
	query := url.Values{stopPlanningActivityQuery: []string{activityID}}
	if err := c.get(ctx, c.urlFor(resource, query), &dto, op); err != nil {
		return StopDecision{}, err
	}
	if err := validateAssessment(actorID, c, &dto); err != nil {
		return StopDecision{}, &ReadError{Op: op, Err: err}
	}
	if dto.StopPlanning == nil {
		return StopDecision{}, &ReadError{Op: op, Err: fmt.Errorf(
			"%w: WORLD published no stop planning decision for activity %q", ErrContract, activityID)}
	}
	if err := validateStopPlanning(actorID, activityID, &dto, dto.StopPlanning); err != nil {
		return StopDecision{}, &ReadError{Op: op, Err: err}
	}
	return copyStopDecision(dto.StopPlanning), nil
}

// validateStopPlanning keeps WORLD's decision readable as one meaning. The
// activity always names back what was asked about, so a decision cannot be
// applied to another activity. Only a stop grants a posture, a cleanup action, or
// an occupancy decision, because a hold is the absence of a safe stop and a
// continue changes nothing - reading a stop state off either would move an Actor
// on WORLD's authority without a planned stop. A retained-occupancy reason never
// accompanies a granted release, since it exists only to explain a refused one.
// ClearWorkPresentation follows the WorkSet state exactly as WORLD derives it, so
// a decision cannot drop CORE's work presentation while an assignment is still
// published (D-07, D-08, D-11, D-12). Each case fails as ErrContract with no
// partially read decision.
func validateStopPlanning(actorID, activityID string, assessment *assessmentDTO, plan *stopPlanningDTO) error {
	if plan.ActivityID == nil || !validID(*plan.ActivityID) {
		return fmt.Errorf("%w: stop_planning.activity_id is missing or invalid", ErrContract)
	}
	if *plan.ActivityID != activityID {
		return fmt.Errorf("%w: stop_planning.activity_id %q does not name the requested activity %q for actor %q",
			ErrContract, *plan.ActivityID, activityID, actorID)
	}
	if plan.Action == nil || !validID(*plan.Action) {
		return fmt.Errorf("%w: stop_planning.action is missing or invalid", ErrContract)
	}
	if plan.Reason == nil || !validID(*plan.Reason) {
		return fmt.Errorf("%w: stop_planning.reason is missing or invalid", ErrContract)
	}
	if plan.StopState == nil || plan.StopActions == nil || plan.ReleaseOccupancy == nil ||
		plan.ClearWorkPresentation == nil {
		return fmt.Errorf("%w: stop_planning publishes an incomplete stop effect", ErrContract)
	}
	for _, action := range *plan.StopActions {
		if !validID(action) {
			return fmt.Errorf("%w: stop_planning.stop_actions carries an invalid ID", ErrContract)
		}
	}
	retainReason := ""
	if plan.RetainOccupancyReason != nil {
		retainReason = *plan.RetainOccupancyReason
		if !validID(retainReason) {
			return fmt.Errorf("%w: stop_planning.retain_occupancy_reason is invalid", ErrContract)
		}
	}

	action := StopPlanningAction(*plan.Action)
	switch action {
	case StopPlanningContinue, StopPlanningHold:
		if *plan.StopState != "" || len(*plan.StopActions) != 0 || *plan.ReleaseOccupancy || retainReason != "" {
			return fmt.Errorf("%w: stop_planning.action %q grants a stop effect; only %q may",
				ErrContract, action, StopPlanningStop)
		}
	case StopPlanningStop:
		if *plan.StopState == "" {
			return fmt.Errorf("%w: stop_planning.action %q names no stop state", ErrContract, action)
		}
		if len(*plan.StopActions) == 0 {
			return fmt.Errorf("%w: stop_planning.action %q names no cleanup action", ErrContract, action)
		}
		if *plan.ReleaseOccupancy && retainReason != "" {
			return fmt.Errorf("%w: stop_planning released occupancy and explained %q",
				ErrContract, retainReason)
		}
	default:
		return fmt.Errorf("%w: stop_planning.action %q is outside WORLD's vocabulary", ErrContract, *plan.Action)
	}

	if *plan.ClearWorkPresentation != (assessment.WorkSetState == "empty") {
		return fmt.Errorf("%w: stop_planning.clear_work_presentation=%v does not follow from work_set_state %q",
			ErrContract, *plan.ClearWorkPresentation, assessment.WorkSetState)
	}
	return nil
}

func copyStopDecision(plan *stopPlanningDTO) StopDecision {
	decision := StopDecision{
		ActivityID:            *plan.ActivityID,
		Action:                StopPlanningAction(*plan.Action),
		Reason:                *plan.Reason,
		StopState:             *plan.StopState,
		ReleaseOccupancy:      *plan.ReleaseOccupancy,
		ClearWorkPresentation: *plan.ClearWorkPresentation,
		StopActions:           copyIDs(*plan.StopActions),
	}
	if plan.RetainOccupancyReason != nil {
		decision.RetainOccupancyReason = *plan.RetainOccupancyReason
	}
	return decision
}
