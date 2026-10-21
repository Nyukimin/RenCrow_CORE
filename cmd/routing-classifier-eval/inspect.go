package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/routing/classifierlab"
)

var routeHeading = regexp.MustCompile(`^###\s+(CHAT|PLAN|ANALYZE|OPS|RESEARCH|CODE[1-4]?|WILD)\s*$`)
var routeBulletLabelCapture = regexp.MustCompile(`^[-*]\s+(CHAT|PLAN|ANALYZE|OPS|RESEARCH|CODE[1-4]?|WILD)\s*:`)
var classifierParserRoutePair = regexp.MustCompile(`\{\s*"([A-Z][A-Z0-9]*)"\s*,\s*routing\.Route([A-Z][A-Z0-9]*)\s*\}`)
var explicitCode4Command = regexp.MustCompile(`\{\s*"/code4"\s*,\s*routing\.RouteCODE4\s*\}`)
var ruleDictionaryCode4Route = regexp.MustCompile(`route\s*:\s*routing\.RouteCODE4\b`)

type baselineManifest struct {
	SchemaVersion                    string                        `json:"schema_version"`
	CoreCommit                       string                        `json:"core_commit"`
	WorkingTreeDirty                 bool                          `json:"working_tree_dirty"`
	WorkingTreeDiffSHA256            string                        `json:"working_tree_diff_sha256"`
	WorkingTreeStatusSHA256          string                        `json:"working_tree_status_sha256"`
	WorkingTreeFingerprintScope      string                        `json:"working_tree_fingerprint_scope"`
	UntrackedSourceSHA256            map[string]string             `json:"untracked_source_sha256"`
	SourceSHA256                     map[string]string             `json:"source_sha256"`
	LegacyClassifier                 string                        `json:"legacy_classifier"`
	RuntimeClassifierWiring          string                        `json:"runtime_classifier_wiring"`
	RuntimeWiringLine                int                           `json:"runtime_wiring_line"`
	RepositoryPromptSHA256           string                        `json:"repository_prompt_sha256"`
	ClassifierParserAcceptedLabels   []string                      `json:"classifier_parser_accepted_labels"`
	RepositoryClassifierPromptLabels []string                      `json:"repository_classifier_prompt_labels"`
	Code4Reachability                code4ReachabilityEvidence     `json:"code4_reachability"`
	LoadedPromptSHA256               *string                       `json:"loaded_prompt_sha256"`
	LoadedPromptStatus               string                        `json:"loaded_prompt_status"`
	LegacyModelUnknown               bool                          `json:"legacy_model_unknown"`
	LegacyBaselineStatus             string                        `json:"legacy_baseline_status"`
	LegacyBaseline                   *classifierlab.LegacyBaseline `json:"legacy_baseline,omitempty"`
	LegacyPhysicalModelStatus        string                        `json:"legacy_physical_model_status"`
	LegacyProviderInvoked            bool                          `json:"legacy_provider_invoked"`
}

type code4ReachabilityEvidence struct {
	ExplicitCommandStatus                   string `json:"explicit_command_status"`
	ExplicitCommand                         string `json:"explicit_command,omitempty"`
	RuleDictionaryStatus                    string `json:"rule_dictionary_status"`
	NaturalLanguageAutomaticSelectionStatus string `json:"natural_language_automatic_selection_status"`
	NaturalLanguageAutomaticSelectionReason string `json:"natural_language_automatic_selection_reason"`
}

type policyReview struct {
	Status              string   `json:"semantic_review_status"`
	Conflict            string   `json:"conflict"`
	ConflictSourcePaths []string `json:"source_paths"`
	PolicySHA256        string   `json:"inspection_policy_sha256"`
	Usage               string   `json:"usage"`
}

type repositoryInspection struct {
	Baseline baselineManifest
	Policy   classifierlab.RoutePolicy
	Review   policyReview
}

func runInspect(configPath, outPath, repoRoot, coreConfigPath string) error {
	if _, err := loadExperimentConfig(configPath); err != nil {
		return err
	}
	inspection, err := inspectRepository(repoRoot)
	if err != nil {
		return err
	}
	if strings.TrimSpace(coreConfigPath) != "" {
		legacyBaseline, err := classifierlab.ResolveLegacyBaseline(coreConfigPath)
		if err != nil {
			return fmt.Errorf("resolve explicit CORE config baseline: %w", err)
		}
		inspection.Baseline.LegacyBaseline = &legacyBaseline
		inspection.Baseline.LegacyBaselineStatus = "CONFIG_RESOLVED_PROVIDER_NOT_CALLED"
		inspection.Baseline.LoadedPromptStatus = "CONFIG_RESOLVED_NOT_RUNTIME_OBSERVED"
	}
	directory, err := createPrivateOutputDirectory(outPath)
	if err != nil {
		return err
	}
	if err := writeJSONExclusive(filepath.Join(directory, "baseline_manifest.json"), inspection.Baseline); err != nil {
		return err
	}
	if err := writeJSONExclusive(filepath.Join(directory, classifierlab.PreparedPolicyFileName), inspection.Policy); err != nil {
		return err
	}
	if err := writeJSONExclusive(filepath.Join(directory, "policy_review.json"), inspection.Review); err != nil {
		return err
	}
	return nil
}

func inspectRepository(repoRoot string) (repositoryInspection, error) {
	paths := []string{
		"internal/infrastructure/routing/llm_classifier.go",
		"internal/domain/routing/route.go",
		"internal/domain/agent/mio_persona.go",
		"internal/infrastructure/routing/rule_dictionary.go",
		"rules/routing-policy.md",
		"prompts/classifier.md",
		"cmd/rencrow/runtime_dependencies.go",
		"internal/adapter/config/prompts.go",
	}
	hashes := make(map[string]string, len(paths))
	contents := make(map[string][]byte, len(paths))
	for _, path := range paths {
		data, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(path)))
		if err != nil {
			return repositoryInspection{}, fmt.Errorf("read inspected source %s: %w", path, err)
		}
		contents[path] = data
		hashes[path] = sha256Hex(data)
	}
	commit, err := gitOutput(repoRoot, "rev-parse", "HEAD")
	if err != nil {
		return repositoryInspection{}, fmt.Errorf("read CORE commit: %w", err)
	}
	gitStatus, err := gitOutput(repoRoot, "status", "--porcelain=v1", "--untracked-files=all", "-z")
	if err != nil {
		return repositoryInspection{}, fmt.Errorf("read CORE working-tree status: %w", err)
	}
	diff, err := gitOutput(repoRoot, "diff", "--no-ext-diff", "--binary", "HEAD")
	if err != nil {
		return repositoryInspection{}, fmt.Errorf("hash CORE working-tree diff: %w", err)
	}
	untrackedHashes, err := hashUntrackedSources(repoRoot, gitStatus)
	if err != nil {
		return repositoryInspection{}, err
	}
	promptSHA := hashes["prompts/classifier.md"]
	policy, err := buildInspectionPolicy(contents["rules/routing-policy.md"], promptSHA)
	if err != nil {
		return repositoryInspection{}, err
	}
	policyStatus, conflict := inspectCode4SourceAgreement(
		contents["internal/domain/routing/route.go"],
		policy.Criteria["CODE4"],
	)
	parserLabels := classifierParserAcceptedLabels(contents["internal/infrastructure/routing/llm_classifier.go"])
	if len(parserLabels) == 0 {
		return repositoryInspection{}, errors.New("classifier parser accepted-label set could not be read from LLMClassifier.parseRouteWithMatch")
	}
	promptLabels := repositoryPromptLabels(contents["prompts/classifier.md"])
	code4Reachability := inspectCode4Reachability(
		parserLabels,
		promptLabels,
		contents["internal/domain/agent/mio_persona.go"],
		contents["internal/infrastructure/routing/rule_dictionary.go"],
	)
	policy.SemanticReviewStatus = policyStatus
	canonicalPolicy, err := canonicalJSON(policy)
	if err != nil {
		return repositoryInspection{}, err
	}
	wiring, wiringLine := findRuntimeWiring(contents["cmd/rencrow/runtime_dependencies.go"])
	if wiring == "" {
		return repositoryInspection{}, errors.New("runtime classifier wiring was not found; baseline is unresolved")
	}
	baseline := baselineManifest{
		SchemaVersion:                    classifierlab.SchemaVersion,
		CoreCommit:                       strings.TrimSpace(commit),
		WorkingTreeDirty:                 strings.TrimSpace(gitStatus) != "",
		WorkingTreeDiffSHA256:            sha256Hex([]byte(diff)),
		WorkingTreeStatusSHA256:          sha256Hex([]byte(gitStatus)),
		WorkingTreeFingerprintScope:      "tracked diff from HEAD plus git status and hashes of untracked Go, Python, Markdown, JSON, and YAML source files; non-source artifacts are excluded",
		UntrackedSourceSHA256:            untrackedHashes,
		SourceSHA256:                     hashes,
		LegacyClassifier:                 "routing.LLMClassifier; model identity is not observable from source inspection",
		RuntimeClassifierWiring:          wiring,
		RuntimeWiringLine:                wiringLine,
		RepositoryPromptSHA256:           promptSHA,
		ClassifierParserAcceptedLabels:   parserLabels,
		RepositoryClassifierPromptLabels: promptLabels,
		Code4Reachability:                code4Reachability,
		LoadedPromptSHA256:               nil,
		LoadedPromptStatus:               "UNOBSERVED_WORKSPACE_OVERRIDE_POSSIBLE",
		LegacyModelUnknown:               true,
		LegacyBaselineStatus:             "UNOBSERVED_CORE_CONFIG_NOT_SUPPLIED",
		LegacyPhysicalModelStatus:        "UNKNOWN_NOT_OBSERVED",
		LegacyProviderInvoked:            false,
	}
	policyHash := sha256Hex(canonicalPolicy)
	conflictSources := []string(nil)
	if conflict != "" {
		conflictSources = []string{"internal/domain/routing/route.go", "rules/routing-policy.md"}
	}
	review := policyReview{
		Status:              policyStatus,
		Conflict:            conflict,
		ConflictSourcePaths: conflictSources,
		PolicySHA256:        policyHash,
		Usage:               "inspection-only; semantic review and an explicit verified policy are required before preparation",
	}
	return repositoryInspection{Baseline: baseline, Policy: policy, Review: review}, nil
}

func hashUntrackedSources(repoRoot, status string) (map[string]string, error) {
	hashes := make(map[string]string)
	allowed := map[string]bool{".go": true, ".py": true, ".md": true, ".json": true, ".yaml": true, ".yml": true}
	for _, entry := range strings.Split(status, "\x00") {
		if len(entry) < 4 || entry[:2] != "??" {
			continue
		}
		relative := entry[3:]
		if !allowed[strings.ToLower(filepath.Ext(relative))] {
			continue
		}
		path := filepath.Join(repoRoot, filepath.FromSlash(relative))
		info, err := os.Lstat(path)
		if err != nil {
			return nil, fmt.Errorf("inspect untracked source %s: %w", relative, err)
		}
		if !info.Mode().IsRegular() {
			continue
		}
		file, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("open untracked source %s: %w", relative, err)
		}
		data, readErr := io.ReadAll(io.LimitReader(file, 32<<20+1))
		closeErr := file.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read untracked source %s: %w", relative, readErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close untracked source %s: %w", relative, closeErr)
		}
		if len(data) > 32<<20 {
			return nil, fmt.Errorf("untracked source %s exceeds 32 MiB; fingerprint is incomplete", relative)
		}
		hashes[filepath.ToSlash(relative)] = sha256Hex(data)
	}
	return hashes, nil
}

func buildInspectionPolicy(policySource []byte, promptSHA string) (classifierlab.RoutePolicy, error) {
	routes := []classifierlab.RouteLabel{
		classifierlab.LabelCHAT, classifierlab.LabelPLAN, classifierlab.LabelANALYZE,
		classifierlab.LabelOPS, classifierlab.LabelRESEARCH, classifierlab.LabelCODE,
		classifierlab.LabelCODE1, classifierlab.LabelCODE2, classifierlab.LabelCODE3,
		classifierlab.LabelCODE4, classifierlab.LabelWILD,
	}
	criteria := make(map[string]string, len(routes))
	lines := strings.Split(string(policySource), "\n")
	current := ""
	var section strings.Builder
	flush := func() {
		if current != "" {
			value := strings.TrimSpace(section.String())
			if value != "" {
				criteria[current] = value
			}
		}
		section.Reset()
	}
	for _, line := range lines {
		if matches := routeHeading.FindStringSubmatch(line); len(matches) == 2 {
			flush()
			current = matches[1]
			continue
		}
		if current != "" {
			if strings.HasPrefix(line, "## ") || strings.HasPrefix(line, "### ") {
				flush()
				current = ""
				continue
			}
			section.WriteString(line)
			section.WriteByte('\n')
		}
	}
	flush()
	for _, route := range routes {
		if strings.TrimSpace(criteria[string(route)]) == "" {
			return classifierlab.RoutePolicy{}, fmt.Errorf("routing policy source has no criteria section for %s", route)
		}
	}
	policy := classifierlab.RoutePolicy{
		PolicyVersion:        "rencrow-route-policy-v1",
		Routes:               make([]string, len(routes)),
		Instructions:         "Inspection-only criteria extracted from routing-policy.md; a verified policy is required before model preparation.",
		Criteria:             criteria,
		SourcePaths:          []string{"rules/routing-policy.md", "internal/domain/routing/route.go", "prompts/classifier.md"},
		LegacyPromptSHA256:   promptSHA,
		SemanticReviewStatus: "POLICY_UNDEFINED",
	}
	for index, route := range routes {
		policy.Routes[index] = string(route)
	}
	return policy, nil
}

func inspectCode4SourceAgreement(routeSource []byte, policyCriteria string) (string, string) {
	routeMeaning := code4RouteComment(string(routeSource))
	policyMeaning := code4PolicyMeaning(policyCriteria)
	routeDomains := code4Domains(routeMeaning)
	policyDomains := code4Domains(policyMeaning)
	if len(routeDomains) == 0 || len(policyDomains) == 0 || !sameStringSet(routeDomains, policyDomains) {
		return "POLICY_UNDEFINED", "CODE4 definitions in route.go and rules/routing-policy.md are absent or semantically inconsistent"
	}
	return "SOURCE_ALIGNED_REVIEW_REQUIRED", ""
}

func code4PolicyMeaning(criteria string) string {
	lines := strings.Split(criteria, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "### ") {
			continue
		}
		lower := strings.ToLower(trimmed)
		for _, marker := range []string{"意味:", "意味：", "meaning:"} {
			if index := strings.Index(lower, marker); index >= 0 {
				return firstPolicySentence(strings.TrimSpace(trimmed[index+len(marker):]))
			}
		}
	}
	for _, line := range lines {
		trimmed := strings.TrimSpace(strings.TrimLeft(line, "-* "))
		if trimmed != "" && !strings.HasPrefix(trimmed, "### ") {
			return firstPolicySentence(trimmed)
		}
	}
	return ""
}

func firstPolicySentence(value string) string {
	end := len(value)
	for _, punctuation := range []string{"。", ".", ";", "\n"} {
		if index := strings.Index(value, punctuation); index >= 0 && index < end {
			end = index
		}
	}
	return strings.TrimSpace(value[:end])
}

func classifierParserAcceptedLabels(source []byte) []string {
	labels := make([]string, 0, 11)
	seen := make(map[string]struct{}, 11)
	for _, match := range classifierParserRoutePair.FindAllSubmatch(source, -1) {
		label := string(match[1])
		if label != string(match[2]) {
			continue
		}
		if _, exists := seen[label]; exists {
			continue
		}
		seen[label] = struct{}{}
		labels = append(labels, label)
	}
	return labels
}

func repositoryPromptLabels(source []byte) []string {
	labels := make([]string, 0, 11)
	seen := make(map[string]struct{}, 11)
	for _, line := range strings.Split(string(source), "\n") {
		match := routeBulletLabelCapture.FindStringSubmatch(strings.TrimSpace(line))
		if len(match) != 2 {
			continue
		}
		if _, exists := seen[match[1]]; exists {
			continue
		}
		seen[match[1]] = struct{}{}
		labels = append(labels, match[1])
	}
	return labels
}

func inspectCode4Reachability(parserLabels, promptLabels []string, commandSource, ruleDictionarySource []byte) code4ReachabilityEvidence {
	evidence := code4ReachabilityEvidence{
		ExplicitCommandStatus:                   "NOT_FOUND",
		RuleDictionaryStatus:                    "NO_DEDICATED_RULE",
		NaturalLanguageAutomaticSelectionStatus: "NOT_ESTABLISHED",
		NaturalLanguageAutomaticSelectionReason: "classifier parser acceptance, prompt inventory, and RuleDictionary reachability describe different stages; prompt omission alone does not prove automatic unreachability",
	}
	if explicitCode4Command.Match(commandSource) {
		evidence.ExplicitCommandStatus = "REACHABLE"
		evidence.ExplicitCommand = "/code4"
	}
	if ruleDictionaryCode4Route.Match(ruleDictionarySource) {
		evidence.RuleDictionaryStatus = "DEDICATED_RULE_PRESENT"
	}
	if !hasLabel(parserLabels, "CODE4") {
		evidence.NaturalLanguageAutomaticSelectionStatus = "PARSER_REJECTS_LABEL"
		evidence.NaturalLanguageAutomaticSelectionReason = "LLMClassifier parser does not accept CODE4 as output"
	} else if !hasLabel(promptLabels, "CODE4") && evidence.RuleDictionaryStatus == "NO_DEDICATED_RULE" {
		evidence.NaturalLanguageAutomaticSelectionStatus = "UNDEFINED"
		evidence.NaturalLanguageAutomaticSelectionReason = "the parser accepts CODE4, but the legacy prompt has no CODE4 label and RuleDictionary has no dedicated CODE4 rule; natural-language classifier selection is undefined. This does not assert automatic unreachability"
	}
	return evidence
}

func hasLabel(labels []string, expected string) bool {
	for _, label := range labels {
		if label == expected {
			return true
		}
	}
	return false
}

func code4RouteComment(source string) string {
	for _, line := range strings.Split(source, "\n") {
		if !strings.Contains(line, "RouteCODE4") {
			continue
		}
		comment := strings.Index(line, "//")
		if comment >= 0 {
			return strings.TrimSpace(line[comment+2:])
		}
	}
	return ""
}

func code4Domains(value string) map[string]struct{} {
	value = strings.ToLower(value)
	domains := make(map[string]struct{}, 2)
	for _, marker := range []string{"高速", "実験", "high-speed", "fast coding", "fast prototyping", "experiment"} {
		if strings.Contains(value, strings.ToLower(marker)) {
			domains["fast_or_experiment"] = struct{}{}
			break
		}
	}
	for _, marker := range []string{"比較", "レビュー", "review", "compare", "決定材料", "採用案", "仕上げ"} {
		if strings.Contains(value, strings.ToLower(marker)) {
			if !containsCode4ComparisonRejection(value) {
				domains["comparison_or_review"] = struct{}{}
			}
			break
		}
	}
	return domains
}

func containsCode4ComparisonRejection(value string) bool {
	for _, phrase := range []string{
		"比較レビューや仕上げのrouteではない",
		"比較レビューや仕上げrouteではない",
		"比較レビューrouteではなく",
		"not a comparison review route",
		"not a comparison/review route",
	} {
		if strings.Contains(value, phrase) {
			return true
		}
	}
	return false
}

func sameStringSet(left, right map[string]struct{}) bool {
	if len(left) != len(right) {
		return false
	}
	for value := range left {
		if _, exists := right[value]; !exists {
			return false
		}
	}
	return true
}

func findRuntimeWiring(data []byte) (string, int) {
	for lineNumber, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, "buildRuntimeClassifier(llmRuntime.Chat, cfg.Prompts.Classifier, classifierTestConfig)") {
			return "buildRuntimeClassifier(llmRuntime.Chat, cfg.Prompts.Classifier, classifierTestConfig)", lineNumber + 1
		}
		if strings.Contains(trimmed, "routing.NewLLMClassifier(llmRuntime.Chat, cfg.Prompts.Classifier)") {
			return trimmed, lineNumber + 1
		}
	}
	return "", 0
}

func gitOutput(repoRoot string, arguments ...string) (string, error) {
	command := exec.Command("git", append([]string{"-C", repoRoot}, arguments...)...)
	output, err := command.Output()
	if err != nil {
		return "", err
	}
	return string(output), nil
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func canonicalJSON(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("canonicalize inspection artifact: %w", err)
	}
	return data, nil
}
