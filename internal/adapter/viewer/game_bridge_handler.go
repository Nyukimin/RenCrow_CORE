package viewer

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

const gameBridgeMaxBodyBytes int64 = 64 * 1024

// GameBridgeStatusOptions reports the currently active bridge mode.
type GameBridgeStatusOptions struct {
	ResultMode                string
	MemoryMode                string
	DecisionMode              string
	ConversationEngineEnabled bool
	L1StoreEnabled            bool
	SupportedGames            []string
	DefaultPersona            string
}

// GameActionStep is one executable action step returned to RenCrow_GAMES.
type GameActionStep struct {
	Action string         `json:"action"`
	Target string         `json:"target,omitempty"`
	Args   map[string]any `json:"args,omitempty"`
}

// GameBrainDecision is the bridge decision response.
type GameBrainDecision struct {
	AgentID    string           `json:"agent_id,omitempty"`
	Persona    string           `json:"persona"`
	Intent     string           `json:"intent"`
	Reason     string           `json:"reason"`
	ActionPlan []GameActionStep `json:"action_plan"`
	MemoryRefs []string         `json:"memory_refs"`
	Confidence float64          `json:"confidence"`
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
	if strings.TrimSpace(opts.ResultMode) == "" {
		opts.ResultMode = "candidate_ack"
	}
	if strings.TrimSpace(opts.MemoryMode) == "" {
		opts.MemoryMode = "candidate_only"
	}
	if strings.TrimSpace(opts.DecisionMode) == "" {
		opts.DecisionMode = "unavailable"
	}
	if len(opts.SupportedGames) == 0 {
		opts.SupportedGames = []string{"herzog_zwei", "territory_commander", "survival_garden", "nethack"}
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
			"result_endpoint":             "/viewer/games/result",
			"conversation_engine_enabled": opts.ConversationEngineEnabled,
			"l1_store_enabled":            opts.L1StoreEnabled,
			"supported_games":             opts.SupportedGames,
			"default_persona":             opts.DefaultPersona,
			"result_mode":                 opts.ResultMode,
			"memory_mode":                 opts.MemoryMode,
			"decision_mode":               opts.DecisionMode,
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
		eventID := gameBridgeEventID(req.GameID, req.SessionID, req.Turn, req.Persona)
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

func decodeGameBridgeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, gameBridgeMaxBodyBytes))
	return dec.Decode(dst)
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

func gameResultBool(result map[string]any, key string) (bool, bool) {
	value, ok := result[key]
	if !ok {
		return false, false
	}
	v, ok := value.(bool)
	return v, ok
}

func gameBridgeEventID(gameID, sessionID string, turn int, persona string) string {
	return fmt.Sprintf(
		"game:%s:%s:turn_%d:persona_%s",
		sanitizeGameBridgeID(gameID),
		sanitizeGameBridgeID(sessionID),
		turn,
		sanitizeGameBridgeID(persona),
	)
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
