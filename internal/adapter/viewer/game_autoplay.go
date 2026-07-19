package viewer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

// autoplay ランナーの安全クランプ (RenCrow_GAMES/docs/10 §2)。
// ペースは LLM が next_check_minutes で自己決定し、ここでは暴走だけを防ぐ。
const (
	gameAutoplayMinCheck     = 5 * time.Minute
	gameAutoplayMaxCheck     = 24 * time.Hour
	gameAutoplayDefaultCheck = 60 * time.Minute
	// gameAutoplayBusyRetry は自発起動したセッションがまだ実行中の時の再確認間隔。
	gameAutoplayBusyRetry = 10 * time.Minute
	// gameAutoplayFirstCheck は起動直後の初回確認までの猶予。
	gameAutoplayFirstCheck            = 5 * time.Minute
	gameAutoplayDefaultDailyCap       = 8
	gameAutoplayDecisionTimeout       = 30 * time.Second
	gameAutoplayLeaderboardTimeout    = 5 * time.Second
	gameAutoplayLeaderboardEntryLimit = 10
	gameAutoplayRecentSessionLimit    = 5
)

var gameAutoplayDefaultPersonas = []string{"mio", "shiro", "midori"}

// GameBridgeSessionLister は autoplay のプロンプト材料（直近セッション要約）。
type GameBridgeSessionLister interface {
	RecentGameBridgeSessions(context.Context, int) ([]GameBridgeSessionSummary, int, error)
}

// GameAutoplayOptions は autoplay ランナーの依存。
type GameAutoplayOptions struct {
	Provider llm.LLMProvider
	// Launch は PerformGameLaunch に渡す設定（observer base URL / 動機記録先）。
	Launch GameLaunchOptions
	// Recall は nil 可（プロンプトから直近セッション情報が抜けるだけ）。
	Recall            GameBridgeSessionLister
	Personas          []string
	MaxSessionsPerDay int
	HTTPClient        *http.Client
	Now               func() time.Time
}

// gameAutoplayDecision は LLM が返す strict JSON。
type gameAutoplayDecision struct {
	Play             bool     `json:"play"`
	GameID           string   `json:"game_id"`
	Personas         []string `json:"personas"`
	Reason           string   `json:"reason"`
	NextCheckMinutes int      `json:"next_check_minutes"`
}

// GameAutoplayService はペルソナの「遊びたい」を定期的に LLM へ問い、
// play=true なら PerformGameLaunch で自発起動する常駐ランナー
// (RenCrow_GAMES/docs/10_RenCrow自発プレイ仕様.md)。
type GameAutoplayService struct {
	provider llm.LLMProvider
	launch   GameLaunchOptions
	recall   GameBridgeSessionLister
	personas []string
	dailyCap int
	client   *http.Client
	now      func() time.Time

	mu            sync.Mutex
	dayKey        string
	dayCount      int
	lastSessionID string

	stopOnce sync.Once
	cancel   context.CancelFunc
	done     chan struct{}
	started  bool
}

func NewGameAutoplayService(opts GameAutoplayOptions) *GameAutoplayService {
	if opts.Provider == nil {
		return nil
	}
	personas := make([]string, 0, len(opts.Personas))
	for _, persona := range opts.Personas {
		if persona = strings.ToLower(strings.TrimSpace(persona)); persona != "" {
			personas = append(personas, persona)
		}
	}
	if len(personas) == 0 {
		personas = append(personas, gameAutoplayDefaultPersonas...)
	}
	dailyCap := opts.MaxSessionsPerDay
	if dailyCap <= 0 {
		dailyCap = gameAutoplayDefaultDailyCap
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: gameAutoplayLeaderboardTimeout}
	}
	now := opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &GameAutoplayService{
		provider: opts.Provider,
		launch:   opts.Launch,
		recall:   opts.Recall,
		personas: personas,
		dailyCap: dailyCap,
		client:   client,
		now:      now,
		done:     make(chan struct{}),
	}
}

// Start はランナーを起動する（二重 Start は無視）。
func (s *GameAutoplayService) Start() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return
	}
	s.started = true
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.mu.Unlock()

	go func() {
		defer close(s.done)
		delay := gameAutoplayFirstCheck
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
				delay = s.RunOnce(ctx)
			}
		}
	}()
}

// Stop はランナーを停止する（冪等）。
func (s *GameAutoplayService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		s.mu.Lock()
		cancel := s.cancel
		started := s.started
		s.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		if started {
			<-s.done
		}
	})
}

// RunOnce は 1 回の「遊びたいか」判断を行い、次に考えるまでの間隔を返す。
func (s *GameAutoplayService) RunOnce(ctx context.Context) time.Duration {
	if s.sessionStillRunning(ctx) {
		return gameAutoplayBusyRetry
	}
	if !s.underDailyCap() {
		return gameAutoplayDefaultCheck
	}

	decision, err := s.askProvider(ctx)
	if err != nil {
		log.Printf("[GamesAutoplay] decision unavailable: %v", err)
		return gameAutoplayDefaultCheck
	}
	next := clampGameAutoplayCheck(decision.NextCheckMinutes)
	if !decision.Play {
		log.Printf("[GamesAutoplay] decided not to play (next check in %s)", next)
		return next
	}

	personas := s.filterToRoster(decision.Personas)
	if strings.TrimSpace(decision.GameID) == "" || len(personas) == 0 {
		log.Printf("[GamesAutoplay] play decision missing game_id or personas, skipped: %+v", decision)
		return next
	}
	result, _, err := PerformGameLaunch(ctx, s.launch, GameLaunchRequest{
		GameID:   strings.TrimSpace(decision.GameID),
		Personas: personas,
		Reason:   strings.TrimSpace(decision.Reason),
	})
	if err != nil {
		log.Printf("[GamesAutoplay] launch failed: %v", err)
		return next
	}
	s.recordLaunch(result.SessionID)
	log.Printf("[GamesAutoplay] launched %s session=%s personas=%v reason=%q (next check in %s)",
		result.GameID, result.SessionID, personas, decision.Reason, next)
	return next
}

func clampGameAutoplayCheck(minutes int) time.Duration {
	if minutes <= 0 {
		return gameAutoplayDefaultCheck
	}
	next := time.Duration(minutes) * time.Minute
	if next < gameAutoplayMinCheck {
		return gameAutoplayMinCheck
	}
	if next > gameAutoplayMaxCheck {
		return gameAutoplayMaxCheck
	}
	return next
}

func (s *GameAutoplayService) filterToRoster(personas []string) []string {
	roster := map[string]bool{}
	for _, persona := range s.personas {
		roster[persona] = true
	}
	out := make([]string, 0, len(personas))
	seen := map[string]bool{}
	for _, persona := range personas {
		persona = strings.ToLower(strings.TrimSpace(persona))
		if persona == "" || !roster[persona] || seen[persona] {
			continue
		}
		seen[persona] = true
		out = append(out, persona)
	}
	return out
}

func (s *GameAutoplayService) underDailyCap() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	today := s.now().Format("2006-01-02")
	if s.dayKey != today {
		s.dayKey = today
		s.dayCount = 0
	}
	return s.dayCount < s.dailyCap
}

func (s *GameAutoplayService) recordLaunch(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	today := s.now().Format("2006-01-02")
	if s.dayKey != today {
		s.dayKey = today
		s.dayCount = 0
	}
	s.dayCount++
	s.lastSessionID = sessionID
}

// sessionStillRunning は直前に自発起動したセッションがまだ実行中かを observer に
// 問い合わせる。到達不能・不明時は false（起動判断そのものは LLM に委ねる）。
func (s *GameAutoplayService) sessionStillRunning(ctx context.Context) bool {
	s.mu.Lock()
	sessionID := s.lastSessionID
	s.mu.Unlock()
	if sessionID == "" {
		return false
	}
	baseURL, err := parseGameObserverBaseURL(s.launch.ObserverBaseURL)
	if err != nil {
		return false
	}
	target := strings.TrimRight(baseURL.String(), "/") + "/games/sessions/" + url.PathEscape(sessionID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return false
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var payload struct {
		Session struct {
			Status string `json:"status"`
		} `json:"session"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		return false
	}
	return payload.Session.Status == "running"
}

func (s *GameAutoplayService) fetchLeaderboard(ctx context.Context) []map[string]any {
	baseURL, err := parseGameObserverBaseURL(s.launch.ObserverBaseURL)
	if err != nil {
		return nil
	}
	target := strings.TrimRight(baseURL.String(), "/") + "/games/leaderboard?limit=" + fmt.Sprint(gameAutoplayLeaderboardEntryLimit)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var payload struct {
		Entries []map[string]any `json:"entries"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		return nil
	}
	return payload.Entries
}

func (s *GameAutoplayService) recentSessions(ctx context.Context) []GameBridgeSessionSummary {
	if s.recall == nil {
		return nil
	}
	sessions, _, err := s.recall.RecentGameBridgeSessions(ctx, gameAutoplayRecentSessionLimit)
	if err != nil {
		return nil
	}
	return sessions
}

func (s *GameAutoplayService) askProvider(ctx context.Context) (gameAutoplayDecision, error) {
	ctx, cancel := context.WithTimeout(ctx, gameAutoplayDecisionTimeout)
	defer cancel()

	payload := map[string]any{
		"personas": s.personas,
		"titles": map[string]any{
			"herzog_zwei":         "1-4人対戦RTS (足りない陣営はbot)",
			"territory_commander": "1-2人 (2人でLLM対戦)",
			"survival_garden":     "1-4人の独立生存 (遭遇あり)",
			"nethack":             "1人 (スコア争い)",
		},
		"leaderboard":     s.fetchLeaderboard(ctx),
		"recent_sessions": s.recentSessions(ctx),
		"now":             s.now().Format(time.RFC3339),
	}
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return gameAutoplayDecision{}, err
	}
	resp, err := s.provider.Generate(ctx, llm.GenerateRequest{
		SystemPrompt: gameAutoplaySystemPrompt,
		Messages: []llm.Message{
			{Role: "user", Content: "Autoplay input:\n" + string(encoded)},
		},
		MaxTokens:   400,
		Temperature: 0.6,
		ProviderOptions: map[string]any{
			"surface": "game_autoplay",
		},
	})
	if err != nil {
		return gameAutoplayDecision{}, err
	}
	return decodeGameAutoplayDecision(resp.Content)
}

const gameAutoplaySystemPrompt = `You decide whether the RenCrow personas feel like playing a game right now.
They play for their own reasons: revenge for a lost match, beating a rival's leaderboard score, curiosity, or just fun.
Not playing is a perfectly good decision. Do not play out of obligation.
Return only one strict JSON object:
{"play":false,"game_id":"","personas":[],"reason":"","next_check_minutes":60}
or
{"play":true,"game_id":"<title id>","personas":["<from the given roster>"],"reason":"<motive in Japanese, first person>","next_check_minutes":<when to consider playing again>}
Rules: personas must come from the given roster. Respect the per-title persona counts. next_check_minutes is required (how many minutes until you want to think about playing again). No markdown, no extra keys.`

func decodeGameAutoplayDecision(content string) (gameAutoplayDecision, error) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return gameAutoplayDecision{}, fmt.Errorf("empty autoplay decision response")
	}
	dec := json.NewDecoder(strings.NewReader(trimmed))
	dec.DisallowUnknownFields()
	var decision gameAutoplayDecision
	if err := dec.Decode(&decision); err != nil {
		return gameAutoplayDecision{}, fmt.Errorf("decode autoplay decision json: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return gameAutoplayDecision{}, fmt.Errorf("autoplay decision response contains trailing content")
	}
	return decision, nil
}
