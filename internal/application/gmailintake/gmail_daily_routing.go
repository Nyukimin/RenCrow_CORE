package gmailintake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	domainkm "github.com/Nyukimin/RenCrow_CORE/internal/domain/knowledgememory"
	modulewebgather "github.com/Nyukimin/RenCrow_CORE/modules/webgather"
)

const (
	gmailDailyExtractionModeNumbered     = "numbered"
	gmailDailyExtractionModeUnstructured = "unstructured"
	gmailDailySummaryInputRunes          = 8000
)

var gmailDailyNumberedHeading = regexp.MustCompile(`(?m)^\s*(\d+)([.)])\s+\S`)

type gmailDailyExtractionCoverage struct {
	Mode             string
	ExpectedSections int
	SectionURLs      map[int]map[string]struct{}
	Required         bool
}

type gmailDailyNumberedSection struct {
	Index int    `json:"index"`
	Text  string `json:"text"`
}

type gmailDailyExtractionInput struct {
	Subject  string                        `json:"subject"`
	Body     string                        `json:"body,omitempty"`
	Coverage gmailDailyExtractionInputMeta `json:"coverage"`
	Sections []gmailDailyNumberedSection   `json:"numbered_sections,omitempty"`
}

type gmailDailyExtractionInputMeta struct {
	Mode             string `json:"mode"`
	ExpectedSections int    `json:"expected_sections"`
	MaxTopics        int    `json:"max_topics"`
}

// buildGmailDailyExtractionInput makes section coverage explicit before the
// untrusted semantic extractor is called. Numbered sections are passed as
// individually bounded records, so a model cannot silently omit a later item
// by receiving only the first few lines of the mail.
func buildGmailDailyExtractionInput(subject, body string) (gmailDailyExtractionInput, gmailDailyExtractionCoverage, error) {
	sections, numbered, err := parseGmailDailyNumberedSections(body)
	if err != nil {
		return gmailDailyExtractionInput{}, gmailDailyExtractionCoverage{}, err
	}
	input := gmailDailyExtractionInput{
		Subject: boundedGmailText(subject, gmailBriefMaxTopicTextRunes),
		Coverage: gmailDailyExtractionInputMeta{
			Mode:      gmailDailyExtractionModeUnstructured,
			MaxTopics: gmailBriefMaxTopics,
		},
	}
	coverage := gmailDailyExtractionCoverage{Mode: gmailDailyExtractionModeUnstructured, Required: true}
	if numbered {
		sectionURLs := make(map[int]map[string]struct{}, len(sections))
		for _, section := range sections {
			urls, urlErr := gmailDailySectionURLs(section.Text)
			if urlErr != nil {
				return gmailDailyExtractionInput{}, gmailDailyExtractionCoverage{}, urlErr
			}
			sectionURLs[section.Index] = urls
		}
		input.Body = ""
		input.Sections = sections
		input.Coverage.Mode = gmailDailyExtractionModeNumbered
		input.Coverage.ExpectedSections = len(sections)
		coverage = gmailDailyExtractionCoverage{Mode: gmailDailyExtractionModeNumbered, ExpectedSections: len(sections), SectionURLs: sectionURLs, Required: true}
	} else {
		input.Body = body
	}
	return input, coverage, nil
}

func gmailDailySectionURLs(sectionText string) (map[string]struct{}, error) {
	urls := make(map[string]struct{})
	for _, raw := range gmailBriefURLPattern.FindAllString(sectionText, -1) {
		trimmed := trimGmailURLPunctuation(raw)
		if trimmed == "" {
			continue
		}
		normalized, err := normalizeGmailURL(trimmed)
		if err != nil {
			return nil, fmt.Errorf("Gmail numbered section contains an unsafe URL: %w", err)
		}
		urls[normalized] = struct{}{}
	}
	if len(urls) > gmailBriefMaxURLsPerTopic {
		return nil, fmt.Errorf("Gmail numbered section contains more than %d source URLs", gmailBriefMaxURLsPerTopic)
	}
	return urls, nil
}

func parseGmailDailyNumberedSections(body string) ([]gmailDailyNumberedSection, bool, error) {
	matches := gmailDailyNumberedHeading.FindAllStringSubmatchIndex(body, -1)
	if len(matches) == 0 {
		return nil, false, nil
	}
	firstIndex, err := strconv.Atoi(body[matches[0][2]:matches[0][3]])
	if err != nil || firstIndex != 1 {
		return nil, false, nil
	}
	canonicalDelimiter := body[matches[0][4]:matches[0][5]]
	canonicalMatches := make([][]int, 0, len(matches))
	for _, match := range matches {
		if body[match[4]:match[5]] == canonicalDelimiter {
			canonicalMatches = append(canonicalMatches, match)
		}
	}
	if len(canonicalMatches) > gmailBriefMaxTopics {
		return nil, false, fmt.Errorf("Gmail daily brief has more than %d numbered sections", gmailBriefMaxTopics)
	}
	sections := make([]gmailDailyNumberedSection, 0, len(canonicalMatches))
	for index, match := range canonicalMatches {
		sectionIndex, parseErr := strconv.Atoi(body[match[2]:match[3]])
		if parseErr != nil || sectionIndex != index+1 {
			return nil, false, fmt.Errorf("Gmail daily brief numbered sections are not consecutive")
		}
		start := match[0]
		end := len(body)
		if index+1 < len(canonicalMatches) {
			end = canonicalMatches[index+1][0]
		}
		text := strings.TrimSpace(body[start:end])
		if text == "" || !utf8.ValidString(text) {
			return nil, false, fmt.Errorf("Gmail daily brief numbered section %d is empty or invalid", sectionIndex)
		}
		sections = append(sections, gmailDailyNumberedSection{Index: sectionIndex, Text: text})
	}
	return sections, true, nil
}

func validateGmailExtractionCoverage(raw *gmailExtractionCoverageWire, expected gmailDailyExtractionCoverage, topics []gmailTopic) error {
	if !expected.Required {
		if raw == nil {
			return nil
		}
		if raw.Mode == nil || strings.TrimSpace(*raw.Mode) != gmailDailyExtractionModeUnstructured {
			return errors.New("Gmail extraction coverage is invalid")
		}
		if raw.ExpectedSections == nil || *raw.ExpectedSections != 0 || raw.CoveredTopics == nil || *raw.CoveredTopics != len(topics) {
			return errors.New("Gmail unstructured extraction coverage is incomplete")
		}
		return nil
	}
	if raw == nil || raw.Mode == nil || strings.TrimSpace(*raw.Mode) != expected.Mode {
		return errors.New("Gmail extraction coverage mode is invalid")
	}
	if expected.Mode == gmailDailyExtractionModeNumbered {
		if raw.ExpectedSections == nil || *raw.ExpectedSections != expected.ExpectedSections || raw.SectionIndices == nil || len(*raw.SectionIndices) != expected.ExpectedSections || raw.CoveredTopics == nil || *raw.CoveredTopics != expected.ExpectedSections || len(topics) != expected.ExpectedSections {
			return errors.New("Gmail numbered extraction coverage is incomplete")
		}
		seen := make(map[int]struct{}, expected.ExpectedSections)
		for index, section := range *raw.SectionIndices {
			if section != index+1 {
				return errors.New("Gmail numbered extraction section indices are not consecutive")
			}
			if _, exists := seen[section]; exists {
				return errors.New("Gmail numbered extraction section index is duplicated")
			}
			seen[section] = struct{}{}
		}
		for index, topic := range topics {
			if topic.SectionIndex != index+1 {
				return errors.New("Gmail numbered extraction topics omit or reorder a section")
			}
			expectedURLs := expected.SectionURLs[topic.SectionIndex]
			actualURLs := make(map[string]struct{}, len(topic.URLs))
			for _, rawURL := range topic.URLs {
				normalized, err := normalizeGmailURL(rawURL)
				if err != nil {
					return errors.New("Gmail numbered extraction returned an unsafe section URL")
				}
				actualURLs[normalized] = struct{}{}
			}
			if len(actualURLs) != len(expectedURLs) {
				return errors.New("Gmail numbered extraction omitted or added a section URL")
			}
			for url := range expectedURLs {
				if _, exists := actualURLs[url]; !exists {
					return errors.New("Gmail numbered extraction reassigned a section URL")
				}
			}
		}
		return nil
	}
	if raw.ExpectedSections == nil || *raw.ExpectedSections != 0 || raw.CoveredTopics == nil || *raw.CoveredTopics != len(topics) || raw.SectionIndices != nil {
		return errors.New("Gmail unstructured extraction coverage is incomplete")
	}
	for _, topic := range topics {
		if topic.SectionIndex != 0 {
			return errors.New("unstructured Gmail extraction must not assign section indices")
		}
	}
	return nil
}

func (e *gmailBriefEvaluator) buildGmailEvidence(ctx context.Context, requestedURL string, response modulewebgather.FetchResponse) (gmailEvidence, error) {
	evidence, err := buildGmailEvidence(requestedURL, response)
	if err != nil {
		return gmailEvidence{}, err
	}
	return e.summarizeGmailEvidence(ctx, evidence)
}

// buildGmailKnowledgeProposalFromAssessment preserves every successfully
// captured URL and exact assessment quote, while retaining fetch failures as
// explicit unverified source records.
func buildGmailKnowledgeProposalFromAssessment(topic gmailTopic, assessment gmailAssessment, quotes []gmailQuote, evidenceByURL map[string]gmailEvidence, failedURLs []string) GmailKnowledgeProposal {
	verificationStatus := "unverified"
	if assessment.Decision != "unverified" && assessment.VerificationStatus == "verified" && strings.TrimSpace(assessment.Verification) != "" && len(quotes) > 0 {
		verificationStatus = "verified"
	}
	sourceQuotes := make(map[string][]GmailKnowledgeQuote)
	for _, quote := range quotes {
		sourceQuotes[quote.URL] = append(sourceQuotes[quote.URL], GmailKnowledgeQuote{URL: quote.URL, Quote: quote.Quote, Primary: quote.Primary})
	}
	sources := make([]GmailKnowledgeSource, 0, len(topic.URLs))
	for _, sourceURL := range topic.URLs {
		if evidence, ok := evidenceByURL[sourceURL]; ok {
			quotesForSource := sourceQuotes[sourceURL]
			status := "unverified"
			reason := firstNonEmptyGmailString(assessment.Verification, assessment.Reason, "取得済みですが、この話題の検証引用には選択されていません")
			if verificationStatus == "verified" && len(quotesForSource) > 0 {
				status = "verified"
				reason = assessment.Verification
			}
			sources = append(sources, GmailKnowledgeSource{URL: evidence.URL, FinalURL: evidence.FinalURL, ContentHash: evidence.ContentHash, CapturedAt: evidence.CapturedAt, VerificationStatus: status, VerificationReason: reason, Quotes: quotesForSource})
			continue
		}
		sources = append(sources, GmailKnowledgeSource{URL: sourceURL, VerificationStatus: "unverified", VerificationReason: "元記事を取得できませんでした"})
	}
	return GmailKnowledgeProposal{
		Topic: topic.Title, Claim: topic.Claim, Kind: topic.Kind, SectionIndex: topic.SectionIndex,
		Summary:            firstNonEmptyGmailString(assessment.Verification, assessment.Reason, topic.Claim),
		VerificationStatus: verificationStatus,
		VerificationReason: firstNonEmptyGmailString(assessment.Verification, assessment.Reason, "検証根拠がありません"),
		Sources:            sources, FailedURLs: append([]string(nil), failedURLs...),
	}
}

func buildGmailUnverifiedKnowledgeProposal(topic gmailTopic, failedURLs []string, reason string) GmailKnowledgeProposal {
	return buildGmailUnverifiedKnowledgeProposalWithEvidence(topic, nil, failedURLs, reason)
}

// buildGmailUnverifiedKnowledgeProposalWithEvidence is the fail-closed
// per-topic semantic fallback. It copies only the already validated extracted
// topic and fetched source metadata; assessment quotes and specification text
// are deliberately omitted because the assessment was not accepted.
func buildGmailUnverifiedKnowledgeProposalWithEvidence(topic gmailTopic, evidenceByURL map[string]gmailEvidence, failedURLs []string, reason string) GmailKnowledgeProposal {
	sources := make([]GmailKnowledgeSource, 0, len(topic.URLs))
	failed := make(map[string]struct{}, len(failedURLs))
	for _, value := range failedURLs {
		failed[value] = struct{}{}
	}
	for _, sourceURL := range topic.URLs {
		if evidence, fetched := evidenceByURL[sourceURL]; fetched {
			sources = append(sources, GmailKnowledgeSource{
				URL: evidence.URL, FinalURL: evidence.FinalURL, ContentHash: evidence.ContentHash, CapturedAt: evidence.CapturedAt,
				VerificationStatus: "unverified", VerificationReason: reason,
			})
		} else if _, unavailable := failed[sourceURL]; unavailable {
			sources = append(sources, GmailKnowledgeSource{URL: sourceURL, VerificationStatus: "unverified", VerificationReason: reason})
		}
	}
	return GmailKnowledgeProposal{
		Topic: topic.Title, Claim: topic.Claim, Kind: topic.Kind, SectionIndex: topic.SectionIndex,
		Summary: topic.Claim, VerificationStatus: "unverified", VerificationReason: reason,
		Sources: sources, FailedURLs: append([]string(nil), failedURLs...),
	}
}

func firstNonEmptyGmailString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func validateGmailKnowledgeProposal(proposal GmailKnowledgeProposal) error {
	switch proposal.Kind {
	case "ai", "politics", "other":
	default:
		return errors.New("Gmail Knowledge proposal kind is invalid")
	}
	if strings.TrimSpace(proposal.Topic) == "" || !utf8.ValidString(proposal.Topic) || utf8.RuneCountInString(proposal.Topic) > gmailBriefMaxTopicTextRunes {
		return errors.New("Gmail Knowledge proposal topic is invalid")
	}
	if strings.TrimSpace(proposal.Claim) == "" || !utf8.ValidString(proposal.Claim) || utf8.RuneCountInString(proposal.Claim) > gmailBriefMaxTopicTextRunes {
		return errors.New("Gmail Knowledge proposal claim is invalid")
	}
	if strings.TrimSpace(proposal.Summary) == "" || !utf8.ValidString(proposal.Summary) || utf8.RuneCountInString(proposal.Summary) > gmailBriefMaxProposalTextRunes {
		return errors.New("Gmail Knowledge proposal summary is invalid")
	}
	if proposal.SectionIndex < 0 || proposal.SectionIndex > gmailBriefMaxTopics {
		return errors.New("Gmail Knowledge proposal section index is invalid")
	}
	switch proposal.VerificationStatus {
	case "verified", "unverified":
	default:
		return errors.New("Gmail Knowledge proposal verification status is invalid")
	}
	if strings.TrimSpace(proposal.VerificationReason) == "" || !utf8.ValidString(proposal.VerificationReason) || utf8.RuneCountInString(proposal.VerificationReason) > gmailBriefMaxReasonRunes {
		return errors.New("Gmail Knowledge proposal verification reason is invalid")
	}
	if len(proposal.Sources) > gmailBriefMaxURLsPerTopic {
		return errors.New("Gmail Knowledge proposal source bound exceeded")
	}
	seenSources := make(map[string]struct{}, len(proposal.Sources))
	hasVerifiedEvidence := false
	for sourceIndex, source := range proposal.Sources {
		if strings.TrimSpace(source.URL) != source.URL || strings.TrimSpace(source.FinalURL) != source.FinalURL || strings.TrimSpace(source.ContentHash) != source.ContentHash || strings.TrimSpace(source.CapturedAt) != source.CapturedAt {
			return fmt.Errorf("Gmail Knowledge source %d has non-canonical whitespace", sourceIndex+1)
		}
		requestedURL, err := normalizeGmailURL(source.URL)
		if err != nil || len([]byte(source.URL)) > gmailBriefMaxURLBytes {
			return fmt.Errorf("Gmail Knowledge source %d URL is unsafe", sourceIndex+1)
		}
		if _, exists := seenSources[requestedURL]; exists {
			return errors.New("Gmail Knowledge source URL is duplicated")
		}
		seenSources[requestedURL] = struct{}{}
		if source.FinalURL != "" {
			if _, err := normalizeGmailURL(source.FinalURL); err != nil || len([]byte(source.FinalURL)) > gmailBriefMaxURLBytes {
				return fmt.Errorf("Gmail Knowledge source %d final URL is unsafe", sourceIndex+1)
			}
		}
		if !utf8.ValidString(source.VerificationReason) || strings.TrimSpace(source.VerificationReason) == "" || utf8.RuneCountInString(source.VerificationReason) > gmailBriefMaxReasonRunes {
			return fmt.Errorf("Gmail Knowledge source %d verification reason is invalid", sourceIndex+1)
		}
		hash := strings.TrimSpace(source.ContentHash)
		captured := strings.TrimSpace(source.CapturedAt)
		if (hash == "") != (captured == "") {
			return fmt.Errorf("Gmail Knowledge source %d evidence metadata is incomplete", sourceIndex+1)
		}
		if hash != "" {
			if _, err := normalizedSHA256(hash); err != nil {
				return fmt.Errorf("Gmail Knowledge source %d content hash is invalid", sourceIndex+1)
			}
			if _, err := time.Parse(time.RFC3339Nano, captured); err != nil {
				return fmt.Errorf("Gmail Knowledge source %d capture time is invalid", sourceIndex+1)
			}
			if source.FinalURL == "" {
				return fmt.Errorf("Gmail Knowledge source %d final URL is required with fetched evidence", sourceIndex+1)
			}
		}
		switch source.VerificationStatus {
		case "verified", "unverified":
		default:
			return fmt.Errorf("Gmail Knowledge source %d verification status is invalid", sourceIndex+1)
		}
		if source.VerificationStatus == "verified" && hash == "" {
			return fmt.Errorf("Gmail Knowledge source %d verified evidence is incomplete", sourceIndex+1)
		}
		if len(source.Quotes) > gmailBriefMaxQuotes {
			return fmt.Errorf("Gmail Knowledge source %d quote bound exceeded", sourceIndex+1)
		}
		for quoteIndex, quote := range source.Quotes {
			quoteURL, quoteErr := normalizeGmailURL(quote.URL)
			if quoteErr != nil || quoteURL != requestedURL || strings.TrimSpace(quote.Quote) == "" || !utf8.ValidString(quote.Quote) || utf8.RuneCountInString(quote.Quote) > gmailBriefMaxQuoteRunes {
				return fmt.Errorf("Gmail Knowledge source %d quote %d is invalid", sourceIndex+1, quoteIndex+1)
			}
		}
		if source.VerificationStatus == "verified" && len(source.Quotes) == 0 {
			return fmt.Errorf("Gmail Knowledge source %d verified evidence requires an exact quote", sourceIndex+1)
		}
		if source.VerificationStatus == "verified" && hash != "" && len(source.Quotes) > 0 {
			hasVerifiedEvidence = true
		}
	}
	if len(proposal.FailedURLs) > gmailBriefMaxURLsPerTopic {
		return errors.New("Gmail Knowledge failed URL bound exceeded")
	}
	seenFailed := make(map[string]struct{}, len(proposal.FailedURLs))
	for _, rawURL := range proposal.FailedURLs {
		normalized, err := normalizeGmailURL(rawURL)
		if err != nil || len([]byte(rawURL)) > gmailBriefMaxURLBytes {
			return errors.New("Gmail Knowledge failed URL is unsafe")
		}
		if _, exists := seenFailed[normalized]; exists {
			return errors.New("Gmail Knowledge failed URL is duplicated")
		}
		seenFailed[normalized] = struct{}{}
		if _, exists := seenSources[normalized]; !exists {
			return errors.New("Gmail Knowledge failed URL is not a topic source")
		}
	}
	if proposal.VerificationStatus == "verified" && !hasVerifiedEvidence {
		return errors.New("verified Gmail Knowledge proposal requires fetched evidence and an exact quote")
	}
	return nil
}

func (g *GmailIntake) prepareKnowledgeProposals(message GmailMessage, proposals []GmailKnowledgeProposal) ([]GmailPreparedKnowledge, error) {
	if len(proposals) == 0 {
		return nil, nil
	}
	if len(proposals) > maxGmailKnowledgeItems {
		return nil, errors.New("Gmail Knowledge proposal bound exceeded")
	}
	createdAt, err := time.Parse(time.RFC3339Nano, message.ReceivedAt)
	if err != nil {
		return nil, errors.New("Gmail Knowledge proposal message timestamp is invalid")
	}
	prepared := make([]GmailPreparedKnowledge, 0, len(proposals))
	seenTopics := make(map[int]struct{}, len(proposals))
	for index, proposal := range proposals {
		topicIndex := proposal.SectionIndex
		if topicIndex <= 0 {
			topicIndex = index + 1
		}
		if topicIndex > gmailBriefMaxTopics {
			return nil, errors.New("Gmail Knowledge proposal topic index is invalid")
		}
		if _, exists := seenTopics[topicIndex]; exists {
			return nil, errors.New("Gmail Knowledge proposal topic index is duplicated")
		}
		seenTopics[topicIndex] = struct{}{}
		proposal.SectionIndex = topicIndex
		if strings.TrimSpace(proposal.Topic) == "" || !utf8.ValidString(proposal.Topic) {
			return nil, errors.New("Gmail Knowledge proposal topic is invalid")
		}
		if strings.TrimSpace(proposal.Summary) == "" {
			proposal.Summary = firstNonEmptyGmailString(proposal.Claim, "Gmail topic requires source assessment")
		}
		if !utf8.ValidString(proposal.Summary) {
			return nil, errors.New("Gmail Knowledge proposal summary is invalid")
		}
		if err := validateGmailKnowledgeProposal(proposal); err != nil {
			return nil, err
		}
		itemStatus := "candidate"
		if proposal.VerificationStatus == "verified" {
			itemStatus = "reviewed"
		}
		sourceKey := gmailKnowledgeSourceKey(g.account, message.ID, topicIndex)
		item := domainkm.NewsKnowledgeItem{
			ItemID: gmailKnowledgeItemID(sourceKey), UserID: g.userID,
			Source: sourceKey, Topic: proposal.Topic, URL: firstGmailKnowledgeURL(proposal.Sources),
			Summary: buildGmailKnowledgeSummary(proposal), Durable: false, Status: itemStatus,
			Visibility: "private", CreatedAt: createdAt.UTC(),
		}
		if err := domainkm.ValidateNewsKnowledgeItem(item); err != nil {
			return nil, err
		}
		prepared = append(prepared, GmailPreparedKnowledge{SourceKey: sourceKey, Item: item, Evidence: cloneGmailKnowledgeProposal(proposal)})
	}
	return prepared, nil
}

func firstGmailKnowledgeURL(sources []GmailKnowledgeSource) string {
	for _, source := range sources {
		if strings.TrimSpace(source.URL) != "" {
			return strings.TrimSpace(source.URL)
		}
	}
	return ""
}

func buildGmailKnowledgeSummary(proposal GmailKnowledgeProposal) string {
	var body strings.Builder
	body.WriteString("Gmail daily brief Knowledge routing\n")
	body.WriteString("kind: ")
	body.WriteString(proposal.Kind)
	body.WriteString("\nverification_status: ")
	body.WriteString(proposal.VerificationStatus)
	body.WriteString("\nverification_reason: ")
	body.WriteString(proposal.VerificationReason)
	body.WriteString("\n\nClaim:\n")
	body.WriteString(proposal.Claim)
	body.WriteString("\n\nAssessment:\n")
	body.WriteString(proposal.Summary)
	body.WriteString("\n\nSource evidence:\n")
	for _, source := range proposal.Sources {
		body.WriteString("- URL: ")
		body.WriteString(source.URL)
		if source.FinalURL != "" {
			body.WriteString("\n  Final URL: ")
			body.WriteString(source.FinalURL)
		}
		body.WriteString("\n  verification_status: ")
		body.WriteString(source.VerificationStatus)
		body.WriteString("\n  verification_reason: ")
		body.WriteString(source.VerificationReason)
		if source.ContentHash != "" {
			body.WriteString("\n  content_hash: ")
			body.WriteString(source.ContentHash)
		}
		if source.CapturedAt != "" {
			body.WriteString("\n  captured_at: ")
			body.WriteString(source.CapturedAt)
		}
		for _, quote := range source.Quotes {
			body.WriteString(fmt.Sprintf("\n  quote (primary=%t): %s", quote.Primary, quote.Quote))
		}
		body.WriteByte('\n')
	}
	if len(proposal.FailedURLs) > 0 {
		body.WriteString("Failed URLs:\n")
		for _, value := range proposal.FailedURLs {
			body.WriteString("- ")
			body.WriteString(value)
			body.WriteByte('\n')
		}
	}
	return strings.TrimSpace(body.String())
}

func gmailKnowledgeSourceKey(account, messageID string, topicIndex int) string {
	return gmailLocator(account, messageID) + "#topic=" + strconv.Itoa(topicIndex)
}

func gmailKnowledgeItemID(sourceKey string) string {
	digest := sha256.Sum256([]byte(sourceKey))
	return "news_gmail_" + hex.EncodeToString(digest[:])
}

func validatePreparedGmailKnowledge(receipt GmailReceipt, index int, prepared GmailPreparedKnowledge) error {
	if classifyGmailSubject(receipt.Message.Subject) != gmailSubjectDaily {
		return errors.New("prepared Knowledge item requires a daily Gmail subject")
	}
	topicIndex, err := gmailKnowledgeTopicIndex(prepared.SourceKey)
	if err != nil || topicIndex <= 0 || topicIndex > gmailBriefMaxTopics {
		return errors.New("prepared Knowledge source key is invalid")
	}
	expectedSourceKey := gmailKnowledgeSourceKey(receipt.Account, receipt.Message.ID, topicIndex)
	if prepared.SourceKey != expectedSourceKey || prepared.Item.ItemID != gmailKnowledgeItemID(expectedSourceKey) {
		return errors.New("prepared Knowledge identity is not canonical")
	}
	if prepared.Evidence.SectionIndex != topicIndex {
		return errors.New("prepared Knowledge evidence topic index is not canonical")
	}
	if err := validateGmailKnowledgeProposal(prepared.Evidence); err != nil {
		return err
	}
	if prepared.Item.Topic != prepared.Evidence.Topic || prepared.Item.URL != firstGmailKnowledgeURL(prepared.Evidence.Sources) || prepared.Item.Summary != buildGmailKnowledgeSummary(prepared.Evidence) {
		return errors.New("prepared Knowledge item does not match its source evidence")
	}
	expectedStatus := "candidate"
	if prepared.Evidence.VerificationStatus == "verified" {
		expectedStatus = "reviewed"
	}
	if prepared.Item.UserID == "" || prepared.Item.Visibility != "private" || prepared.Item.Status != expectedStatus || prepared.Item.Source != expectedSourceKey {
		return errors.New("prepared Knowledge owner fields are invalid")
	}
	createdAt, parseErr := time.Parse(time.RFC3339Nano, receipt.Message.ReceivedAt)
	if parseErr != nil || !prepared.Item.CreatedAt.Equal(createdAt) {
		return errors.New("prepared Knowledge created_at must match the Gmail message")
	}
	if prepared.Item.URL != "" {
		if _, urlErr := normalizeGmailURL(prepared.Item.URL); urlErr != nil {
			return errors.New("prepared Knowledge URL is unsafe")
		}
	}
	if err := domainkm.ValidateNewsKnowledgeItem(prepared.Item); err != nil {
		return err
	}
	if strings.TrimSpace(prepared.Item.Summary) == "" || len([]byte(prepared.Item.Summary)) > maxGmailTextBytes {
		return errors.New("prepared Knowledge summary is invalid")
	}
	if index < 0 || index >= maxGmailKnowledgeItems {
		return errors.New("prepared Knowledge index is invalid")
	}
	return nil
}

func gmailKnowledgeTopicIndex(sourceKey string) (int, error) {
	marker := "#topic="
	position := strings.LastIndex(sourceKey, marker)
	if position < 0 {
		return 0, errors.New("topic marker is missing")
	}
	value := sourceKey[position+len(marker):]
	if strings.ContainsAny(value, "#/?&") {
		return 0, errors.New("topic marker is malformed")
	}
	return strconv.Atoi(value)
}

func validateGmailTopicOutcomes(outcomes []GmailTopicOutcome) error {
	seen := make(map[int]struct{}, len(outcomes))
	for _, outcome := range outcomes {
		if outcome.TopicIndex <= 0 || outcome.TopicIndex > maxGmailTopicOutcomes || strings.TrimSpace(outcome.Topic) == "" || !utf8.ValidString(outcome.Topic) || strings.TrimSpace(outcome.Reason) == "" || !utf8.ValidString(outcome.Reason) || utf8.RuneCountInString(outcome.Reason) > gmailBriefMaxReasonRunes {
			return errors.New("Gmail topic outcome is invalid")
		}
		if outcome.SectionIndex < 0 || outcome.SectionIndex > gmailBriefMaxTopics {
			return errors.New("Gmail topic outcome section index is invalid")
		}
		if _, exists := seen[outcome.TopicIndex]; exists {
			return errors.New("Gmail topic outcome is duplicated")
		}
		seen[outcome.TopicIndex] = struct{}{}
		switch outcome.Kind {
		case "ai", "politics", "other":
		default:
			return errors.New("Gmail topic outcome kind is invalid")
		}
		switch outcome.Status {
		case "atlas", "reviewed", "candidate", "blocked":
		default:
			return errors.New("Gmail topic outcome status is invalid")
		}
		for _, value := range append(append([]string(nil), outcome.SourceURLs...), outcome.FailedURLs...) {
			if strings.TrimSpace(value) != value {
				return errors.New("Gmail topic outcome source URL is not canonical")
			}
			if _, err := normalizeGmailURL(value); err != nil {
				return errors.New("Gmail topic outcome source URL is unsafe")
			}
		}
	}
	return nil
}

func cloneGmailTopicOutcomes(values []GmailTopicOutcome) []GmailTopicOutcome {
	if len(values) == 0 {
		return nil
	}
	result := make([]GmailTopicOutcome, len(values))
	for index, value := range values {
		result[index] = value
		result[index].SourceURLs = append([]string(nil), value.SourceURLs...)
		result[index].FailedURLs = append([]string(nil), value.FailedURLs...)
	}
	return result
}

func cloneGmailPreparedKnowledge(values []GmailPreparedKnowledge) []GmailPreparedKnowledge {
	if len(values) == 0 {
		return nil
	}
	result := make([]GmailPreparedKnowledge, len(values))
	for index, value := range values {
		result[index] = value
		result[index].Item = cloneNewsKnowledgeItem(value.Item)
		result[index].Evidence = cloneGmailKnowledgeProposal(value.Evidence)
	}
	return result
}

func cloneGmailKnowledgeProposal(value GmailKnowledgeProposal) GmailKnowledgeProposal {
	clone := value
	clone.Sources = make([]GmailKnowledgeSource, len(value.Sources))
	for index, source := range value.Sources {
		clone.Sources[index] = source
		clone.Sources[index].Quotes = append([]GmailKnowledgeQuote(nil), source.Quotes...)
	}
	clone.FailedURLs = append([]string(nil), value.FailedURLs...)
	return clone
}

func cloneNewsKnowledgeItem(item domainkm.NewsKnowledgeItem) domainkm.NewsKnowledgeItem {
	return item
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

// summarizeGmailEvidence is kept separate from URL fetching so the evaluator
// can pass a bounded summary to the model while validating quotes against the
// complete captured body.
func (e *gmailBriefEvaluator) summarizeGmailEvidence(ctx context.Context, evidence gmailEvidence) (gmailEvidence, error) {
	if utf8.RuneCountInString(evidence.FullText) <= gmailDailySummaryInputRunes {
		return evidence, nil
	}
	if e == nil || e.summarizer == nil {
		return gmailEvidence{}, &GmailBriefEvaluatorUnavailableError{Reason: "external body summarizer is unavailable"}
	}
	summary, err := e.summarizer.SummarizeBody(ctx, evidence.URL, evidence.FullText)
	if err != nil {
		if ctx.Err() != nil {
			return gmailEvidence{}, ctx.Err()
		}
		code := strings.TrimSpace(err.Error())
		switch code {
		case "provider_error", "provider_unavailable", "timeout":
			return gmailEvidence{}, &GmailBriefRetryableError{Reason: "external body summarizer failed", Err: err}
		default:
			return gmailEvidence{}, err
		}
	}
	summaryHash, summaryHashErr := normalizedSHA256(summary.BodySHA256)
	evidenceHash, evidenceHashErr := normalizedSHA256(evidence.ContentHash)
	if summaryHashErr != nil || evidenceHashErr != nil || !strings.EqualFold(summaryHash, evidenceHash) {
		return gmailEvidence{}, fmt.Errorf("%w: external body summary hash does not match the complete source", ErrGmailBriefEvidenceInvalid)
	}
	if strings.TrimSpace(summary.Text) == "" || !utf8.ValidString(summary.Text) {
		return gmailEvidence{}, fmt.Errorf("%w: external body summary is empty or invalid", ErrGmailBriefEvidenceInvalid)
	}
	for _, quote := range summary.EvidenceQuotes {
		if !containsGmailEvidenceQuote(evidence.FullText, quote) {
			return gmailEvidence{}, fmt.Errorf("%w: external body summary evidence is not copied from the complete source", ErrGmailBriefEvidenceInvalid)
		}
	}
	evidence.Text = summary.Text
	evidence.SummaryEvidenceQuotes = append([]string(nil), summary.EvidenceQuotes...)
	return evidence, nil
}
