// Package turntiming records bounded, process-local timing points for one
// opt-in chat turn. It never records request content or identity values.
package turntiming

import (
	"context"
	"sync"
	"time"
)

type Point string

const (
	PointHandlerReceived                    Point = "handler_received"
	PointRequestParsed                      Point = "request_parsed"
	PointHandlerValidationEnded             Point = "handler_validation_ended"
	PointHandlerSessionStarted              Point = "handler_session_started"
	PointHandlerSessionEnded                Point = "handler_session_ended"
	PointAccepted                           Point = "accepted"
	PointAsyncStart                         Point = "async_start"
	PointProcessStarted                     Point = "process_started"
	PointProcessSessionStarted              Point = "process_session_started"
	PointProcessSessionEnded                Point = "process_session_ended"
	PointRootTaskPreparationStarted         Point = "root_task_preparation_started"
	PointRootTaskPreparationEnded           Point = "root_task_preparation_ended"
	PointRootTaskCreated                    Point = "root_task_created"
	PointInitialEventsStarted               Point = "initial_events_started"
	PointInitialEventsEnded                 Point = "initial_events_ended"
	PointMessageReceivedEventPublished      Point = "message_received_event_published"
	PointServerReceivedMetricPublished      Point = "server_received_metric_published"
	PointSessionTurnLogAttemptEnded         Point = "session_turn_log_attempt_ended"
	PointPersonaObservationStarted          Point = "persona_observation_started"
	PointPersonaObservationEnded            Point = "persona_observation_ended"
	PointPreRouteChecksStarted              Point = "pre_route_checks_started"
	PointPreRouteChecksEnded                Point = "pre_route_checks_ended"
	PointTurnInputBuilt                     Point = "turn_input_built"
	PointRouteDecisionStarted               Point = "route_decision_started"
	PointRouteDecided                       Point = "route_decided"
	PointClassificationStarted              Point = "classification_started"
	PointClassificationEnded                Point = "classification_ended"
	PointTaskActivationStarted              Point = "task_activation_started"
	PointTaskActivationEnded                Point = "task_activation_ended"
	PointTaskRoutingEventPublished          Point = "task_routing_event_published"
	PointTaskExecutionPrepared              Point = "task_execution_prepared"
	PointTaskAssignmentEventPublished       Point = "task_assignment_event_published"
	PointTaskAssignmentRecorded             Point = "task_assignment_recorded"
	PointTaskRootRunStarted                 Point = "task_root_run_started"
	PointTaskExecutionRunStarted            Point = "task_execution_run_started"
	PointTaskIdentityBound                  Point = "task_identity_bound"
	PointLeadRunResolutionStarted           Point = "lead_run_resolution_started"
	PointLeadRunResolutionEnded             Point = "lead_run_resolution_ended"
	PointRouteMetricPublished               Point = "route_metric_published"
	PointSkillBootstrapStarted              Point = "skill_bootstrap_started"
	PointSkillBootstrapEnded                Point = "skill_bootstrap_ended"
	PointTTSSessionSetupStarted             Point = "tts_session_setup_started"
	PointTTSSessionSetupEnded               Point = "tts_session_setup_ended"
	PointLeadRunPersistenceStarted          Point = "lead_run_persistence_started"
	PointLeadRunPersistenceEnded            Point = "lead_run_persistence_ended"
	PointLeadAgentRunSaved                  Point = "lead_agent_run_saved"
	PointLeadAgentStartEventAppended        Point = "lead_agent_start_event_appended"
	PointLeadContextPackSaved               Point = "lead_context_pack_saved"
	PointLeadRunContextReady                Point = "lead_run_context_ready"
	PointDispatchMetricPublished            Point = "dispatch_metric_published"
	PointDispatchStarted                    Point = "dispatch_started"
	PointAgentStartPublicationStarted       Point = "agent_start_publication_started"
	PointAgentStartPublicationEnded         Point = "agent_start_publication_ended"
	PointAgentInvocationStarted             Point = "agent_invocation_started"
	PointBeginTurnStarted                   Point = "begin_turn_started"
	PointBeginTurnEnded                     Point = "begin_turn_ended"
	PointUserMemoryStarted                  Point = "user_memory_started"
	PointUserMemoryEnded                    Point = "user_memory_ended"
	PointRecallStarted                      Point = "recall_started"
	PointRecallEnded                        Point = "recall_ended"
	PointRecallProjectionStarted            Point = "recall_projection_started"
	PointRecallProjectionEnded              Point = "recall_projection_ended"
	PointActiveDomainStarted                Point = "active_domain_started"
	PointActiveDomainEnded                  Point = "active_domain_ended"
	PointConversationHeadPreparationStarted Point = "conversation_head_preparation_started"
	PointConversationHeadPreparationEnded   Point = "conversation_head_preparation_ended"
	PointConversationDetectorStarted        Point = "conversation_detector_started"
	PointConversationDetectorEnded          Point = "conversation_detector_ended"
	PointConversationRAMAcceptStarted       Point = "conversation_ram_accept_started"
	PointConversationRAMAcceptEnded         Point = "conversation_ram_accept_ended"
	PointCategoryStarted                    Point = "category_started"
	PointCategoryEnded                      Point = "category_ended"
	PointPromptPreparationStarted           Point = "prompt_preparation_started"
	PointPromptAssemblyStarted              Point = "prompt_assembly_started"
	PointPromptPreparationEnded             Point = "prompt_preparation_ended"
	PointGenerationRequestReady             Point = "generation_request_ready"
	PointGenerationProviderStart            Point = "generation_provider_started"
	PointGenerationProviderEnd              Point = "generation_provider_ended"
	PointGatewayRequestStarted              Point = "gateway_request_started"
	PointGatewayResponseHeaders             Point = "gateway_response_headers"
	PointFirstTextCallback                  Point = "first_text_callback"
	PointConversationCommitStarted          Point = "conversation_commit_started"
	PointConversationCommitEnded            Point = "conversation_commit_ended"
	PointResponsePublicationStarted         Point = "response_publication_started"
)

const SchemaVersion = 1

// Snapshot is the bounded public projection attached to one terminal response.
type Snapshot struct {
	SchemaVersion int                `json:"schema_version"`
	PointsMS      map[string]float64 `json:"points_ms"`
}

// Recorder stores elapsed monotonic offsets from the handler-entry origin.
type Recorder struct {
	startedAt time.Time
	mu        sync.Mutex
	pointsMS  map[string]float64
}

type contextKey struct{}
type bodyGenerationContextKey struct{}

// New creates a recorder with handler-entry already recorded at offset zero.
func New(startedAt time.Time) *Recorder {
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	return &Recorder{
		startedAt: startedAt,
		pointsMS:  map[string]float64{string(PointHandlerReceived): 0},
	}
}

func WithRecorder(ctx context.Context, recorder *Recorder) context.Context {
	if recorder == nil {
		return ctx
	}
	return context.WithValue(ctx, contextKey{}, recorder)
}

func FromContext(ctx context.Context) *Recorder {
	if ctx == nil {
		return nil
	}
	recorder, _ := ctx.Value(contextKey{}).(*Recorder)
	return recorder
}

// WithBodyGenerationContext marks provider calls that produce the user's chat
// response. Routing and other provider calls must keep their context unmarked.
func WithBodyGenerationContext(ctx context.Context) context.Context {
	if ctx == nil || FromContext(ctx) == nil {
		return ctx
	}
	return context.WithValue(ctx, bodyGenerationContextKey{}, true)
}

func IsBodyGenerationContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	marked, _ := ctx.Value(bodyGenerationContextKey{}).(bool)
	return marked
}

// Gateway boundary marks are restricted to body-generation calls so a prior
// routing request cannot claim the first HTTP timing points for the turn.
func MarkGatewayRequestStarted(ctx context.Context) {
	if IsBodyGenerationContext(ctx) {
		Mark(ctx, PointGatewayRequestStarted)
	}
}

func MarkGatewayResponseHeaders(ctx context.Context) {
	if IsBodyGenerationContext(ctx) {
		Mark(ctx, PointGatewayResponseHeaders)
	}
}

// Mark records the first observation of a known point. Unknown point names are
// ignored so the serialized snapshot remains bounded by this package's schema.
func Mark(ctx context.Context, point Point) {
	if recorder := FromContext(ctx); recorder != nil {
		recorder.Mark(point)
	}
}

func (r *Recorder) Mark(point Point) {
	if r == nil || !knownPoint(point) {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	key := string(point)
	if _, exists := r.pointsMS[key]; exists {
		return
	}
	r.pointsMS[key] = time.Since(r.startedAt).Seconds() * 1000
}

func (r *Recorder) Snapshot() Snapshot {
	if r == nil {
		return Snapshot{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	points := make(map[string]float64, len(r.pointsMS))
	for point, offset := range r.pointsMS {
		points[point] = offset
	}
	return Snapshot{SchemaVersion: SchemaVersion, PointsMS: points}
}

func knownPoint(point Point) bool {
	switch point {
	case PointHandlerReceived, PointRequestParsed, PointHandlerValidationEnded,
		PointHandlerSessionStarted, PointHandlerSessionEnded, PointAccepted, PointAsyncStart,
		PointProcessStarted, PointProcessSessionStarted, PointProcessSessionEnded,
		PointRootTaskPreparationStarted, PointRootTaskPreparationEnded, PointRootTaskCreated,
		PointInitialEventsStarted, PointInitialEventsEnded,
		PointMessageReceivedEventPublished, PointServerReceivedMetricPublished, PointSessionTurnLogAttemptEnded,
		PointPersonaObservationStarted, PointPersonaObservationEnded,
		PointPreRouteChecksStarted, PointPreRouteChecksEnded, PointTurnInputBuilt,
		PointRouteDecisionStarted, PointRouteDecided, PointClassificationStarted, PointClassificationEnded,
		PointTaskActivationStarted, PointTaskActivationEnded,
		PointTaskRoutingEventPublished, PointTaskExecutionPrepared,
		PointTaskAssignmentEventPublished, PointTaskAssignmentRecorded,
		PointTaskRootRunStarted, PointTaskExecutionRunStarted, PointTaskIdentityBound,
		PointLeadRunResolutionStarted, PointLeadRunResolutionEnded,
		PointRouteMetricPublished, PointSkillBootstrapStarted, PointSkillBootstrapEnded,
		PointTTSSessionSetupStarted, PointTTSSessionSetupEnded,
		PointLeadRunPersistenceStarted, PointLeadRunPersistenceEnded,
		PointLeadAgentRunSaved, PointLeadAgentStartEventAppended, PointLeadContextPackSaved,
		PointLeadRunContextReady, PointDispatchMetricPublished, PointDispatchStarted,
		PointAgentStartPublicationStarted, PointAgentStartPublicationEnded, PointAgentInvocationStarted,
		PointBeginTurnStarted, PointBeginTurnEnded,
		PointUserMemoryStarted, PointUserMemoryEnded, PointRecallStarted, PointRecallEnded,
		PointRecallProjectionStarted, PointRecallProjectionEnded,
		PointActiveDomainStarted, PointActiveDomainEnded,
		PointConversationHeadPreparationStarted, PointConversationHeadPreparationEnded,
		PointConversationDetectorStarted, PointConversationDetectorEnded,
		PointConversationRAMAcceptStarted, PointConversationRAMAcceptEnded,
		PointCategoryStarted, PointCategoryEnded,
		PointPromptPreparationStarted, PointPromptAssemblyStarted, PointPromptPreparationEnded,
		PointGenerationRequestReady, PointGenerationProviderStart, PointGenerationProviderEnd,
		PointGatewayRequestStarted, PointGatewayResponseHeaders, PointFirstTextCallback,
		PointConversationCommitStarted, PointConversationCommitEnded, PointResponsePublicationStarted:
		return true
	default:
		return false
	}
}
