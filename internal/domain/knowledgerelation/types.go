package knowledgerelation

import (
	"sort"
	"strings"
	"time"
)

const (
	RelationSameEntity                 = "same_entity"
	RelationSameTopic                  = "same_topic"
	RelationSameProject                = "same_project"
	RelationSameAuthor                 = "same_author"
	RelationUsedTogetherInConversation = "used_together_in_conversation"
)

type ItemMetadata struct {
	ItemID     string
	Domain     string
	SourceType string
	Title      string
	Summary    string
	Entities   []string
	Topics     []string
	Projects   []string
	Author     string
	CreatedAt  time.Time
}

type Relation struct {
	SrcItemID    string
	DstItemID    string
	RelationType string
	Score        float64
	Evidence     string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type ScoringConfig struct {
	SameEntityScore  float64
	SameProjectScore float64
	SameTopicScore   float64
	SameAuthorScore  float64
	MinimumScore     float64
	Now              func() time.Time
}

func DefaultScoringConfig() ScoringConfig {
	return ScoringConfig{
		SameEntityScore:  3,
		SameProjectScore: 3,
		SameTopicScore:   2,
		SameAuthorScore:  1,
		MinimumScore:     4,
		Now:              time.Now,
	}
}

func BuildPairRelations(src ItemMetadata, dst ItemMetadata, cfg ScoringConfig) []Relation {
	if strings.TrimSpace(src.ItemID) == "" || strings.TrimSpace(dst.ItemID) == "" || src.ItemID == dst.ItemID {
		return nil
	}
	cfg = normalizeScoringConfig(cfg)
	now := cfg.Now().UTC()
	type match struct {
		relationType string
		score        float64
		evidence     string
	}
	matches := []match{}
	addMatch := func(relationType string, score float64, evidence string) {
		matches = append(matches, match{relationType: relationType, score: score, evidence: evidence})
	}
	if common := firstCommon(src.Entities, dst.Entities); common != "" {
		addMatch(RelationSameEntity, cfg.SameEntityScore, "same entity: "+common)
	}
	if common := firstCommon(src.Projects, dst.Projects); common != "" {
		addMatch(RelationSameProject, cfg.SameProjectScore, "same project: "+common)
	}
	if common := firstCommon(src.Topics, dst.Topics); common != "" {
		addMatch(RelationSameTopic, cfg.SameTopicScore, "same topic: "+common)
	}
	if sameNonEmpty(src.Author, dst.Author) {
		addMatch(RelationSameAuthor, cfg.SameAuthorScore, "same author: "+strings.TrimSpace(src.Author))
	}
	totalScore := 0.0
	evidenceParts := make([]string, 0, len(matches))
	for _, item := range matches {
		totalScore += item.score
		evidenceParts = append(evidenceParts, item.evidence)
	}
	if totalScore < cfg.MinimumScore || len(matches) == 0 {
		return nil
	}
	var relations []Relation
	for _, item := range matches {
		relations = append(relations, Relation{
			SrcItemID:    src.ItemID,
			DstItemID:    dst.ItemID,
			RelationType: item.relationType,
			Score:        totalScore,
			Evidence:     strings.Join(evidenceParts, "; "),
			CreatedAt:    now,
			UpdatedAt:    now,
		})
	}
	return relations
}

func BuildRelations(items []ItemMetadata, cfg ScoringConfig) []Relation {
	cfg = normalizeScoringConfig(cfg)
	relations := []Relation{}
	for i := 0; i < len(items); i++ {
		for j := i + 1; j < len(items); j++ {
			pair := BuildPairRelations(items[i], items[j], cfg)
			relations = append(relations, pair...)
			reverse := BuildPairRelations(items[j], items[i], cfg)
			relations = append(relations, reverse...)
		}
	}
	sort.SliceStable(relations, func(i, j int) bool {
		if relations[i].Score == relations[j].Score {
			if relations[i].SrcItemID == relations[j].SrcItemID {
				return relations[i].DstItemID < relations[j].DstItemID
			}
			return relations[i].SrcItemID < relations[j].SrcItemID
		}
		return relations[i].Score > relations[j].Score
	})
	return relations
}

func normalizeScoringConfig(cfg ScoringConfig) ScoringConfig {
	def := DefaultScoringConfig()
	if cfg.SameEntityScore == 0 {
		cfg.SameEntityScore = def.SameEntityScore
	}
	if cfg.SameProjectScore == 0 {
		cfg.SameProjectScore = def.SameProjectScore
	}
	if cfg.SameTopicScore == 0 {
		cfg.SameTopicScore = def.SameTopicScore
	}
	if cfg.SameAuthorScore == 0 {
		cfg.SameAuthorScore = def.SameAuthorScore
	}
	if cfg.MinimumScore == 0 {
		cfg.MinimumScore = def.MinimumScore
	}
	if cfg.Now == nil {
		cfg.Now = def.Now
	}
	return cfg
}

func firstCommon(left []string, right []string) string {
	seen := map[string]string{}
	for _, value := range left {
		normalized := normalizeKey(value)
		if normalized != "" {
			seen[normalized] = strings.TrimSpace(value)
		}
	}
	for _, value := range right {
		normalized := normalizeKey(value)
		if normalized != "" {
			if original := seen[normalized]; original != "" {
				return original
			}
		}
	}
	return ""
}

func sameNonEmpty(left string, right string) bool {
	return normalizeKey(left) != "" && normalizeKey(left) == normalizeKey(right)
}

func normalizeKey(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
