package viewer

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

const gameBridgeMaxBodyBytes int64 = 64 * 1024

// GameBridgeStatusOptions reports the currently active bridge mode.
type GameBridgeStatusOptions struct {
	DecisionMode              string
	ResultMode                string
	MemoryMode                string
	ConversationEngineEnabled bool
	L1StoreEnabled            bool
	LLMRouterEnabled          bool
	SupportedGames            []string
	DefaultPersona            string
}

// GameObservationRequest is the synchronous RenCrow_GAMES -> rencrow decision
// contract. Game rules and validation stay in RenCrow_GAMES; rencrow owns the
// persona/recall/memory side of the bridge.
type GameObservationRequest struct {
	GameID           string         `json:"game_id"`
	SessionID        string         `json:"session_id"`
	Turn             int            `json:"turn"`
	Persona          string         `json:"persona"`
	Observation      map[string]any `json:"observation"`
	AvailableActions []string       `json:"available_actions"`
	Request          string         `json:"request"`
}

// GameActionStep is one executable action step returned to RenCrow_GAMES.
type GameActionStep struct {
	Action string         `json:"action"`
	Target string         `json:"target,omitempty"`
	Args   map[string]any `json:"args,omitempty"`
}

// GameBrainDecision is the bridge decision response.
type GameBrainDecision struct {
	Persona    string           `json:"persona"`
	Intent     string           `json:"intent"`
	Reason     string           `json:"reason"`
	ActionPlan []GameActionStep `json:"action_plan"`
	MemoryRefs []string         `json:"memory_refs"`
	Confidence float64          `json:"confidence"`
}

// GameBridgeDecisionOptions configures how /viewer/games/decision gathers
// recall context and produces the decision.
type GameBridgeDecisionOptions struct {
	RecallReader GameBridgeRecallReader
	Generator    GameDecisionGenerator
}

// GameDecisionGenerator produces a BrainDecision from a validated game request.
type GameDecisionGenerator interface {
	GenerateGameDecision(r *http.Request, req GameObservationRequest, recentEvents []GameBridgeEvent) (GameBrainDecision, error)
}

type GameDecisionGenerationError struct {
	StatusCode int
	Err        error
}

func (e GameDecisionGenerationError) Error() string {
	if e.Err == nil {
		return "game decision generation failed"
	}
	return e.Err.Error()
}

func (e GameDecisionGenerationError) Unwrap() error {
	return e.Err
}

// GameResultRequest reports the executed game turn back to rencrow.
type GameResultRequest struct {
	GameID          string            `json:"game_id"`
	SessionID       string            `json:"session_id"`
	Turn            int               `json:"turn"`
	Persona         string            `json:"persona"`
	Decision        GameBrainDecision `json:"decision"`
	ExecutedActions []string          `json:"executed_actions"`
	Result          map[string]any    `json:"result"`
}

// HandleGameBridgeStatus reports bridge availability without touching LLM or
// long-term memory.
func HandleGameBridgeStatus(opts GameBridgeStatusOptions) http.HandlerFunc {
	if strings.TrimSpace(opts.DecisionMode) == "" {
		opts.DecisionMode = "deterministic_stub"
	}
	if strings.TrimSpace(opts.ResultMode) == "" {
		opts.ResultMode = "candidate_ack"
	}
	if strings.TrimSpace(opts.MemoryMode) == "" {
		opts.MemoryMode = "candidate_only"
	}
	if len(opts.SupportedGames) == 0 {
		opts.SupportedGames = []string{"survival_garden", "territory_commander"}
	}
	if strings.TrimSpace(opts.DefaultPersona) == "" {
		opts.DefaultPersona = "mio"
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":                          true,
			"decision_endpoint":           "/viewer/games/decision",
			"result_endpoint":             "/viewer/games/result",
			"conversation_engine_enabled": opts.ConversationEngineEnabled,
			"l1_store_enabled":            opts.L1StoreEnabled,
			"llm_router_enabled":          opts.LLMRouterEnabled,
			"supported_games":             opts.SupportedGames,
			"default_persona":             opts.DefaultPersona,
			"decision_mode":               opts.DecisionMode,
			"result_mode":                 opts.ResultMode,
			"memory_mode":                 opts.MemoryMode,
			"endpoints": []string{
				"/viewer/games/status",
				"/viewer/games/decision",
				"/viewer/games/result",
				"/viewer/games/sessions",
				"/viewer/games/events",
			},
		})
	}
}

// HandleGameBridgeDecision returns a synchronous Phase 1 decision. It is
// intentionally deterministic until the bridge is wired into recall/LLM.
func HandleGameBridgeDecision(options ...GameBridgeDecisionOptions) http.HandlerFunc {
	var opts GameBridgeDecisionOptions
	if len(options) > 0 {
		opts = options[0]
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req GameObservationRequest
		if err := decodeGameBridgeJSON(w, r, &req); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		if err := validateGameObservationRequest(req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		recentEvents, err := recentGameBridgeEvents(r, opts.RecallReader, req)
		if err != nil {
			http.Error(w, "game bridge recall unavailable", http.StatusServiceUnavailable)
			return
		}
		decision, err := generateGameBridgeDecision(r, opts.Generator, req, recentEvents)
		if err != nil {
			http.Error(w, "game bridge decision unavailable", gameDecisionHTTPStatus(err))
			return
		}
		if err := validateGameDecision(decision, req.AvailableActions); err != nil {
			http.Error(w, "invalid bridge decision", http.StatusBadGateway)
			return
		}
		writeJSON(w, http.StatusOK, decision)
	}
}

// HandleGameBridgeResult accepts the post-execution result as a candidate
// memory event. Phase 1 acknowledges the event without promoting memory.
func HandleGameBridgeResult(writers ...GameBridgeResultWriter) http.HandlerFunc {
	var writer GameBridgeResultWriter
	if len(writers) > 0 {
		writer = writers[0]
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req GameResultRequest
		if err := decodeGameBridgeJSON(w, r, &req); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		if err := validateGameResultRequest(req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		eventID := gameBridgeEventID(req.GameID, req.SessionID, req.Turn)
		candidateMemoryIDs := []string{eventID + ":candidate"}
		if writer != nil {
			event, err := writer.SaveGameBridgeResult(r.Context(), req)
			if err != nil {
				http.Error(w, "failed to persist game result", http.StatusServiceUnavailable)
				return
			}
			eventID = event.EventID
			candidateMemoryIDs = []string{event.CandidateMemoryID}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":                   true,
			"event_id":             eventID,
			"candidate_memory_ids": candidateMemoryIDs,
			"memory_state":         "candidate",
			"promoted":             false,
		})
	}
}

func recentGameBridgeEvents(r *http.Request, reader GameBridgeRecallReader, req GameObservationRequest) ([]GameBridgeEvent, error) {
	if reader == nil {
		return nil, nil
	}
	return reader.RecentGameBridgeEvents(r.Context(), req.GameID, req.SessionID, 3)
}

func generateGameBridgeDecision(r *http.Request, generator GameDecisionGenerator, req GameObservationRequest, recentEvents []GameBridgeEvent) (GameBrainDecision, error) {
	if generator == nil {
		return deterministicGameDecision(req, recentEvents), nil
	}
	decision, err := generator.GenerateGameDecision(r, req, recentEvents)
	if err != nil {
		return GameBrainDecision{}, err
	}
	decision.MemoryRefs = mergeGameBridgeMemoryRefs(decision.MemoryRefs, gameBridgeMemoryRefs(recentEvents))
	return decision, nil
}

func gameDecisionHTTPStatus(err error) int {
	var decisionErr GameDecisionGenerationError
	if errors.As(err, &decisionErr) && decisionErr.StatusCode != 0 {
		return decisionErr.StatusCode
	}
	return http.StatusBadGateway
}

func decodeGameBridgeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, gameBridgeMaxBodyBytes))
	return dec.Decode(dst)
}

func validateGameObservationRequest(req GameObservationRequest) error {
	if strings.TrimSpace(req.GameID) == "" {
		return fmt.Errorf("game_id is required")
	}
	if strings.TrimSpace(req.SessionID) == "" {
		return fmt.Errorf("session_id is required")
	}
	if req.Turn < 0 {
		return fmt.Errorf("turn must be non-negative")
	}
	if strings.TrimSpace(req.Persona) == "" {
		return fmt.Errorf("persona is required")
	}
	if strings.TrimSpace(req.Request) == "" {
		return fmt.Errorf("request is required")
	}
	if len(req.AvailableActions) == 0 {
		return fmt.Errorf("available_actions is required")
	}
	for _, action := range req.AvailableActions {
		if strings.TrimSpace(action) != "" {
			return nil
		}
	}
	return fmt.Errorf("available_actions must include at least one action")
}

func validateGameResultRequest(req GameResultRequest) error {
	if strings.TrimSpace(req.GameID) == "" {
		return fmt.Errorf("game_id is required")
	}
	if strings.TrimSpace(req.SessionID) == "" {
		return fmt.Errorf("session_id is required")
	}
	if req.Turn < 0 {
		return fmt.Errorf("turn must be non-negative")
	}
	if strings.TrimSpace(req.Persona) == "" {
		return fmt.Errorf("persona is required")
	}
	if req.Result == nil {
		return fmt.Errorf("result is required")
	}
	success, ok := gameResultBool(req.Result, "success")
	if !ok {
		return fmt.Errorf("result.success is required")
	}
	gameOver, _ := gameResultBool(req.Result, "game_over")
	if len(req.ExecutedActions) == 0 && success && !gameOver {
		return fmt.Errorf("executed_actions is required for successful non-game-over turns")
	}
	return nil
}

func deterministicGameDecision(req GameObservationRequest, recentEvents []GameBridgeEvent) GameBrainDecision {
	actions := gameActionSet(req.AvailableActions)
	fatigue, _ := gameNestedNumber(req.Observation, "status", "fatigue")
	thirst, _ := gameNestedNumber(req.Observation, "status", "thirst")
	hunger, _ := gameNestedNumber(req.Observation, "status", "hunger")
	rainRisk := gameObservationHasString(req.Observation, "visible_events", "rain_clouds") ||
		gameObservationHasString(req.Observation, "visible_events", "storm_clouds")
	nightRisk := strings.Contains(strings.ToLower(gameObservationString(req.Observation, "time")), "night")

	intent := firstAvailableAction(req.AvailableActions)
	reason := "fallback first available action"
	switch {
	case actions["return_to_camp"] && (fatigue >= 70 || rainRisk || nightRisk):
		intent = "return_to_camp"
		reason = "camp is safer than continuing while risk is high"
	case actions["drink"] && thirst >= 55:
		intent = "drink"
		reason = "thirst is the most urgent visible need"
	case actions["fish"] && hunger >= 60:
		intent = "fish"
		reason = "hunger is high and fishing is available"
	case actions["rest"] && fatigue >= 70:
		intent = "rest"
		reason = "fatigue is high"
	}

	plan := make([]GameActionStep, 0, 3)
	if intent == "return_to_camp" && actions["drink"] && thirst >= 55 {
		plan = append(plan, GameActionStep{Action: "drink"})
	}
	plan = append(plan, GameActionStep{Action: intent})
	if intent == "return_to_camp" && actions["rest"] && fatigue >= 70 {
		plan = append(plan, GameActionStep{Action: "rest"})
	}
	memoryRefs := gameBridgeMemoryRefs(recentEvents)
	if len(memoryRefs) > 0 {
		reason += "; recent candidate game context is available"
	}
	return GameBrainDecision{
		Persona:    strings.TrimSpace(req.Persona),
		Intent:     intent,
		Reason:     reason,
		ActionPlan: plan,
		MemoryRefs: memoryRefs,
		Confidence: 0.52,
	}
}

func gameBridgeMemoryRefs(events []GameBridgeEvent) []string {
	if len(events) == 0 {
		return []string{}
	}
	refs := make([]string, 0, len(events))
	seen := map[string]bool{}
	for _, event := range events {
		ref := strings.TrimSpace(event.CandidateMemoryID)
		if ref == "" {
			ref = strings.TrimSpace(event.EventID) + ":candidate"
		}
		if ref == ":candidate" || seen[ref] {
			continue
		}
		seen[ref] = true
		refs = append(refs, ref)
	}
	return refs
}

func mergeGameBridgeMemoryRefs(primary []string, appended []string) []string {
	if len(primary) == 0 && len(appended) == 0 {
		return []string{}
	}
	refs := make([]string, 0, len(primary)+len(appended))
	seen := map[string]bool{}
	for _, ref := range append(primary, appended...) {
		ref = strings.TrimSpace(ref)
		if ref == "" || seen[ref] {
			continue
		}
		seen[ref] = true
		refs = append(refs, ref)
	}
	return refs
}

func validateGameDecision(decision GameBrainDecision, availableActions []string) error {
	actions := gameActionSet(availableActions)
	if strings.TrimSpace(decision.Persona) == "" {
		return fmt.Errorf("persona is required")
	}
	if !actions[strings.TrimSpace(decision.Intent)] {
		return fmt.Errorf("intent is unavailable")
	}
	if len(decision.ActionPlan) == 0 {
		return fmt.Errorf("action_plan is required")
	}
	for _, step := range decision.ActionPlan {
		if !actions[strings.TrimSpace(step.Action)] {
			return fmt.Errorf("action_plan includes unavailable action")
		}
	}
	if decision.Confidence < 0 || decision.Confidence > 1 {
		return fmt.Errorf("confidence must be between 0 and 1")
	}
	return nil
}

func firstAvailableAction(actions []string) string {
	for _, action := range actions {
		if trimmed := strings.TrimSpace(action); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func gameActionSet(actions []string) map[string]bool {
	out := make(map[string]bool, len(actions))
	for _, action := range actions {
		if trimmed := strings.TrimSpace(action); trimmed != "" {
			out[trimmed] = true
		}
	}
	return out
}

func gameNestedNumber(root map[string]any, keys ...string) (float64, bool) {
	var current any = root
	for _, key := range keys {
		m, ok := current.(map[string]any)
		if !ok {
			return 0, false
		}
		current, ok = m[key]
		if !ok {
			return 0, false
		}
	}
	switch v := current.(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case json.Number:
		n, err := v.Float64()
		return n, err == nil
	default:
		return 0, false
	}
}

func gameObservationString(root map[string]any, key string) string {
	value, ok := root[key]
	if !ok {
		return ""
	}
	s, _ := value.(string)
	return strings.TrimSpace(s)
}

func gameObservationHasString(root map[string]any, key string, want string) bool {
	value, ok := root[key]
	if !ok {
		return false
	}
	for _, item := range anySlice(value) {
		if strings.EqualFold(strings.TrimSpace(item), want) {
			return true
		}
	}
	return false
}

func anySlice(value any) []string {
	switch v := value.(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func gameResultBool(result map[string]any, key string) (bool, bool) {
	value, ok := result[key]
	if !ok {
		return false, false
	}
	v, ok := value.(bool)
	return v, ok
}

func gameBridgeEventID(gameID, sessionID string, turn int) string {
	return fmt.Sprintf("game:%s:%s:turn_%d", sanitizeGameBridgeID(gameID), sanitizeGameBridgeID(sessionID), turn)
}

func sanitizeGameBridgeID(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "unknown"
	}
	return out
}
