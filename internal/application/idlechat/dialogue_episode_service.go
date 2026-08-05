package idlechat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// DialogueEpisodeService prepares a complete dialogue through CodexExe, then
// validates it turn-by-turn in CORE. Only the suffix beginning at the first
// invalid turn may be regenerated.
type DialogueEpisodeService struct {
	path                   string
	generator              IdleChatCodexGenerator
	personas               map[string]string
	config                 DialogueInterestingnessConfig
	maxSuffixRegenerations int
	mu                     sync.Mutex
}

func (o *IdleChatOrchestrator) SetDialogueEpisodeService(service *DialogueEpisodeService) {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.dialogueEpisodeService = service
	o.mu.Unlock()
}

func (o *IdleChatOrchestrator) prepareDialogueEpisode(sessionID string, result TopicGenerationResult, turnCount int) (DialogueEpisodeArtifact, error) {
	if o == nil {
		return DialogueEpisodeArtifact{}, errors.New("idlechat orchestrator is nil")
	}
	o.mu.Lock()
	service := o.dialogueEpisodeService
	o.mu.Unlock()
	if service == nil {
		return DialogueEpisodeArtifact{}, errors.New("dialogue CodexExe producer is not configured")
	}
	return service.Prepare(o.idleRunContext(), sessionID, result, turnCount)
}

func NewPersistentDialogueEpisodeService(path string, generator IdleChatCodexGenerator, personas map[string]string, config DialogueInterestingnessConfig) *DialogueEpisodeService {
	cloned := make(map[string]string, len(personas))
	for name, prompt := range personas {
		cloned[strings.ToLower(strings.TrimSpace(name))] = strings.TrimSpace(prompt)
	}
	return &DialogueEpisodeService{
		path:                   strings.TrimSpace(path),
		generator:              generator,
		personas:               cloned,
		config:                 normalizeDialogueInterestingnessConfig(config),
		maxSuffixRegenerations: 3,
	}
}

func (s *DialogueEpisodeService) SetMaxSuffixRegenerations(limit int) {
	if s != nil && limit > 0 {
		s.maxSuffixRegenerations = limit
	}
}

func (s *DialogueEpisodeService) Prepare(ctx context.Context, sessionID string, result TopicGenerationResult, turnCount int) (DialogueEpisodeArtifact, error) {
	if s == nil || s.generator == nil {
		return DialogueEpisodeArtifact{}, errors.New("dialogue CodexExe producer is not configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if turnCount <= 0 {
		turnCount = normalizeDialogueInterestingnessConfig(s.config).MaxTurnsPerTopic
	}
	if turnCount > maxTurnsPerTopic && result.Category != TopicCategoryForecast {
		turnCount = maxTurnsPerTopic
	}
	director := NewDialogueDirector(s.config)
	plan := director.BuildArcPlan(result)
	result.Category = plan.Category
	plan.TurnPlans = buildDialogueTurnPlans(turnCount, dialogueCategorySpec(result.Category))
	raw, err := s.generator.Generate(ctx, s.generationPrompt(sessionID, result, plan, turnCount))
	if err != nil {
		return DialogueEpisodeArtifact{}, fmt.Errorf("CodexExe dialogue generation: %w", err)
	}
	var generated struct {
		Turns []DialogueEpisodeTurn `json:"turns"`
	}
	if err := decodeStoryJSON(raw, &generated); err != nil {
		return DialogueEpisodeArtifact{}, fmt.Errorf("decode CodexExe dialogue: %w", err)
	}
	now := time.Now().UTC()
	artifact := DialogueEpisodeArtifact{
		SchemaVersion:    DialogueEpisodeSchemaVersion,
		EpisodeID:        "dialogue-" + uuid.NewString(),
		GenerationID:     "dialogue-generation-" + uuid.NewString(),
		Revision:         1,
		SessionID:        strings.TrimSpace(sessionID),
		InitiatedBy:      "shiro",
		TopicResult:      result,
		ArcPlan:          plan,
		Participants:     []string{"mio", "shiro"},
		Turns:            normalizeDialogueTurns(generated.Turns, 0),
		ProductionStatus: DialogueProductionValidating,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	artifact.Validation = ValidateDialogueEpisode(artifact, s.config)
	setDialogueProductionStatus(&artifact, s.maxSuffixRegenerations)
	if err := s.append(artifact); err != nil {
		return artifact, err
	}
	for !artifact.Validation.Valid && artifact.SuffixRegenerations < s.maxSuffixRegenerations {
		artifact, err = s.repairSuffix(ctx, artifact)
		if err != nil {
			return artifact, err
		}
	}
	if !artifact.Validation.Valid {
		return artifact, fmt.Errorf("dialogue episode %s failed validation at turn %d", artifact.EpisodeID, artifact.Validation.FirstInvalidTurn)
	}
	return artifact, nil
}

func (s *DialogueEpisodeService) repairSuffix(ctx context.Context, artifact DialogueEpisodeArtifact) (DialogueEpisodeArtifact, error) {
	from := artifact.Validation.FirstInvalidTurn
	if from < 1 {
		from = 1
	}
	prefixLength := min(from-1, len(artifact.Turns))
	prefix := append([]DialogueEpisodeTurn(nil), artifact.Turns[:prefixLength]...)
	payload, err := json.Marshal(artifact)
	if err != nil {
		return artifact, err
	}
	prompt := fmt.Sprintf(`あなたはRenCrow IdleChatの対話suffix修復担当です。
turn %dより前はCORE検査合格済みで、本文・speaker・message_idを変更禁止です。
turn %d以降だけを最終turnまで再生成し、validation.errorsをすべて解消してください。
MioとShiroのSystemPrompt、content_mode、arc_plan、発話順を維持してください。
JSON以外を付けず、{"turns":[{"speaker":"mio","display_text":"本文","speech_text":"読み上げ本文"}]}だけを返してください。message_idは省略してください。
対象artifact:
%s`, from, from, string(payload))
	raw, err := s.generator.Generate(ctx, prompt)
	if err != nil {
		return artifact, fmt.Errorf("CodexExe dialogue suffix repair: %w", err)
	}
	var generated struct {
		Turns []DialogueEpisodeTurn `json:"turns"`
	}
	if err := decodeStoryJSON(raw, &generated); err != nil {
		return artifact, fmt.Errorf("decode CodexExe dialogue suffix: %w", err)
	}
	if len(generated.Turns) == 0 {
		return artifact, errors.New("CodexExe dialogue suffix repair returned no turns")
	}
	artifact.Turns = append(prefix, normalizeDialogueTurns(generated.Turns, prefixLength)...)
	artifact.Revision++
	artifact.FixedPrefixLength = prefixLength
	artifact.RepairFromTurn = from
	artifact.SuffixRegenerations++
	artifact.UpdatedAt = time.Now().UTC()
	artifact.Validation = ValidateDialogueEpisode(artifact, s.config)
	setDialogueProductionStatus(&artifact, s.maxSuffixRegenerations)
	if err := s.append(artifact); err != nil {
		return artifact, err
	}
	return artifact, nil
}

func normalizeDialogueTurns(turns []DialogueEpisodeTurn, prefixLength int) []DialogueEpisodeTurn {
	out := make([]DialogueEpisodeTurn, len(turns))
	for i, turn := range turns {
		turn.TurnIndex = prefixLength + i + 1
		turn.MessageID = newIdleChatMessageID()
		turn.Speaker = strings.ToLower(strings.TrimSpace(turn.Speaker))
		turn.DisplayText = ensureTrailingPeriod(strings.TrimSpace(turn.DisplayText))
		turn.SpeechText = ensureTrailingPeriod(strings.TrimSpace(turn.SpeechText))
		if turn.SpeechText == "" {
			turn.SpeechText = turn.DisplayText
		}
		out[i] = turn
	}
	return out
}

func setDialogueProductionStatus(artifact *DialogueEpisodeArtifact, maxRepairs int) {
	if artifact.Validation.Valid {
		artifact.ProductionStatus = DialogueProductionReady
	} else if artifact.SuffixRegenerations >= maxRepairs {
		artifact.ProductionStatus = DialogueProductionFailed
	} else {
		artifact.ProductionStatus = DialogueProductionNeedsRepair
	}
}

func ValidateDialogueEpisode(artifact DialogueEpisodeArtifact, config DialogueInterestingnessConfig) DialogueEpisodeValidation {
	validation := DialogueEpisodeValidation{Valid: true}
	wantTurns := len(artifact.ArcPlan.TurnPlans)
	if len(artifact.Turns) != wantTurns {
		turn := min(len(artifact.Turns)+1, wantTurns)
		if len(artifact.Turns) > wantTurns {
			turn = max(wantTurns, 1)
		}
		validation.Errors = append(validation.Errors, DialogueTurnValidationError{Code: "turn_count_violation", TurnIndex: turn, Evidence: fmt.Sprintf("turns=%d want=%d", len(artifact.Turns), wantTurns)})
	}
	checker := NewDialogueQualityChecker(config)
	director := NewDialogueDirector(config)
	state := director.NewArcState(artifact.SessionID, artifact.TopicResult, artifact.ArcPlan)
	lastBySpeaker := map[string]string{}
	transcript := make([]string, 0, len(artifact.Turns))
	if len(artifact.Participants) == 0 {
		validation.Errors = append(validation.Errors, DialogueTurnValidationError{Code: "participant_contract_violation", TurnIndex: 1, Evidence: "participants are required"})
		validation.Valid = false
		validation.FirstInvalidTurn = 1
		return validation
	}
	for i, turn := range artifact.Turns {
		turnIndex := i + 1
		if turnIndex > wantTurns {
			break
		}
		expectedSpeaker := artifact.Participants[i%len(artifact.Participants)]
		if turn.TurnIndex != turnIndex || strings.ToLower(strings.TrimSpace(turn.Speaker)) != expectedSpeaker {
			validation.Errors = append(validation.Errors, DialogueTurnValidationError{Code: "speaker_sequence_violation", TurnIndex: turnIndex, Evidence: fmt.Sprintf("speaker=%q want=%q", turn.Speaker, expectedSpeaker)})
			continue
		}
		text := strings.TrimSpace(turn.DisplayText)
		if text == "" || strings.TrimSpace(turn.SpeechText) == "" {
			validation.Errors = append(validation.Errors, DialogueTurnValidationError{Code: "empty_utterance", TurnIndex: turnIndex, Evidence: "display_text and speech_text are required"})
			continue
		}
		latestOther := ""
		if i > 0 {
			latestOther = artifact.Turns[i-1].DisplayText
		}
		quality := checker.Check(DialogueQualityInput{
			Category: artifact.TopicResult.Category, ContentMode: artifact.ArcPlan.ContentMode,
			Utterance: text, LatestOther: latestOther, LatestSelf: lastBySpeaker[expectedSpeaker],
			State: state, TurnPlan: artifact.ArcPlan.TurnPlans[i], Config: config,
		})
		if !quality.OK {
			validation.Errors = append(validation.Errors, DialogueTurnValidationError{Code: "turn_quality_violation", TurnIndex: turnIndex, Evidence: fmt.Sprintf("score=%d", quality.Score), Reasons: quality.Reasons})
		}
		if isResponseTooSimilar(text, transcript) {
			validation.Errors = append(validation.Errors, DialogueTurnValidationError{Code: "repetition_violation", TurnIndex: turnIndex, Evidence: "utterance is too similar to an earlier turn"})
		}
		state = director.UpdateArcState(state, text, artifact.ArcPlan.TurnPlans[i], quality)
		lastBySpeaker[expectedSpeaker] = text
		transcript = append(transcript, text)
	}
	for _, item := range validation.Errors {
		if item.TurnIndex > 0 && (validation.FirstInvalidTurn == 0 || item.TurnIndex < validation.FirstInvalidTurn) {
			validation.FirstInvalidTurn = item.TurnIndex
		}
	}
	validation.Valid = len(validation.Errors) == 0
	return validation
}

func (s *DialogueEpisodeService) generationPrompt(sessionID string, result TopicGenerationResult, plan DialogueArcPlan, turnCount int) string {
	resultJSON, _ := json.Marshal(result)
	planJSON, _ := json.Marshal(plan)
	return fmt.Sprintf(`あなたはRenCrow IdleChatの完成対話台本を生成します。
MioとShiroはCORE Agentであり、CodexExeやモデル名は発話者ではありません。全%d turnを一度に生成してください。

Mio SystemPrompt:
%s

Shiro SystemPrompt:
%s

session_id=%s
topic_result=%s
arc_plan=%s
content_mode指示=%s

条件:
- speakerはmioから始め、mio/shiroを交互にする。
- 各turnは直前の相手発話を受け、新しい具体的貢献を一つ加える。
- 話者名、プロンプト、JSON、候補、生成過程、ユーザーへの質問を本文へ出さない。
- display_textはViewer用、speech_textはTTS用。通常は同文でよい。
- Forecastの時間範囲はtopic_result.seed.forecast_horizonを厳守する。Forecast以外へ未来範囲を持ち込まない。
- 出力前に全turnを順番に自己点検する。

JSON以外を付けず、{"turns":[{"speaker":"mio","display_text":"本文","speech_text":"読み上げ本文"}]}だけを返してください。turn_indexとmessage_idはCOREが確定します。`,
		turnCount, s.personas["mio"], s.personas["shiro"], strings.TrimSpace(sessionID), string(resultJSON), string(planJSON), dialogueContentPolicyInstruction(DialogueContentPolicy{Mode: plan.ContentMode, Reasons: plan.ContentModeReasons}))
}

func (s *DialogueEpisodeService) append(artifact DialogueEpisodeArtifact) error {
	if s.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("create dialogue episode directory: %w", err)
	}
	data, err := json.Marshal(artifact)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(s.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open dialogue episode store: %w", err)
	}
	defer file.Close()
	if _, err := file.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("append dialogue episode: %w", err)
	}
	return nil
}
