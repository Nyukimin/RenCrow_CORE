package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	domaincontext "github.com/Nyukimin/RenCrow_CORE/internal/domain/context"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

// MaxSkillBodyBytes bounds the trusted instruction body retained in memory
// and returned by skill.read.
const MaxSkillBodyBytes = 64 * 1024

const skillBodyTruncationNotice = "\n\n[skill body truncated]"

var (
	ErrSkillNotFound    = errors.New("skill not found")
	ErrSkillNameInvalid = errors.New("invalid skill name")
)

type skillCatalogEntry struct {
	name        string
	description string
	body        string
}

// SkillCatalog is an immutable, startup-loaded instruction catalog.
// It has no filesystem dependency after construction.
type SkillCatalog struct {
	entries map[string]skillCatalogEntry
}

// NewSkillCatalog copies valid skill metadata into an in-memory catalog.
// Input order is preserved for duplicate names, matching configured root
// priority established by SkillsLoader.
func NewSkillCatalog(skills []domaincontext.SkillMetadata) *SkillCatalog {
	catalog := &SkillCatalog{entries: make(map[string]skillCatalogEntry, len(skills))}
	for _, skill := range skills {
		name := canonicalSkillName(skill.Name)
		if !validSkillName(name) {
			continue
		}
		if strings.TrimSpace(skill.BodyText) == "" {
			continue
		}
		if _, exists := catalog.entries[name]; exists {
			continue
		}
		catalog.entries[name] = skillCatalogEntry{
			name:        name,
			description: strings.TrimSpace(skill.Description),
			body:        boundSkillBody(skill.BodyText),
		}
	}
	return catalog
}

// Len reports how many skills can be read from this catalog.
func (c *SkillCatalog) Len() int {
	if c == nil {
		return 0
	}
	return len(c.entries)
}

// Read returns a bounded body for an exact known skill name.
// It never consults the filesystem and never interprets the body.
func (c *SkillCatalog) Read(name string) (string, error) {
	if !validSkillName(name) || name != strings.TrimSpace(name) {
		return "", fmt.Errorf("%w: %q", ErrSkillNameInvalid, name)
	}
	if c == nil {
		return "", ErrSkillNotFound
	}
	entry, ok := c.entries[canonicalSkillName(name)]
	if !ok {
		return "", fmt.Errorf("%w: %q", ErrSkillNotFound, name)
	}
	return entry.body, nil
}

func canonicalSkillName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func validSkillName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	if strings.ContainsAny(name, `/\\:`) {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func boundSkillBody(body string) string {
	body = strings.TrimSpace(body)
	if len(body) <= MaxSkillBodyBytes {
		return body
	}
	limit := MaxSkillBodyBytes - len(skillBodyTruncationNotice)
	if limit < 0 {
		return skillBodyTruncationNotice
	}
	for limit > 0 && !utf8.ValidString(body[:limit]) {
		limit--
	}
	return body[:limit] + skillBodyTruncationNotice
}

func (r *ToolRunner) registerSkillReadTool() {
	if r.config.SkillCatalog == nil || r.config.SkillCatalog.Len() == 0 {
		return
	}
	r.toolsV2["skill.read"] = r.executeSkillReadV2
}

func (r *ToolRunner) executeSkillReadV2(_ context.Context, args map[string]interface{}) (*tool.ToolResponse, error) {
	rawName, ok := args["name"]
	if !ok {
		return tool.NewError(tool.ErrValidationFailed, "skill.read requires an exact name", map[string]any{"field": "name"}), nil
	}
	name, ok := rawName.(string)
	if !ok {
		return tool.NewError(tool.ErrValidationFailed, "skill.read name must be a string", map[string]any{"field": "name"}), nil
	}
	body, err := r.config.SkillCatalog.Read(name)
	if err != nil {
		if errors.Is(err, ErrSkillNameInvalid) {
			return tool.NewError(tool.ErrValidationFailed, "skill.read accepts an exact skill name, not a path", nil), nil
		}
		return tool.NewError(tool.ErrNotFound, "skill.read name is not loaded", nil), nil
	}
	return tool.NewSuccess(body), nil
}
