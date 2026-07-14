package knowledgerelation

import (
	"context"
	"crypto/sha256"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	domainrelation "github.com/Nyukimin/RenCrow_CORE/internal/domain/knowledgerelation"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
)

const (
	MaxRelationUpsertsPerRun      = 5000
	BuildStatusCompleted          = "completed"
	BuildStatusBlockedNeedsReview = "blocked_needs_review"
)

type AliasResolver interface {
	Canonicalize(value string) string
}

type MetadataExtractor struct {
	aliases AliasResolver
}

func NewMetadataExtractor(aliases AliasResolver) *MetadataExtractor {
	return &MetadataExtractor{aliases: aliases}
}

var (
	technicalTokenPattern = regexp.MustCompile(`[A-Za-z][A-Za-z0-9_+.-]*`)
	projectPattern        = regexp.MustCompile(`(?i)rencrow(?:_[a-z0-9]+)*`)
)

func (e *MetadataExtractor) ExtractFromL1KnowledgeItem(item l1sqlite.L1KnowledgeItem) domainrelation.ItemMetadata {
	entities := newCanonicalSet(e)
	topics := newCanonicalSet(e)
	projects := newCanonicalSet(e)
	for _, token := range technicalTokenPattern.FindAllString(item.Title, -1) {
		if isUnambiguousTechnicalToken(token) {
			entities.add(token)
		}
	}
	for _, keyword := range item.Keywords {
		keyword = strings.TrimSpace(keyword)
		if keyword == "" {
			continue
		}
		if strings.ContainsAny(keyword, "_-") {
			topics.add(keyword)
		} else if isUnambiguousTechnicalToken(keyword) {
			entities.add(keyword)
		}
	}
	if strings.TrimSpace(item.Domain) != "" {
		topics.add(item.Domain)
	}
	for _, source := range []string{item.Title, item.SourceID, strings.Join(item.Keywords, " ")} {
		for _, project := range projectPattern.FindAllString(source, -1) {
			projects.add(project)
		}
	}
	sourceType := canonicalValue(item.Domain)
	if sourceType == "" {
		sourceType = canonicalValue(strings.SplitN(item.SourceID, ":", 2)[0])
	}
	return domainrelation.ItemMetadata{
		ItemID: item.ID, Domain: item.Domain, SourceType: sourceType, Title: item.Title,
		Summary: item.SummaryDraft, Entities: entities.values(), Topics: topics.values(), Projects: projects.values(),
		CreatedAt: item.CreatedAt,
	}
}

type canonicalSet struct {
	extractor *MetadataExtractor
	items     map[string]struct{}
}

func newCanonicalSet(extractor *MetadataExtractor) *canonicalSet {
	return &canonicalSet{extractor: extractor, items: map[string]struct{}{}}
}

func (s *canonicalSet) add(value string) {
	value = canonicalValue(value)
	if s.extractor != nil && s.extractor.aliases != nil {
		value = canonicalValue(s.extractor.aliases.Canonicalize(value))
	}
	if value != "" {
		s.items[value] = struct{}{}
	}
}

func (s *canonicalSet) values() []string {
	values := make([]string, 0, len(s.items))
	for value := range s.items {
		values = append(values, value)
	}
	sort.Strings(values)
	return values
}

func canonicalValue(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.Trim(value, "._-+:/\\")
	return value
}

func isUnambiguousTechnicalToken(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) < 2 {
		return false
	}
	hasUpper := false
	hasLower := false
	hasDigitOrSeparator := false
	for _, r := range value {
		hasUpper = hasUpper || unicode.IsUpper(r)
		hasLower = hasLower || unicode.IsLower(r)
		hasDigitOrSeparator = hasDigitOrSeparator || unicode.IsDigit(r) || strings.ContainsRune("_+.-", r)
	}
	return hasDigitOrSeparator || (hasUpper && hasLower) || (hasUpper && !hasLower)
}

type RelationBuildStore interface {
	ListKnowledgeItemsForRelations(ctx context.Context, domain string, limit int, since time.Time) ([]l1sqlite.L1KnowledgeItem, error)
	SaveKnowledgeEntity(ctx context.Context, item l1sqlite.L1KnowledgeEntity) error
	SaveKnowledgeItemEntity(ctx context.Context, item l1sqlite.L1KnowledgeItemEntity) error
	SaveKnowledgeItemRelation(ctx context.Context, item domainrelation.Relation) error
}

type BatchQuery struct {
	Domain string
	Limit  int
	DryRun bool
	Since  time.Time
}

type BuildReport struct {
	Status            string         `json:"status"`
	CheckedItems      int            `json:"checked_items"`
	EntityUpserts     int            `json:"entity_upserts"`
	ItemEntityUpserts int            `json:"item_entity_upserts"`
	RelationUpserts   int            `json:"relation_upserts"`
	Skipped           int            `json:"skipped"`
	SkipReasons       map[string]int `json:"skip_reasons"`
	DryRun            bool           `json:"dry_run"`
}

type RelationBuildService struct {
	store     RelationBuildStore
	extractor *MetadataExtractor
	cfg       domainrelation.ScoringConfig
}

func NewRelationBuildService(store RelationBuildStore, extractor *MetadataExtractor, cfg domainrelation.ScoringConfig) *RelationBuildService {
	if extractor == nil {
		extractor = NewMetadataExtractor(nil)
	}
	return &RelationBuildService{store: store, extractor: extractor, cfg: cfg}
}

func (s *RelationBuildService) BuildForItem(ctx context.Context, item l1sqlite.L1KnowledgeItem) (BuildReport, error) {
	report := newBuildReport(false)
	if s == nil || s.store == nil {
		return report, fmt.Errorf("knowledge relation store is required")
	}
	metadata := s.extractor.ExtractFromL1KnowledgeItem(item)
	if strings.TrimSpace(metadata.ItemID) == "" {
		report.Skipped = 1
		report.SkipReasons["missing_item_id"] = 1
		return report, nil
	}
	report.CheckedItems = 1
	entities, links := relationMetadataRecords(metadata)
	report.EntityUpserts = len(entities)
	report.ItemEntityUpserts = len(links)
	candidates, err := s.store.ListKnowledgeItemsForRelations(ctx, "all", 200, time.Time{})
	if err != nil {
		return report, err
	}
	relations := make([]domainrelation.Relation, 0)
	for _, candidate := range candidates {
		if candidate.ID == item.ID {
			continue
		}
		candidateMetadata := s.extractor.ExtractFromL1KnowledgeItem(candidate)
		relations = append(relations, domainrelation.BuildPairRelations(metadata, candidateMetadata, s.cfg)...)
		relations = append(relations, domainrelation.BuildPairRelations(candidateMetadata, metadata, s.cfg)...)
	}
	report.RelationUpserts = len(relations)
	if report.RelationUpserts > MaxRelationUpsertsPerRun {
		report.Status = BuildStatusBlockedNeedsReview
		return report, nil
	}
	if err := persistRelationRecords(ctx, s.store, entities, links, relations); err != nil {
		return report, err
	}
	return report, nil
}

func (s *RelationBuildService) BuildBatch(ctx context.Context, query BatchQuery) (BuildReport, error) {
	report := newBuildReport(query.DryRun)
	if s == nil || s.store == nil {
		return report, fmt.Errorf("knowledge relation store is required")
	}
	if strings.TrimSpace(query.Domain) == "" {
		query.Domain = "all"
	}
	if query.Limit <= 0 {
		query.Limit = 100
	}
	items, err := s.store.ListKnowledgeItemsForRelations(ctx, query.Domain, query.Limit, query.Since)
	if err != nil {
		return report, err
	}
	metadataItems := make([]domainrelation.ItemMetadata, 0, len(items))
	allEntities := []l1sqlite.L1KnowledgeEntity{}
	allLinks := []l1sqlite.L1KnowledgeItemEntity{}
	uniqueEntities := map[string]l1sqlite.L1KnowledgeEntity{}
	for _, item := range items {
		metadata := s.extractor.ExtractFromL1KnowledgeItem(item)
		if strings.TrimSpace(metadata.ItemID) == "" {
			report.Skipped++
			report.SkipReasons["missing_item_id"]++
			continue
		}
		report.CheckedItems++
		metadataItems = append(metadataItems, metadata)
		entities, links := relationMetadataRecords(metadata)
		for _, entity := range entities {
			uniqueEntities[entity.EntityID] = entity
		}
		allLinks = append(allLinks, links...)
	}
	for _, entity := range uniqueEntities {
		allEntities = append(allEntities, entity)
	}
	sort.Slice(allEntities, func(i, j int) bool { return allEntities[i].EntityID < allEntities[j].EntityID })
	relations := domainrelation.BuildRelations(metadataItems, s.cfg)
	report.EntityUpserts = len(allEntities)
	report.ItemEntityUpserts = len(allLinks)
	report.RelationUpserts = len(relations)
	if report.RelationUpserts > MaxRelationUpsertsPerRun {
		report.Status = BuildStatusBlockedNeedsReview
		return report, nil
	}
	if query.DryRun {
		return report, nil
	}
	if err := persistRelationRecords(ctx, s.store, allEntities, allLinks, relations); err != nil {
		return report, err
	}
	return report, nil
}

func newBuildReport(dryRun bool) BuildReport {
	return BuildReport{Status: BuildStatusCompleted, SkipReasons: map[string]int{}, DryRun: dryRun}
}

func relationMetadataRecords(metadata domainrelation.ItemMetadata) ([]l1sqlite.L1KnowledgeEntity, []l1sqlite.L1KnowledgeItemEntity) {
	now := time.Now().UTC()
	entities := []l1sqlite.L1KnowledgeEntity{}
	links := []l1sqlite.L1KnowledgeItemEntity{}
	add := func(kind, relationKind string, values []string) {
		for _, value := range values {
			id := relationEntityID(kind, value)
			entities = append(entities, l1sqlite.L1KnowledgeEntity{EntityID: id, CanonicalName: value, EntityType: kind, CreatedAt: now, UpdatedAt: now})
			links = append(links, l1sqlite.L1KnowledgeItemEntity{ItemID: metadata.ItemID, EntityID: id, RelationKind: relationKind, Score: 1, Evidence: relationKind + ": " + value, CreatedAt: now})
		}
	}
	add("entity", "mentions", metadata.Entities)
	add("topic", "topic", metadata.Topics)
	add("project", "project", metadata.Projects)
	return entities, links
}

func relationEntityID(kind, value string) string {
	sum := sha256.Sum256([]byte(kind + ":" + canonicalValue(value)))
	return fmt.Sprintf("entity:%s:%x", kind, sum[:8])
}

func persistRelationRecords(ctx context.Context, store RelationBuildStore, entities []l1sqlite.L1KnowledgeEntity, links []l1sqlite.L1KnowledgeItemEntity, relations []domainrelation.Relation) error {
	for _, entity := range entities {
		if err := store.SaveKnowledgeEntity(ctx, entity); err != nil {
			return err
		}
	}
	for _, link := range links {
		if err := store.SaveKnowledgeItemEntity(ctx, link); err != nil {
			return err
		}
	}
	for _, relation := range relations {
		if err := store.SaveKnowledgeItemRelation(ctx, relation); err != nil {
			return err
		}
	}
	return nil
}
