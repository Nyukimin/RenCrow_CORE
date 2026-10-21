package speecheval

import (
	"fmt"
	"regexp"
)

const (
	publicSchemaVersion = "rencrow.speech-eval/1"
	answerSchemaVersion = "rencrow.speech-eval-answer/1"
)

var (
	opaqueIDPattern  = regexp.MustCompile(`^[a-f0-9]{32}$`)
	opaqueWAVPattern = regexp.MustCompile(`^[a-f0-9]{32}\.wav$`)
	sha256HexPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

type AudioArtifact struct {
	Filename        string
	ProfileRevision int
	Seed            uint64
	SHA256          string
	SynthesisMS     uint64
}

type RenderedCase struct {
	Case      Case
	SessionID string
	TurnID    string
	AVariant  string
	BVariant  string
	A         AudioArtifact
	B         AudioArtifact
}

// Manifest is the only metadata file intended for the Viewer audio root.
// Keep it limited to playback links and blind case labels.
type Manifest struct {
	SchemaVersion string         `json:"schema_version"`
	SetID         string         `json:"set_id"`
	Items         []ManifestItem `json:"items"`
}

type ManifestItem struct {
	ID            string `json:"id"`
	Text          string `json:"text"`
	Intent        string `json:"intent"`
	CueApplicable bool   `json:"cue_applicable"`
	CueHint       string `json:"cue_hint"`
	AURL          string `json:"a_url"`
	BURL          string `json:"b_url"`
}

// Answer contains evaluator-only mappings and synthesis metadata. Callers
// must persist this outside the configured Viewer audio root.
type Answer struct {
	SchemaVersion string           `json:"schema_version"`
	SetID         string           `json:"set_id"`
	Profile       int              `json:"approved_profile_revision"`
	Mapping       []VariantMapping `json:"mapping"`
	Items         []AnswerItem     `json:"items"`
}

type VariantMapping struct {
	ID string `json:"id"`
	A  string `json:"a"`
	B  string `json:"b"`
}

type AnswerItem struct {
	ID            string         `json:"id"`
	Text          string         `json:"text"`
	Intent        string         `json:"intent"`
	CueApplicable bool           `json:"cue_applicable"`
	SessionID     string         `json:"session_id"`
	TurnID        string         `json:"turn_id"`
	A             AnswerArtifact `json:"a"`
	B             AnswerArtifact `json:"b"`
}

type AnswerArtifact struct {
	Filename        string `json:"filename"`
	ProfileRevision int    `json:"profile_revision"`
	Seed            uint64 `json:"seed"`
	SHA256          string `json:"sha256"`
	SynthesisMS     uint64 `json:"synthesis_ms"`
}

func BuildPack(setID string, approvedProfileRevision int, rendered []RenderedCase) (Manifest, Answer, error) {
	if !opaqueIDPattern.MatchString(setID) {
		return Manifest{}, Answer{}, fmt.Errorf("speech evaluation set_id must be opaque lowercase hex")
	}
	if approvedProfileRevision <= 0 {
		return Manifest{}, Answer{}, fmt.Errorf("approved Mio profile revision must be positive")
	}
	if len(rendered) != CaseCount {
		return Manifest{}, Answer{}, fmt.Errorf("speech evaluation pack requires exactly %d rendered cases", CaseCount)
	}
	if err := ValidateCases(AllCases()); err != nil {
		return Manifest{}, Answer{}, fmt.Errorf("validate fixed speech evaluation cases: %w", err)
	}

	fixedCases := AllCases()
	manifest := Manifest{SchemaVersion: publicSchemaVersion, SetID: setID, Items: make([]ManifestItem, 0, CaseCount)}
	answer := Answer{
		SchemaVersion: answerSchemaVersion,
		SetID:         setID,
		Profile:       approvedProfileRevision,
		Mapping:       make([]VariantMapping, 0, CaseCount),
		Items:         make([]AnswerItem, 0, CaseCount),
	}
	for i, item := range rendered {
		want := fixedCases[i]
		if item.Case.ID != want.ID || item.Case.Text != want.Text || item.Case.Intent != want.Intent || item.Case.CueApplicable != want.CueApplicable {
			return Manifest{}, Answer{}, fmt.Errorf("rendered case[%d] does not match fixed case %s", i, want.ID)
		}
		if item.SessionID == "" || item.TurnID == "" {
			return Manifest{}, Answer{}, fmt.Errorf("rendered case %s requires session_id and turn_id", want.ID)
		}
		if err := validateArtifact(want.ID, "A", item.A, approvedProfileRevision); err != nil {
			return Manifest{}, Answer{}, err
		}
		if err := validateArtifact(want.ID, "B", item.B, approvedProfileRevision); err != nil {
			return Manifest{}, Answer{}, err
		}
		if item.A.Filename == item.B.Filename {
			return Manifest{}, Answer{}, fmt.Errorf("case %s A/B audio filenames must be distinct", want.ID)
		}
		if item.A.Seed != item.B.Seed {
			return Manifest{}, Answer{}, fmt.Errorf("case %s A/B Gateway seeds do not match", want.ID)
		}
		if !validVariantPair(item.AVariant, item.BVariant) {
			return Manifest{}, Answer{}, fmt.Errorf("case %s A/B mapping must contain one neutral and one expressive variant", want.ID)
		}
		manifest.Items = append(manifest.Items, ManifestItem{
			ID:            want.ID,
			Text:          want.Text,
			Intent:        want.Intent,
			CueApplicable: want.CueApplicable,
			CueHint:       cueHint(want.Plan),
			AURL:          viewerAudioURL(setID, item.A.Filename),
			BURL:          viewerAudioURL(setID, item.B.Filename),
		})
		answer.Items = append(answer.Items, AnswerItem{
			ID:            want.ID,
			Text:          want.Text,
			Intent:        want.Intent,
			CueApplicable: want.CueApplicable,
			SessionID:     item.SessionID,
			TurnID:        item.TurnID,
			A:             answerArtifact(item.A),
			B:             answerArtifact(item.B),
		})
		answer.Mapping = append(answer.Mapping, VariantMapping{ID: want.ID, A: item.AVariant, B: item.BVariant})
	}
	return manifest, answer, nil
}

func validVariantPair(a, b string) bool {
	return (a == "neutral" && b == "expressive") || (a == "expressive" && b == "neutral")
}

func validateArtifact(caseID, side string, artifact AudioArtifact, approvedProfileRevision int) error {
	if !opaqueWAVPattern.MatchString(artifact.Filename) {
		return fmt.Errorf("case %s %s filename must be an opaque WAV basename", caseID, side)
	}
	if artifact.ProfileRevision != approvedProfileRevision {
		return fmt.Errorf("case %s %s profile revision does not match approved Mio revision", caseID, side)
	}
	if !sha256HexPattern.MatchString(artifact.SHA256) {
		return fmt.Errorf("case %s %s SHA-256 must be lowercase hex", caseID, side)
	}
	return nil
}

func viewerAudioURL(setID, filename string) string {
	return "/viewer/tts/audio?path=speech-eval/" + setID + "/" + filename
}

func answerArtifact(artifact AudioArtifact) AnswerArtifact {
	return AnswerArtifact{
		Filename:        artifact.Filename,
		ProfileRevision: artifact.ProfileRevision,
		Seed:            artifact.Seed,
		SHA256:          artifact.SHA256,
		SynthesisMS:     artifact.SynthesisMS,
	}
}
