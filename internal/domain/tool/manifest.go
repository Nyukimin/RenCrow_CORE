package tool

import "fmt"

// SideEffect はツールの副作用種別
type SideEffect string

const (
	SideEffectNone       SideEffect = "none"
	SideEffectLocalWrite SideEffect = "local_write"
	SideEffectNetwork    SideEffect = "network"
	SideEffectProcess    SideEffect = "process"
)

// ToolManifest はツールの公開契約
// v1 では既存 ToolMetadata をラップして利用する。
type ToolManifest struct {
	ID           string         `json:"id"`
	Version      string         `json:"version"`
	Description  string         `json:"description,omitempty"`
	InputSchema  map[string]any `json:"input_schema,omitempty"`
	OutputSchema map[string]any `json:"output_schema,omitempty"`
	SideEffect   SideEffect     `json:"side_effect"`
	TimeoutSec   int            `json:"timeout_sec,omitempty"`
}

func (m ToolManifest) Validate() error {
	if m.ID == "" {
		return fmt.Errorf("manifest id is required")
	}
	if m.Version == "" {
		return fmt.Errorf("manifest version is required")
	}
	switch m.SideEffect {
	case SideEffectNone, SideEffectLocalWrite, SideEffectNetwork, SideEffectProcess:
		return nil
	default:
		return fmt.Errorf("invalid side effect: %s", m.SideEffect)
	}
}

// ManifestFromMetadata converts tool metadata to a manifest.
func ManifestFromMetadata(meta ToolMetadata) ToolManifest {
	sideEffect := SideEffectNone
	switch meta.Category {
	case "mutation":
		sideEffect = SideEffectLocalWrite
	case "admin":
		sideEffect = SideEffectProcess
	}
	return ToolManifest{
		ID:          meta.ToolID,
		Version:     meta.Version,
		Description: meta.Description,
		InputSchema: meta.Parameters,
		SideEffect:  sideEffect,
	}
}
