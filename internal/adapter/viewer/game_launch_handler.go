package viewer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const defaultGameLaunchTimeout = 10 * time.Second

// GameLaunchOptions は POST /viewer/games/launch の依存
// (docs/02_正本仕様/09_Game_Bridge_Observer_API.md 11章)。
type GameLaunchOptions struct {
	// ObserverBaseURL は共有 observer の base URL。空なら observer proxy と
	// 同じ解決順（RENCROW_GAMES_OBSERVER_URL > 既定）で解決する。
	ObserverBaseURL string
	HTTPClient      *http.Client
	// Store があれば、起動成功時に動機 (reason) を candidate log へ記録する。
	Store GameBridgeResultWriter
}

type GameLaunchRequest struct {
	GameID   string   `json:"game_id"`
	Personas []string `json:"personas,omitempty"`
	Turns    int      `json:"turns,omitempty"`
	Mode     string   `json:"mode,omitempty"`
	Reason   string   `json:"reason,omitempty"`
}


// HandleGameLaunch は RenCrow のペルソナが「遊びたい時に起動する」ための
// 起動口。共有 observer の POST /games/launch へ転送し、動機を候補記憶として
// 残す（マルチペルソナ WP5）。
func HandleGameLaunch(opts GameLaunchOptions) http.HandlerFunc {
	baseURL, baseErr := parseGameObserverBaseURL(opts.ObserverBaseURL)
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: defaultGameLaunchTimeout}
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var request GameLaunchRequest
		if err := decodeGameBridgeJSON(w, r, &request); err != nil {
			return
		}
		if err := validateGameLaunchRequest(request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if baseErr != nil {
			http.Error(w, "game observer upstream unavailable", http.StatusServiceUnavailable)
			return
		}

		payload, err := json.Marshal(map[string]any{
			"game_id":  strings.TrimSpace(request.GameID),
			"personas": request.Personas,
			"turns":    request.Turns,
			"mode":     strings.TrimSpace(request.Mode),
		})
		if err != nil {
			http.Error(w, "encode launch request failed", http.StatusInternalServerError)
			return
		}
		upstream := strings.TrimRight(baseURL.String(), "/") + "/games/launch"
		req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstream, bytes.NewReader(payload))
		if err != nil {
			http.Error(w, "game observer upstream unavailable", http.StatusServiceUnavailable)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			http.Error(w, "game observer upstream unavailable", http.StatusServiceUnavailable)
			return
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			http.Error(w, "game observer upstream unavailable", http.StatusServiceUnavailable)
			return
		}
		if resp.StatusCode != http.StatusOK {
			message := strings.TrimSpace(string(body))
			if message == "" {
				message = resp.Status
			}
			http.Error(w, message, resp.StatusCode)
			return
		}
		var launched struct {
			OK        bool   `json:"ok"`
			GameID    string `json:"game_id"`
			SessionID string `json:"session_id"`
			Status    string `json:"status"`
		}
		if err := json.Unmarshal(body, &launched); err != nil || launched.SessionID == "" {
			http.Error(w, "invalid launch response from observer", http.StatusBadGateway)
			return
		}

		motiveRecorded := recordGameLaunchMotive(r, opts.Store, request, launched.SessionID)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":              true,
			"game_id":         launched.GameID,
			"session_id":      launched.SessionID,
			"status":          launched.Status,
			"motive_recorded": motiveRecorded,
		})
	}
}

// validateGameLaunchRequest は contract レベルの最小検証のみ行う。
// タイトル・人数の capability 検証は observer 側 launcher が正本であり、
// その 400 を透過する（二重管理によるドリフトを避ける。WP5 残課題 B-2）。
func validateGameLaunchRequest(request GameLaunchRequest) error {
	if strings.TrimSpace(request.GameID) == "" {
		return fmt.Errorf("game_id is required")
	}
	return nil
}

// recordGameLaunchMotive は起動の動機を参加ペルソナ全員の candidate
// イベントとして残す（WP5 残課題 B-3: 誘われた側にも経験候補を残す）。
// event id の重複排除キーが (game, session, turn) のため、i 番目の
// ペルソナは Turn=-(i+1) で記録する（-1 = 言い出しっぺ）。
// observer は launching を楽観返却するため、spawn がその後失敗しても
// 動機イベントは残る（「遊ぼうとした」経験として扱う。仕様 docs/06）。
// 記録失敗は起動失敗にしない（正は observer 側の起動）。
func recordGameLaunchMotive(r *http.Request, store GameBridgeResultWriter, request GameLaunchRequest, sessionID string) bool {
	reason := strings.TrimSpace(request.Reason)
	if store == nil || reason == "" {
		return false
	}
	personas := make([]string, 0, len(request.Personas))
	for _, persona := range request.Personas {
		if persona = strings.TrimSpace(persona); persona != "" {
			personas = append(personas, persona)
		}
	}
	if len(personas) == 0 {
		personas = []string{"mio"}
	}
	recorded := false
	for i, persona := range personas {
		intent := "play_game"
		personaReason := reason
		invitedBy := ""
		if i > 0 {
			intent = "invited_to_play"
			personaReason = personas[0] + "に誘われて参加: " + reason
			invitedBy = personas[0]
		}
		_, err := store.SaveGameBridgeResult(r.Context(), GameResultRequest{
			GameID:    strings.TrimSpace(request.GameID),
			SessionID: sessionID,
			Turn:      -(i + 1),
			Persona:   persona,
			Decision: GameBrainDecision{
				Persona:    persona,
				Intent:     intent,
				Reason:     personaReason,
				Confidence: 1,
			},
			ExecutedActions: []string{"launch"},
			Result: map[string]any{
				"launch":     true,
				"personas":   personas,
				"invited_by": invitedBy,
				"reason":     reason,
			},
		})
		if err == nil {
			recorded = true
		}
	}
	return recorded
}
