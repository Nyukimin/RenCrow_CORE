//go:build ignore

package idlechat

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// storyTwistsFromRaw は JSON の twists フィールドを map[string]string に変換する。
// 旧形式（各スタイルが配列）や新形式（各スタイルが文字列）の両方に対応する。
func storyTwistsFromRaw(raw json.RawMessage) map[string]string {
	if raw == nil {
		return nil
	}
	// まず新形式（map[string]string）を試みる
	var asStrings map[string]string
	if err := json.Unmarshal(raw, &asStrings); err == nil {
		return asStrings
	}
	// 旧形式（map[string]array）は無視して空マップを返す
	return nil
}

// StoryEntryJSON は data/story/<id>.json の1ファイルに対応する統合型。
// StorySource と StorySpec を1つにまとめて保持する。
type StoryEntryJSON struct {
	ID           string         `json:"id"`
	Title        string         `json:"title"`
	SourceLabel  string         `json:"source_label"`
	Kind         string         `json:"kind"`
	Language     string         `json:"language"`
	PublicDomain bool           `json:"public_domain"`
	Text         string         `json:"text"`
	JuvenileText string         `json:"juvenile_text,omitempty"`
	OpeningSeed  string         `json:"opening_seed,omitempty"`
	Setting      string         `json:"setting,omitempty"`
	Spec         *StorySpecJSON `json:"spec,omitempty"`
}

// StorySpecJSON は StorySpec の JSON 表現。
// Twists は新形式（map[string]string）と旧形式（map[string]配列）の両方に対応するため RawMessage を使用する。
type StorySpecJSON struct {
	Skeleton StorySkeletonJSON `json:"skeleton"`
	Twists   json.RawMessage   `json:"twists,omitempty"`
}

// StorySkeletonJSON は StorySkeleton の JSON 表現。
// ID と SourceTitle は runtime に source から埋めるため JSON には含めない。
type StorySkeletonJSON struct {
	CanonicalMotifs     []string    `json:"canonical_motifs,omitempty"`
	RequiredBeats       []StoryBeat `json:"required_beats,omitempty"`
	RoleConstraints     []string    `json:"role_constraints,omitempty"`
	TabooOrRule         string      `json:"taboo_or_rule,omitempty"`
	RewardPunishment    string      `json:"reward_punishment,omitempty"`
	EmotionalAftertaste string      `json:"emotional_aftertaste,omitempty"`
	RecognitionCues     []string    `json:"recognition_cues,omitempty"`
}

var (
	storyLoadOnce sync.Once
	storyCorpus   []StorySource
	storySpecs    map[string]StorySpec
)

// LoadStoryData は dir 以下の *.json を読み込み、storyCorpus と storySpecs を初期化する。
// 複数回呼ばれても初回のみ実行される。
func LoadStoryData(dir string) error {
	var loadErr error
	storyLoadOnce.Do(func() {
		loadErr = loadStoryDataOnce(dir)
	})
	return loadErr
}

func loadStoryDataOnce(dir string) error {
	pattern := filepath.Join(dir, "*.json")
	files, err := filepath.Glob(pattern)
	if err != nil {
		return fmt.Errorf("story data glob %q: %w", pattern, err)
	}
	if len(files) == 0 {
		return fmt.Errorf("story data dir %q: no *.json files found", dir)
	}

	corpus := make([]StorySource, 0, len(files))
	specs := make(map[string]StorySpec, len(files))

	for _, path := range files {
		entry, err := loadStoryEntry(path)
		if err != nil {
			return fmt.Errorf("story data load %q: %w", path, err)
		}
		corpus = append(corpus, StorySource{
			ID:           entry.ID,
			Title:        entry.Title,
			SourceLabel:  entry.SourceLabel,
			Kind:         entry.Kind,
			Language:     entry.Language,
			PublicDomain: entry.PublicDomain,
			Text:         entry.Text,
			JuvenileText: entry.JuvenileText,
			OpeningSeed:  entry.OpeningSeed,
			Setting:      entry.Setting,
		})
		if entry.Spec != nil {
			specs[entry.ID] = storySpecFromJSON(entry.ID, entry.Title, entry.Spec)
		}
	}

	storyCorpus = corpus
	storySpecs = specs
	return nil
}

func loadStoryEntry(path string) (StoryEntryJSON, error) {
	f, err := os.Open(path)
	if err != nil {
		return StoryEntryJSON{}, err
	}
	defer f.Close()

	var entry StoryEntryJSON
	if err := json.NewDecoder(f).Decode(&entry); err != nil {
		return StoryEntryJSON{}, err
	}
	return entry, nil
}

func storySpecFromJSON(id, title string, s *StorySpecJSON) StorySpec {
	sk := s.Skeleton
	return StorySpec{
		Skeleton: StorySkeleton{
			ID:                  id,
			SourceTitle:         title,
			CanonicalMotifs:     sk.CanonicalMotifs,
			RequiredBeats:       sk.RequiredBeats,
			RoleConstraints:     sk.RoleConstraints,
			TabooOrRule:         sk.TabooOrRule,
			RewardPunishment:    sk.RewardPunishment,
			EmotionalAftertaste: sk.EmotionalAftertaste,
			RecognitionCues:     sk.RecognitionCues,
		},
		Twists: storyTwistsFromRaw(s.Twists),
	}
}
