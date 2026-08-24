package main

import (
	"context"
	"fmt"
	"strings"

	capdomain "github.com/Nyukimin/RenCrow_CORE/internal/domain/capability"
	domaincontext "github.com/Nyukimin/RenCrow_CORE/internal/domain/context"
	domainskill "github.com/Nyukimin/RenCrow_CORE/internal/domain/skillgovernance"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/mcp"
)

// runtimeMCPObservation is an explicit, already-observed MCP capability.
// It intentionally contains no command, environment, or filesystem path.
type runtimeMCPObservation struct {
	ServerName  string
	ToolName    string
	ExposedName string
	Description string
	Origin      string
	Reason      string
	Available   bool
}

const runtimeSkillCatalogOrigin = "skill_catalog"

type runtimeSkillGovernanceState struct {
	found       bool
	disabled    bool
	description string
	origin      string
}

// buildRuntimeCapabilitySnapshot converts runtime observations into the
// domain snapshot without reading files, calling tools, or making policy
// decisions. This compatibility form has no loaded SKILL.md metadata.
func buildRuntimeCapabilitySnapshot(
	toolMetadata []domaintool.ToolMetadata,
	skillManifests []domainskill.SkillManifest,
	mcpObservations []runtimeMCPObservation,
) capdomain.RuntimeCapabilitySnapshot {
	return buildRuntimeCapabilitySnapshotWithSkills(toolMetadata, nil, skillManifests, mcpObservations)
}

// buildRuntimeCapabilitySnapshotWithSkills unions loaded SKILL.md metadata
// with governance state. A disabled or conflicting governance state always
// wins over an available loaded body.
func buildRuntimeCapabilitySnapshotWithSkills(
	toolMetadata []domaintool.ToolMetadata,
	loadedSkills []domaincontext.SkillMetadata,
	skillManifests []domainskill.SkillManifest,
	mcpObservations []runtimeMCPObservation,
) capdomain.RuntimeCapabilitySnapshot {
	entries := make([]capdomain.RuntimeCapability, 0, len(toolMetadata)+len(loadedSkills)+len(skillManifests)+len(mcpObservations))
	for _, metadata := range toolMetadata {
		// Namespaced MCP adapters are represented in the MCP section from their
		// explicit observations, not duplicated as ordinary CORE Tools.
		if strings.HasPrefix(strings.TrimSpace(metadata.ToolID), "mcp.") {
			continue
		}
		entries = append(entries, capdomain.RuntimeCapability{
			Kind:        capdomain.CapabilityKindTool,
			Status:      capdomain.CapabilityStatusAvailable,
			Name:        metadata.ToolID,
			Description: metadata.Description,
			Origin:      metadata.Origin,
		})
	}

	governance := runtimeSkillGovernanceStates(skillManifests)
	loadedKeys := make(map[string]bool, len(loadedSkills)*2)
	for _, skill := range loadedSkills {
		name := runtimeSkillMetadataName(skill)
		if name == "" {
			continue
		}
		entry := capdomain.RuntimeCapability{
			Kind:        capdomain.CapabilityKindSkill,
			Status:      capdomain.CapabilityStatusAvailable,
			Name:        name,
			Description: skill.Description,
			Origin:      runtimeSkillCatalogOrigin,
		}
		for _, identity := range runtimeSkillMetadataIdentities(skill) {
			loadedKeys[runtimeSkillIdentity(identity)] = true
		}
		if state, ok := runtimeSkillGovernanceForMetadata(skill, governance); ok {
			if entry.Description == "" {
				entry.Description = state.description
			}
			if state.origin != "" {
				entry.Origin = state.origin
			}
			if state.disabled {
				entry.Status = capdomain.CapabilityStatusUnavailable
				entry.Reason = "Skill governanceで無効化または競合しています"
			}
		}
		entries = append(entries, entry)
	}
	for _, manifest := range skillManifests {
		if loadedSkills == nil {
			entry := capdomain.RuntimeCapability{
				Kind:        capdomain.CapabilityKindSkill,
				Status:      capdomain.CapabilityStatusAvailable,
				Name:        strings.TrimSpace(manifest.SkillID),
				Description: manifest.Description,
				Origin:      manifest.Scope,
			}
			if entry.Name == "" {
				entry.Name = strings.TrimSpace(manifest.Name)
			}
			if !manifest.Enabled {
				entry.Status = capdomain.CapabilityStatusUnavailable
				entry.Reason = "Skillが無効化されています"
			}
			entries = append(entries, entry)
			continue
		}
		if runtimeSkillManifestMatchesLoaded(manifest, loadedKeys) {
			continue
		}
		name := strings.TrimSpace(manifest.SkillID)
		if name == "" {
			name = strings.TrimSpace(manifest.Name)
		}
		entry := capdomain.RuntimeCapability{
			Kind:        capdomain.CapabilityKindSkill,
			Status:      capdomain.CapabilityStatusUnavailable,
			Name:        name,
			Description: manifest.Description,
			Origin:      manifest.Scope,
		}
		if !manifest.Enabled {
			entry.Status = capdomain.CapabilityStatusUnavailable
			entry.Reason = "Skill governanceで無効化されています"
		} else {
			entry.Reason = "SKILL.mdがロードされていません"
		}
		entries = append(entries, entry)
	}
	for _, observation := range mcpObservations {
		name := runtimeMCPObservationDisplayName(observation)
		entry := capdomain.RuntimeCapability{
			Kind:        capdomain.CapabilityKindMCP,
			Status:      capdomain.CapabilityStatusUnavailable,
			Name:        name,
			Description: observation.Description,
			Origin:      observation.Origin,
			Reason:      observation.Reason,
		}
		if observation.Available {
			entry.Status = capdomain.CapabilityStatusAvailable
		}
		entries = append(entries, entry)
	}
	return capdomain.RuntimeCapabilitySnapshot{Entries: entries}
}

func runtimeSkillMetadataName(skill domaincontext.SkillMetadata) string {
	name := strings.TrimSpace(skill.Name)
	if name == "" {
		name = strings.TrimSpace(skill.DirName)
	}
	return name
}

func runtimeSkillMetadataIdentities(skill domaincontext.SkillMetadata) []string {
	identities := []string{runtimeSkillMetadataName(skill)}
	if dirName := strings.TrimSpace(skill.DirName); dirName != "" {
		identities = append(identities, dirName)
	}
	return identities
}

func runtimeSkillIdentity(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func runtimeSkillGovernanceStates(manifests []domainskill.SkillManifest) map[string]runtimeSkillGovernanceState {
	states := make(map[string]runtimeSkillGovernanceState, len(manifests)*2)
	for _, manifest := range manifests {
		for _, identity := range []string{manifest.SkillID, manifest.Name} {
			key := runtimeSkillIdentity(identity)
			if key == "" {
				continue
			}
			state := states[key]
			state.found = true
			state.disabled = state.disabled || !manifest.Enabled
			if state.description == "" {
				state.description = manifest.Description
			}
			if state.origin == "" {
				state.origin = manifest.Scope
			}
			states[key] = state
		}
	}
	return states
}

func runtimeSkillGovernanceForMetadata(skill domaincontext.SkillMetadata, states map[string]runtimeSkillGovernanceState) (runtimeSkillGovernanceState, bool) {
	var combined runtimeSkillGovernanceState
	for _, identity := range runtimeSkillMetadataIdentities(skill) {
		state, ok := states[runtimeSkillIdentity(identity)]
		if !ok {
			continue
		}
		combined.found = true
		combined.disabled = combined.disabled || state.disabled
		if combined.description == "" {
			combined.description = state.description
		}
		if combined.origin == "" {
			combined.origin = state.origin
		}
	}
	return combined, combined.found
}

func runtimeSkillManifestMatchesLoaded(manifest domainskill.SkillManifest, loadedKeys map[string]bool) bool {
	return loadedKeys[runtimeSkillIdentity(manifest.SkillID)] || loadedKeys[runtimeSkillIdentity(manifest.Name)]
}

// buildRuntimeCapabilityContext renders one stable capability section from
// explicit observations. The rendered text does not grant execution access.
func buildRuntimeCapabilityContext(
	toolMetadata []domaintool.ToolMetadata,
	skillManifests []domainskill.SkillManifest,
	mcpObservations []runtimeMCPObservation,
) string {
	return buildRuntimeCapabilityContextWithSkills(toolMetadata, nil, skillManifests, mcpObservations)
}

func buildRuntimeCapabilityContextWithSkills(
	toolMetadata []domaintool.ToolMetadata,
	loadedSkills []domaincontext.SkillMetadata,
	skillManifests []domainskill.SkillManifest,
	mcpObservations []runtimeMCPObservation,
) string {
	return capdomain.RenderStableRuntimeContext(buildRuntimeCapabilitySnapshotWithSkills(toolMetadata, loadedSkills, skillManifests, mcpObservations))
}

// buildRuntimeCapabilityContextWithToolFailure is the fail-closed path for a
// Worker RunnerV2 ListTools error. It never exposes the underlying error,
// which may contain a path or another sensitive value.
func buildRuntimeCapabilityContextWithToolFailure(
	skillManifests []domainskill.SkillManifest,
	mcpObservations []runtimeMCPObservation,
	reason string,
) string {
	return buildRuntimeCapabilityContextWithToolFailureAndSkills(nil, skillManifests, mcpObservations, reason)
}

func buildRuntimeCapabilityContextWithToolFailureAndSkills(
	loadedSkills []domaincontext.SkillMetadata,
	skillManifests []domainskill.SkillManifest,
	mcpObservations []runtimeMCPObservation,
	reason string,
) string {
	snapshot := buildRuntimeCapabilitySnapshotWithSkills(nil, loadedSkills, skillManifests, mcpObservations)
	snapshot.Entries = append(snapshot.Entries, capdomain.RuntimeCapability{
		Kind:   capdomain.CapabilityKindTool,
		Status: capdomain.CapabilityStatusUnavailable,
		Name:   "worker_toolrunner",
		Reason: reason,
	})
	return capdomain.RenderStableRuntimeContext(snapshot)
}

// runtimeCapabilityContextFromWorkerRunner obtains the production Worker
// metadata once. A ListTools failure produces no available Tool entries.
func runtimeCapabilityContextFromWorkerRunner(
	ctx context.Context,
	runner domaintool.RunnerV2,
	skillManifests []domainskill.SkillManifest,
	mcpObservations []runtimeMCPObservation,
) string {
	return runtimeCapabilityContextFromWorkerRunnerWithSkills(ctx, runner, nil, skillManifests, mcpObservations)
}

func runtimeCapabilityContextFromWorkerRunnerWithSkills(
	ctx context.Context,
	runner domaintool.RunnerV2,
	loadedSkills []domaincontext.SkillMetadata,
	skillManifests []domainskill.SkillManifest,
	mcpObservations []runtimeMCPObservation,
) string {
	if runner == nil {
		return buildRuntimeCapabilityContextWithToolFailureAndSkills(loadedSkills, skillManifests, mcpObservations, "Worker ToolRunnerが未接続です")
	}
	metadata, err := runner.ListTools(ctx)
	if err != nil {
		return buildRuntimeCapabilityContextWithToolFailureAndSkills(loadedSkills, skillManifests, mcpObservations, "Worker Tool一覧を取得できません")
	}
	return buildRuntimeCapabilityContextWithSkills(metadata, loadedSkills, skillManifests, mcpObservations)
}

// observeGenericMCPClient collects only explicit generic MCP server/tool
// observations. An empty client therefore yields no available MCP entries.
func observeGenericMCPClient(ctx context.Context, client *mcp.MCPClient) []runtimeMCPObservation {
	if client == nil {
		return nil
	}
	var observations []runtimeMCPObservation
	for _, serverName := range client.ListServers() {
		toolNames, err := client.ListTools(ctx, serverName)
		if err != nil {
			observations = append(observations, runtimeMCPObservation{
				ServerName: serverName,
				Reason:     "MCP Tool一覧を取得できません",
			})
			continue
		}
		if len(toolNames) == 0 {
			observations = append(observations, runtimeMCPObservation{
				ServerName: serverName,
				Reason:     "MCP Toolが未観測です",
			})
			continue
		}
		for _, toolName := range toolNames {
			observations = append(observations, runtimeMCPObservation{
				ServerName: serverName,
				ToolName:   toolName,
				Origin:     serverName,
				Available:  true,
			})
		}
	}
	return observations
}

func runtimeMCPObservationDisplayName(observation runtimeMCPObservation) string {
	if exposedName := strings.TrimSpace(observation.ExposedName); exposedName != "" {
		return exposedName
	}
	return runtimeMCPObservationName(observation.ServerName, observation.ToolName)
}

func runtimeMCPObservationName(serverName, toolName string) string {
	serverName = strings.TrimSpace(serverName)
	toolName = strings.TrimSpace(toolName)
	switch {
	case serverName == "":
		return toolName
	case toolName == "":
		return serverName
	default:
		return serverName + "." + toolName
	}
}

// appendRuntimeCapabilityContext keeps the character prompt untouched and
// adds the snapshot as a separate stable section to every existing runtime
// context entry.
func appendRuntimeCapabilityContext(contexts map[string]string, capabilityContext string) {
	capabilityContext = strings.TrimSpace(capabilityContext)
	if capabilityContext == "" {
		return
	}
	for name, contextText := range contexts {
		projectedContext := capabilityContext
		if strings.EqualFold(strings.TrimSpace(name), "mio") {
			projectedContext = summarizeRuntimeCapabilityContextForMio(capabilityContext)
		}
		contextText = strings.TrimSpace(contextText)
		if contextText == "" {
			contexts[name] = projectedContext
			continue
		}
		contexts[name] = contextText + "\n\n" + projectedContext
	}
}

// summarizeRuntimeCapabilityContextForMio keeps capability awareness without
// placing the Worker-owned execution catalog in the conversational prompt.
// Mio routes work; CORE policy and the Worker runtime own exact availability.
func summarizeRuntimeCapabilityContextForMio(capabilityContext string) string {
	type counts struct{ available, unavailable int }
	sections := map[string]*counts{
		"tools":  {},
		"skills": {},
		"mcp":    {},
	}
	current := ""
	for _, rawLine := range strings.Split(capabilityContext, "\n") {
		line := strings.TrimSpace(rawLine)
		switch line {
		case "### Tools":
			current = "tools"
		case "### Skills":
			current = "skills"
		case "### MCP":
			current = "mcp"
		default:
			section := sections[current]
			if section == nil || strings.HasSuffix(line, "なし") {
				continue
			}
			if strings.HasPrefix(line, "- 利用可能: ") {
				section.available++
			}
			if strings.HasPrefix(line, "- 利用不可: ") {
				section.unavailable++
			}
		}
	}
	return fmt.Sprintf(`## Runtime Capability Snapshot
- projection: summary_for_mio
- full_snapshot_owner: CORE Worker runtime
- tools: available=%d unavailable=%d
- skills: available=%d unavailable=%d
- mcp: available=%d unavailable=%d
- exact availability and permission are evaluated by CORE policy at execution time; Mio returns the required capability to the Orchestrator.`,
		sections["tools"].available, sections["tools"].unavailable,
		sections["skills"].available, sections["skills"].unavailable,
		sections["mcp"].available, sections["mcp"].unavailable,
	)
}
