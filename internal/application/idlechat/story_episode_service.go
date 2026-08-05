package idlechat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// StoryCodexGenerator is the narrow application boundary for one ephemeral
// CodexExe execution. Generation and semantic review use separate executions.
type StoryCodexGenerator interface {
	Generate(ctx context.Context, prompt string) (string, error)
}

type StoryEpisodeService struct {
	store                  *storyEpisodeStore
	generator              StoryCodexGenerator
	personas               map[string]string
	maxAttempts            int
	maxSuffixRegenerations int
	prepareMu              sync.Mutex
}

type storyGenerationSeed struct {
	Source             StoryEpisodeSource
	Reader             string
	Listener           string
	Contract           StoryEpisodeContract
	ReplacementForID   string
	AvoidRecentSummary string
}

var storyTransformationAxes = []string{
	"脇役を主人公にして、元の事件を別の利害から描く",
	"舞台を少し先の未来へ移し、元話の約束を技術と制度で変える",
	"勝敗ではなく、失敗後の再建を物語の中心にする",
	"語られなかった一日を追加し、登場人物の選択の意味を反転させる",
}

var storyGenres = []string{"near_future_sf", "comedy", "human_drama", "mystery"}

var storyInterestContracts = []struct {
	Direction string
	Criteria  []string
}{
	{Direction: "funny", Criteria: []string{"前振りを置く", "誇張を段階的に強める", "最後に意味の通るオチで回収する"}},
	{Direction: "moving", Criteria: []string{"喪失か葛藤を具体化する", "人物の選択で関係を変える", "安易な奇跡ではなく余韻で着地する"}},
	{Direction: "thrilling", Criteria: []string{"制約と危険を早めに示す", "選択ごとに状況を悪化または反転させる", "伏線を終盤で回収する"}},
	{Direction: "thought_provoking", Criteria: []string{"対立する価値を人物の行動で示す", "単純な正解に逃げない", "読後に一つの問いを残す"}},
}

func NewStoryEpisodeService(store *storyEpisodeStore, generator StoryCodexGenerator, personas map[string]string) *StoryEpisodeService {
	clonedPersonas := make(map[string]string, len(personas))
	for name, prompt := range personas {
		clonedPersonas[normalizeStoryAgent(name)] = strings.TrimSpace(prompt)
	}
	target := 1
	if store != nil && store.target > 0 {
		target = store.target
	}
	return &StoryEpisodeService{
		store:                  store,
		generator:              generator,
		personas:               clonedPersonas,
		maxAttempts:            max(3, target),
		maxSuffixRegenerations: 3,
	}
}

func NewPersistentStoryEpisodeService(path string, target int, generator StoryCodexGenerator, personas map[string]string) *StoryEpisodeService {
	return NewStoryEpisodeService(newStoryEpisodeStore(path, target), generator, personas)
}

func (s *StoryEpisodeService) SetMaxSuffixRegenerations(limit int) {
	if s == nil || limit < 1 {
		return
	}
	s.maxSuffixRegenerations = limit
}

func (s *StoryEpisodeService) Snapshot() StoryEpisodeStockSnapshot {
	if s == nil || s.store == nil {
		return StoryEpisodeStockSnapshot{}
	}
	return s.store.snapshot()
}

func (s *StoryEpisodeService) NextReady() (StoryEpisodeArtifact, bool) {
	if s == nil || s.store == nil {
		return StoryEpisodeArtifact{}, false
	}
	return s.store.nextReady()
}

func (s *StoryEpisodeService) Episode(episodeID string) (StoryEpisodeArtifact, bool) {
	if s == nil || s.store == nil {
		return StoryEpisodeArtifact{}, false
	}
	return s.store.get(episodeID)
}

func (s *StoryEpisodeService) MarkPlayed(episodeID string, at time.Time) error {
	if s == nil || s.store == nil {
		return errors.New("story episode service is not configured")
	}
	return s.store.markPlayed(episodeID, at)
}

// BackfillReadyTitles adds a work title to legacy ready artifacts without
// changing their validated turns, message identities, contract, or ledger.
func (s *StoryEpisodeService) BackfillReadyTitles(ctx context.Context) error {
	if s == nil || s.store == nil || s.generator == nil {
		return errors.New("story episode title producer is not configured")
	}
	s.prepareMu.Lock()
	defer s.prepareMu.Unlock()
	s.store.setFilling(true)
	defer s.store.setFilling(false)

	var lastErr error
	for _, artifact := range s.store.snapshot().Episodes {
		if err := ctx.Err(); err != nil {
			return err
		}
		if artifact.ProductionStatus != StoryProductionReady || !artifact.Validation.Valid || strings.TrimSpace(artifact.StoryTitle) != "" {
			continue
		}
		title, err := s.generateStoryTitle(ctx, artifact)
		if err != nil {
			lastErr = err
			s.store.recordFailure("title_generation", err)
			log.Printf("[Story] title backfill failed: episode=%s error=%v", artifact.EpisodeID, err)
			continue
		}
		artifact.StoryTitle = title
		artifact.Revision++
		if err := s.store.append(artifact); err != nil {
			lastErr = err
			s.store.recordFailure("storage", err)
			continue
		}
		log.Printf("[Story] title backfilled: episode=%s revision=%d title=%q", artifact.EpisodeID, artifact.Revision, artifact.StoryTitle)
	}
	return lastErr
}

// PrepareToTarget fills ready stock. Invalid artifacts stay needs_repair and a
// later generation is linked as their replacement instead of waiting for repair.
func (s *StoryEpisodeService) PrepareToTarget(ctx context.Context) error {
	if s == nil || s.store == nil {
		return errors.New("story episode producer is not configured")
	}
	titleErr := s.BackfillReadyTitles(ctx)
	if err := s.prepareUntil(ctx, s.store.target); err != nil {
		return err
	}
	return titleErr
}

func (s *StoryEpisodeService) PrepareAdditional(ctx context.Context, count int) error {
	if s == nil || s.store == nil {
		return errors.New("story episode producer is not configured")
	}
	if count < 1 {
		return s.PrepareToTarget(ctx)
	}
	titleErr := s.BackfillReadyTitles(ctx)
	if err := s.prepareUntil(ctx, s.store.snapshot().Ready+count); err != nil {
		return err
	}
	return titleErr
}

func (s *StoryEpisodeService) prepareUntil(ctx context.Context, desiredReady int) error {
	if s == nil || s.store == nil || s.generator == nil {
		return errors.New("story episode producer is not configured")
	}
	s.prepareMu.Lock()
	defer s.prepareMu.Unlock()
	if s.store.snapshot().Ready >= desiredReady {
		return nil
	}
	s.store.setFilling(true)
	defer s.store.setFilling(false)

	replacementFor := ""
	var lastErr error
	attemptLimit := max(s.maxAttempts, desiredReady-s.store.snapshot().Ready)
	for attempt := 0; attempt < attemptLimit && s.store.snapshot().Ready < desiredReady; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		s.store.recordGenerationAttempt()
		seed := s.seedForAttempt(attempt, replacementFor)
		artifact, err := s.generateArtifact(ctx, seed)
		if err != nil {
			lastErr = err
			s.store.recordFailure("generation", err)
			log.Printf("[Story] generation attempt failed: attempt=%d/%d error=%v", attempt+1, attemptLimit, err)
			continue
		}
		review, err := s.reviewArtifact(ctx, artifact)
		if err != nil {
			lastErr = err
			s.store.recordFailure("semantic_review", err)
			log.Printf("[Story] semantic review failed: episode=%s error=%v", artifact.EpisodeID, err)
			review = StorySemanticReview{Valid: false, Errors: []StoryValidationError{{Code: "quality_violation", Field: "semantic_review", Evidence: err.Error()}}}
		}
		artifact.Validation = ValidateStoryEpisode(artifact, review)
		if artifact.Validation.Valid {
			artifact.ProductionStatus = StoryProductionReady
		} else {
			artifact.ProductionStatus = StoryProductionNeedsRepair
		}
		if err := s.store.append(artifact); err != nil {
			lastErr = err
			s.store.recordFailure("storage", err)
			continue
		}
		if artifact.Validation.Valid {
			replacementFor = ""
			s.store.recordFailure("", nil)
		} else {
			replacementFor = artifact.EpisodeID
			lastErr = fmt.Errorf("story episode %s needs repair", artifact.EpisodeID)
			s.store.recordFailure("validation", lastErr)
			log.Printf("[Story] episode retained as needs_repair: episode=%s first_invalid_turn=%d errors=%d", artifact.EpisodeID, artifact.Validation.FirstInvalidTurn, len(artifact.Validation.Errors))
		}
	}
	missing := max(desiredReady-s.store.snapshot().Ready, 0)
	if missing > 0 {
		if lastErr == nil {
			lastErr = errors.New("generation attempts exhausted")
		}
		return fmt.Errorf("story ready stock is short by %d: %w", missing, lastErr)
	}
	return nil
}

// RepairNeedsRepair performs suffix-only repair independently from stock
// replenishment. The accepted prefix and its message IDs are immutable.
func (s *StoryEpisodeService) RepairNeedsRepair(ctx context.Context) error {
	if s == nil || s.store == nil || s.generator == nil {
		return errors.New("story episode producer is not configured")
	}
	s.prepareMu.Lock()
	defer s.prepareMu.Unlock()
	s.store.setFilling(true)
	defer s.store.setFilling(false)
	var repairErr error
	for _, artifact := range s.store.snapshot().Episodes {
		if s.store.snapshot().Ready >= s.store.target {
			break
		}
		if artifact.ProductionStatus != StoryProductionNeedsRepair {
			continue
		}
		if storyValidationOnlyHasCode(artifact.Validation, "title_violation") {
			if err := s.repairTitleOnly(ctx, artifact); err != nil {
				repairErr = err
				s.store.recordFailure("title_generation", err)
			}
			continue
		}
		if artifact.SuffixRegenerations >= s.maxSuffixRegenerations {
			artifact.ProductionStatus = StoryProductionFailed
			if err := s.store.append(artifact); err != nil {
				repairErr = err
			}
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.repairSuffix(ctx, artifact); err != nil {
			repairErr = err
			s.store.recordFailure("suffix_repair", err)
		}
	}
	return repairErr
}

func (s *StoryEpisodeService) repairSuffix(ctx context.Context, artifact StoryEpisodeArtifact) error {
	from := artifact.Validation.FirstInvalidTurn
	if from < 1 || from > len(artifact.Turns) {
		from = 1
	}
	prefixLength := from - 1
	prefix := append([]StoryEpisodeTurn(nil), artifact.Turns[:prefixLength]...)
	payload, err := json.Marshal(artifact)
	if err != nil {
		return err
	}
	prompt := fmt.Sprintf(`あなたはRenCrow IdleChat物語のsuffix修復担当です。
turn %dより前は合格済みで変更禁止です。turn %d以降だけを、末尾まで作り直してください。
reader=%s、listener=%s、story_contractとstory_ledgerを維持し、検出errorをすべて解消してください。
JSON以外を付けず、{"turns":[...]}だけを返してください。各turnのmessage_idは省略してください。
対象episode:
%s`, from, from, artifact.Reader, artifact.Listener, string(payload))
	raw, err := s.generator.Generate(ctx, prompt)
	if err != nil {
		return fmt.Errorf("CodexExe suffix repair: %w", err)
	}
	var suffix struct {
		Turns []StoryEpisodeTurn `json:"turns"`
	}
	if err := decodeStoryJSON(raw, &suffix); err != nil {
		return fmt.Errorf("decode CodexExe suffix repair: %w", err)
	}
	if len(suffix.Turns) == 0 {
		return errors.New("CodexExe suffix repair returned no turns")
	}
	for i := range suffix.Turns {
		suffix.Turns[i].TurnIndex = prefixLength + i + 1
		suffix.Turns[i].MessageID = newIdleChatMessageID()
		if suffix.Turns[i].UtteranceRole == StoryUtteranceNarration {
			suffix.Turns[i].ReactsTo = 0
		}
	}
	artifact.Turns = append(prefix, suffix.Turns...)
	artifact.Revision++
	artifact.FixedPrefixLength = prefixLength
	artifact.RepairFromTurn = from
	artifact.SuffixRegenerations++
	if strings.TrimSpace(artifact.StoryTitle) == "" || storyValidationHasCode(artifact.Validation, "title_violation") {
		title, err := s.generateStoryTitle(ctx, artifact)
		if err != nil {
			return err
		}
		artifact.StoryTitle = title
	}
	review, reviewErr := s.reviewArtifact(ctx, artifact)
	if reviewErr != nil {
		review = StorySemanticReview{Valid: false, Errors: []StoryValidationError{{Code: "quality_violation", Field: "semantic_review", Evidence: reviewErr.Error()}}}
	}
	artifact.Validation = ValidateStoryEpisode(artifact, review)
	if artifact.Validation.Valid {
		artifact.ProductionStatus = StoryProductionReady
	} else if artifact.SuffixRegenerations >= s.maxSuffixRegenerations {
		artifact.ProductionStatus = StoryProductionFailed
	} else {
		artifact.ProductionStatus = StoryProductionNeedsRepair
	}
	if err := s.store.append(artifact); err != nil {
		return err
	}
	if !artifact.Validation.Valid {
		return fmt.Errorf("story episode %s suffix remains invalid", artifact.EpisodeID)
	}
	return nil
}

func (s *StoryEpisodeService) repairTitleOnly(ctx context.Context, artifact StoryEpisodeArtifact) error {
	title, err := s.generateStoryTitle(ctx, artifact)
	if err != nil {
		return err
	}
	artifact.StoryTitle = title
	artifact.Revision++
	review, reviewErr := s.reviewArtifact(ctx, artifact)
	if reviewErr != nil {
		review = StorySemanticReview{Valid: false, Errors: []StoryValidationError{{Code: "quality_violation", Field: "semantic_review", Evidence: reviewErr.Error()}}}
	}
	artifact.Validation = ValidateStoryEpisode(artifact, review)
	if artifact.Validation.Valid {
		artifact.ProductionStatus = StoryProductionReady
	} else {
		artifact.ProductionStatus = StoryProductionNeedsRepair
	}
	if err := s.store.append(artifact); err != nil {
		return err
	}
	if !artifact.Validation.Valid {
		return fmt.Errorf("story episode %s title repair remains invalid", artifact.EpisodeID)
	}
	return nil
}

func storyValidationHasCode(validation StoryValidationResult, code string) bool {
	for _, item := range validation.Errors {
		if strings.TrimSpace(item.Code) == code {
			return true
		}
	}
	return false
}

func storyValidationOnlyHasCode(validation StoryValidationResult, code string) bool {
	if len(validation.Errors) == 0 {
		return false
	}
	for _, item := range validation.Errors {
		if strings.TrimSpace(item.Code) != code {
			return false
		}
	}
	return true
}

func (s *StoryEpisodeService) generateArtifact(ctx context.Context, seed storyGenerationSeed) (StoryEpisodeArtifact, error) {
	prompt := s.generationPrompt(seed)
	raw, err := s.generator.Generate(ctx, prompt)
	if err != nil {
		return StoryEpisodeArtifact{}, fmt.Errorf("CodexExe story generation: %w", err)
	}
	var artifact StoryEpisodeArtifact
	if err := decodeStoryJSON(raw, &artifact); err != nil {
		return StoryEpisodeArtifact{}, fmt.Errorf("decode CodexExe story: %w", err)
	}
	now := time.Now().UTC()
	artifact.SchemaVersion = StoryEpisodeSchemaVersion
	artifact.EpisodeID = "story-" + uuid.NewString()
	artifact.Revision = 1
	artifact.EpisodeKind = StoryEpisodeKind
	artifact.GenerationID = "story-generation-" + uuid.NewString()
	artifact.ReplacementForEpisodeID = seed.ReplacementForID
	artifact.StoryTitle = strings.TrimSpace(artifact.StoryTitle)
	artifact.Source = seed.Source
	artifact.Reader = seed.Reader
	artifact.Listener = seed.Listener
	artifact.Contract = seed.Contract
	artifact.ProductionStatus = StoryProductionValidating
	artifact.Validation = StoryValidationResult{}
	artifact.CreatedAt = now
	artifact.UpdatedAt = now
	for i := range artifact.Turns {
		artifact.Turns[i].TurnIndex = i + 1
		artifact.Turns[i].MessageID = newIdleChatMessageID()
		if artifact.Turns[i].UtteranceRole == StoryUtteranceNarration {
			artifact.Turns[i].ReactsTo = 0
		}
	}
	return artifact, nil
}

func (s *StoryEpisodeService) generateStoryTitle(ctx context.Context, artifact StoryEpisodeArtifact) (string, error) {
	payload, err := json.Marshal(artifact)
	if err != nil {
		return "", err
	}
	prompt := fmt.Sprintf(`あなたはRenCrow IdleChatの完成作品タイトル担当です。
元話名ではなく、完成した本文、genre、interest_direction、結末の余韻に合う日本語の作品タイトルを1つ付けてください。

方向:
- funny: キャッチーさ、人物の論理、可笑しみが伝わる題。無関係な駄洒落にしない。
- moving: 静かな感情や余韻が残る題。感動作などの説明語を付けない。
- thrilling / scary: 危機、不穏さ、違和感を感じる題。結末を言い切らない。
- thought_provoking: 価値の対立や問いを感じる題。説教や標語にしない。

共通条件:
- 2〜40文字。
- 元話名の丸写し、「新・元話名」「元話名 SF版」のような機械的改題は禁止。
- 本文にない固有名詞、genre名だけの説明、結末の完全なネタバレは禁止。
- 固定テンプレートを使わず、この作品固有の人物、道具、選択、オチ、余韻から言葉を選ぶ。
- JSON以外を付けず、{"story_title":"作品タイトル"}だけを返す。

対象作品:
%s`, string(payload))
	raw, err := s.generator.Generate(ctx, prompt)
	if err != nil {
		return "", fmt.Errorf("CodexExe story title generation: %w", err)
	}
	var response struct {
		StoryTitle string `json:"story_title"`
	}
	if err := decodeStoryJSON(raw, &response); err != nil {
		return "", fmt.Errorf("decode CodexExe story title: %w", err)
	}
	title := strings.TrimSpace(response.StoryTitle)
	candidate := artifact
	candidate.StoryTitle = title
	if evidence := storyTitleValidationEvidence(candidate); evidence != "" {
		return "", fmt.Errorf("invalid CodexExe story title: %s", evidence)
	}
	return title, nil
}

func (s *StoryEpisodeService) reviewArtifact(ctx context.Context, artifact StoryEpisodeArtifact) (StorySemanticReview, error) {
	payload, err := json.Marshal(artifact)
	if err != nil {
		return StorySemanticReview{}, err
	}
	prompt := `あなたはRenCrow IdleChat物語の意味検査担当です。生成担当とは独立して検査してください。
次のJSONについて、story_titleが本文、genre、interest_direction、結末の余韻に合い、元話名の丸写しや機械的改題でないことも確認します。さらに、造語を含む不自然・破損した語、人間関係、時系列、場所、所有関係、世界規則、固有名詞とふりがな、面白さ契約、読み手と合いの手の演出を確認します。
不明・不確実ならvalid=falseにしてください。修正文は作らず、JSONのみ返してください。
形式: {"valid":false,"errors":[{"code":"continuity_violation","turn_index":5,"field":"turns","evidence":"短い根拠"}]}
codeは title_violation, lexical_corruption, entity_relation_violation, continuity_violation, world_rule_violation, reading_violation, interest_contract_violation, story_performance_violation, quality_violation のいずれか。
対象JSON:
` + string(payload)
	raw, err := s.generator.Generate(ctx, prompt)
	if err != nil {
		return StorySemanticReview{}, fmt.Errorf("CodexExe semantic review: %w", err)
	}
	var review StorySemanticReview
	if err := decodeStoryJSON(raw, &review); err != nil {
		return StorySemanticReview{}, fmt.Errorf("decode CodexExe semantic review: %w", err)
	}
	return review, nil
}

func (s *StoryEpisodeService) seedForAttempt(attempt int, replacementFor string) storyGenerationSeed {
	tale := simpleStoryTales[attempt%len(simpleStoryTales)]
	reader := "mio"
	listener := "shiro"
	if attempt%2 == 1 {
		reader, listener = listener, reader
	}
	interest := storyInterestContracts[attempt%len(storyInterestContracts)]
	source := StoryEpisodeSource{Title: tale.title, Synopsis: tale.synopsis}
	policy := ClassifyDialogueContentPolicy(TopicGenerationResult{
		Topic: source.Title + " " + source.Synopsis,
		Seed:  TopicSeed{StoryBase: source.Title, StoryTransform: storyTransformationAxes[attempt%len(storyTransformationAxes)]},
	})
	return storyGenerationSeed{
		Source:           source,
		Reader:           reader,
		Listener:         listener,
		ReplacementForID: replacementFor,
		Contract: StoryEpisodeContract{
			TransformationAxis: storyTransformationAxes[attempt%len(storyTransformationAxes)],
			Genre:              storyGenres[attempt%len(storyGenres)],
			InterestDirection:  interest.Direction,
			InterestContract:   append([]string(nil), interest.Criteria...),
			ContentMode:        string(policy.Mode),
		},
	}
}

func (s *StoryEpisodeService) generationPrompt(seed storyGenerationSeed) string {
	contract, _ := json.Marshal(seed.Contract)
	return fmt.Sprintf(`あなたはRenCrow IdleChatの物語脚本生成担当です。CodexExeのこの実行だけで、朗読開始前に全ターンを完成させます。

Mio character context:
%s

Shiro character context:
%s

元ネタ: %s
元あらすじ: %s
固定読み手: %s
固定聞き手・合いの手: %s
story_contract: %s
内容方針: %s

必須条件:
- 全turnと結末を完成させてから、作品固有の雰囲気に合う2〜40文字のstory_titleを決める。元話名の丸写し、「新・元話名」「元話名 SF版」のような機械的改題は禁止。
- funnyはキャッチーさや人物の論理、movingは静かな余韻、thrilling／scaryは不穏さや危機、thought_provokingは価値の対立や問いを題へ反映し、固定テンプレートを使わない。
- 読み手は全narrationを担当し、途中で交代しない。
- 聞き手だけがinterjectionを担当する。3つ以上のnarrationを挟み、全turnの10〜25%%、30文字以内、連続・同文反復なし。
- 朗読本文は最低500文字、narrationは最低6turn。導入、展開、転換、決着、余韻を持たせる。
- transformation_axis、genre、interest_directionを混同せず、interest_contractを物語上の具体的成果として満たす。
- 戦争・災害はキャラ口調を保ちつつ真面目に扱う。政治・思想は事実と意見を分けた上で強く意見してよい。
- story_ledgerに全人物、人物関係、世界規則、造語、表記と読みを先に固定する。
- display_textは表示用、speech_textはTTS用。固有名詞と造語の読みをspeech_textへ反映する。
- 不自然な造語、人物関係・時系列・場所・所有・世界規則の矛盾、発話者の取り違えを出さない。
- 数量、時刻、場所、権限、所有関係は前後で照合し、原因や世界規則は結果より前に本文で示す。
- narrationのreacts_toは必ず0。interjectionのreacts_toだけが、直前のnarrationのturn_indexを指す。
- relationsのsubjectとobjectは必ずentitiesのidを使い、relationはその2 entity間の関係だけを書く。entityではない道具の所有を別entityとのrelationへ混ぜない。
- 人物や重要物の入手、預託、返却、移動、喪失を省略せず、後で所持者が変わる場合は受け渡しを本文へ書く。
- display_textにentities.nameまたはcoined_terms.surfaceを出すたび、speech_textには対応するreadingを文字列として正確に入れる。
- 出力直前に全turnを非表示で再点検し、台帳との矛盾、助詞抜け、意味を一つに確定できない文、不自然な造語を直してからJSONを返す。

説明やMarkdownを付けず、次の構造のJSONオブジェクトだけを返してください。メタデータはCOREが確定するため省略可です。
{"story_title":"作品タイトル","story_ledger":{"entities":[{"id":"hero","name":"主人公名","reading":"しゅじんこうめい","role":"主人公"},{"id":"ally","name":"仲間名","reading":"なかまめい","role":"仲間"}],"relations":[{"subject":"hero","relation":"友人","object":"ally"}],"world_rules":["死者は説明なく復活しない"],"coined_terms":[{"surface":"造語","reading":"ぞうご","meaning":"物語内での意味"}]},"turns":[{"turn_index":1,"speaker":"%s","utterance_role":"narration","reacts_to":0,"display_text":"本文","speech_text":"読み本文"},{"turn_index":2,"speaker":"%s","utterance_role":"interjection","reacts_to":1,"display_text":"短い反応","speech_text":"短い反応"}]}`,
		s.personas["mio"], s.personas["shiro"], seed.Source.Title, seed.Source.Synopsis,
		seed.Reader, seed.Listener, string(contract), dialogueContentPolicyInstruction(DialogueContentPolicy{Mode: DialogueContentMode(seed.Contract.ContentMode)}), seed.Reader, seed.Listener)
}

func decodeStoryJSON(raw string, target any) error {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "```") {
		lines := strings.Split(raw, "\n")
		if len(lines) >= 3 && strings.HasPrefix(strings.TrimSpace(lines[0]), "```") && strings.TrimSpace(lines[len(lines)-1]) == "```" {
			raw = strings.TrimSpace(strings.Join(lines[1:len(lines)-1], "\n"))
		}
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}
