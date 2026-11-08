package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

type musicControlGatewayStub struct {
	actorID     string
	gets        int
	updates     int
	creates     int
	requestGets int
	projectID   string
	requestID   string
	updateInput MusicControlUpdate
	createInput MusicControlRequestInput
	updateKey   string
	createKey   string
}

func (s *musicControlGatewayStub) ActorID() string { return s.actorID }
func (s *musicControlGatewayStub) GetMusicControl(_ context.Context, projectID string) (MusicControlState, error) {
	s.gets++
	s.projectID = projectID
	return musicControlState(projectID), nil
}
func (s *musicControlGatewayStub) UpdateMusicControl(_ context.Context, projectID string, input MusicControlUpdate, key string) (MusicControlState, error) {
	s.updates++
	s.projectID = projectID
	s.updateInput = input
	s.updateKey = key
	return musicControlState(projectID), nil
}
func (s *musicControlGatewayStub) CreateMusicControlRequest(_ context.Context, projectID string, input MusicControlRequestInput, key string) (MusicControlRequest, error) {
	s.creates++
	s.projectID = projectID
	s.createInput = input
	s.createKey = key
	return MusicControlRequest{
		MusicControlRequestInput: input,
		RequestID:                "req_music_01", ProjectID: projectID, RequestRevision: 1,
		ControlRevision: 3, Mode: "DIRECT", ModeSource: "project",
		EffectiveLocks: []MusicLock{}, EffectiveHardConstraints: []MusicConstraint{}, CreatedAt: time.Now().UTC(),
	}, nil
}
func (s *musicControlGatewayStub) GetMusicControlRequest(_ context.Context, projectID, requestID string) (MusicControlRequest, error) {
	s.requestGets++
	s.projectID = projectID
	s.requestID = requestID
	return MusicControlRequest{
		MusicControlRequestInput: MusicControlRequestInput{RawText: "original"},
		RequestID:                requestID, ProjectID: projectID, RequestRevision: 1,
		ControlRevision: 3, Mode: "DIRECT", ModeSource: "project",
		EffectiveLocks: []MusicLock{}, EffectiveHardConstraints: []MusicConstraint{}, CreatedAt: time.Now().UTC(),
	}, nil
}

func musicControlState(projectID string) MusicControlState {
	return MusicControlState{
		ProjectID: projectID, Revision: 3, Mode: "DIRECT", ModeSource: "project",
		Locks: []MusicLock{}, HardConstraints: []MusicConstraint{}, UpdatedAt: time.Now().UTC(),
	}
}

func TestMusicControlToolsRegisterOnlyForObservedGateway(t *testing.T) {
	without := NewToolRunner(ToolRunnerConfig{DisableToolHarness: true})
	metadata, err := without.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, toolID := range []string{"music.control.get", "music.control.update", "music.request.create", "music.request.get"} {
		if hasRunnerTool(metadata, toolID) {
			t.Errorf("Music production-control Tool %q registered without its capability gateway", toolID)
		}
	}

	control := &musicControlGatewayStub{actorID: "ren"}
	worker := NewToolRunner(ToolRunnerConfig{MusicControl: control, DisableToolHarness: true})
	metadata, err = worker.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, toolID := range []string{"music.control.get", "music.control.update", "music.request.create", "music.request.get"} {
		if !hasRunnerTool(metadata, toolID) {
			t.Errorf("observed production_control is missing Worker Tool %q", toolID)
		}
	}

	propertiesFor := func(toolID string) map[string]any {
		t.Helper()
		for _, item := range metadata {
			if item.ToolID == toolID {
				properties, ok := item.Parameters["properties"].(map[string]any)
				if !ok {
					t.Fatalf("Tool %s has no typed properties", toolID)
				}
				return properties
			}
		}
		t.Fatalf("Tool %s is not registered", toolID)
		return nil
	}
	if _, exposed := propertiesFor("music.control.update")["actor_id"]; exposed {
		t.Fatal("Music control Tool exposes actor_id")
	}
	if _, exposed := propertiesFor("music.request.create")["raw_text"]; exposed {
		t.Fatal("Music request Tool accepts model-authored raw_text")
	}
}

func TestMusicControlUpdateRequiresDelegatedProofAndUsesRevisionCAS(t *testing.T) {
	control := &musicControlGatewayStub{actorID: "ren"}
	runner := NewToolRunner(ToolRunnerConfig{MusicControl: control, DisableToolHarness: true})
	args := map[string]any{"project_id": "prj_track_01", "expected_revision": 3, "mode": "DIRECT"}
	denied, err := runner.ExecuteV2(context.Background(), "music.control.update", args)
	if err != nil || denied == nil || denied.Error == nil || denied.Error.Code != tool.ErrToolExecutionScopeMissing {
		t.Fatalf("update without trusted scope = (%#v, %v), want missing-scope denial", denied, err)
	}
	if control.updates != 0 {
		t.Fatal("untrusted control update reached Music")
	}

	ctx := delegatedMusicUserContext(t, "music-control-write-1", "ren", []string{tool.DataScopePublic, tool.DataScopeUser})
	response, err := runner.ExecuteV2(ctx, "music.control.update", args)
	if err != nil || response == nil || response.Error != nil || control.updates != 1 {
		t.Fatalf("authorized control update = (%#v, %v), updates=%d", response, err, control.updates)
	}
	if control.projectID != "prj_track_01" || control.updateInput.ExpectedRevision != 3 || control.updateInput.Mode == nil || *control.updateInput.Mode != "DIRECT" {
		t.Fatalf("control CAS payload = %+v project=%q", control.updateInput, control.projectID)
	}
	if !strings.HasPrefix(control.updateKey, "rencrow-core-music-control-") || len(control.updateKey) > 200 {
		t.Fatalf("control idempotency key = %q", control.updateKey)
	}
}

func TestMusicControlModeSwitchCannotSilentlyRemoveLocks(t *testing.T) {
	control := &musicControlGatewayStub{actorID: "ren"}
	runner := NewToolRunner(ToolRunnerConfig{MusicControl: control, DisableToolHarness: true})
	ctx := delegatedMusicUserContext(t, "music-control-remove-1", "ren", []string{tool.DataScopePublic, tool.DataScopeUser})
	ctx = tool.WithOriginalUserMessage(ctx, "Music project prj_track_01をAUTOにしてください")
	args := map[string]any{
		"project_id": "prj_track_01", "expected_revision": 3, "mode": "AUTO", "remove_lock_ids": []any{"lock_01"},
	}
	denied, err := runner.ExecuteV2(ctx, "music.control.update", args)
	if err != nil || denied == nil || denied.Error == nil || denied.Error.Code != tool.ErrPermissionDenied || control.updates != 0 {
		t.Fatalf("AUTO switch with implicit lock removal = (%#v, %v), updates=%d", denied, err, control.updates)
	}

	ctx = tool.WithOriginalUserMessage(ctx, "Music project prj_track_01をAUTOにして、lock_01を解除してください")
	allowed, err := runner.ExecuteV2(ctx, "music.control.update", args)
	if err != nil || allowed == nil || allowed.Error != nil || control.updates != 1 {
		t.Fatalf("explicit named lock removal = (%#v, %v), updates=%d", allowed, err, control.updates)
	}
}

func TestMusicRequestCreateUsesTrustedRawTextAndProofIdempotency(t *testing.T) {
	control := &musicControlGatewayStub{actorID: "ren"}
	runner := NewToolRunner(ToolRunnerConfig{MusicControl: control, DisableToolHarness: true})
	args := map[string]any{
		"project_id": "prj_track_01", "operation": "generation", "layers": []any{"L1", "L2"},
		"explicit_mode": "DIRECT", "soft_preferences": []any{map[string]any{"property": "tempo", "value": "about 110 BPM"}},
	}
	ctx := delegatedMusicUserContext(t, "music-request-create-1", "ren", []string{tool.DataScopePublic, tool.DataScopeUser})
	withoutSource, err := runner.ExecuteV2(ctx, "music.request.create", args)
	if err != nil || withoutSource == nil || withoutSource.Error == nil || withoutSource.Error.Code != tool.ErrToolExecutionScopeMissing || control.creates != 0 {
		t.Fatalf("request create without trusted original text = (%#v, %v), creates=%d", withoutSource, err, control.creates)
	}

	const original = "  Music project prj_track_01 でBPMは約110、歌とドラムを含む曲を作って  "
	ctx = tool.WithOriginalUserMessage(ctx, original)
	response, err := runner.ExecuteV2(ctx, "music.request.create", args)
	if err != nil || response == nil || response.Error != nil || control.creates != 1 {
		t.Fatalf("request create with trusted original text = (%#v, %v), creates=%d", response, err, control.creates)
	}
	if control.createInput.RawText != original || control.createInput.Operation != "generation" || len(control.createInput.Layers) != 2 || control.createInput.Layers[0] != "L1" || control.createInput.Layers[1] != "L2" || control.createInput.ExplicitMode == nil || *control.createInput.ExplicitMode != "DIRECT" {
		t.Fatalf("Music request input did not preserve source/normalization: %+v", control.createInput)
	}
	if len(control.createInput.SoftPreferences) != 1 || control.createInput.SoftPreferences[0].Property != "tempo" || control.createInput.SoftPreferences[0].Value != "BPMは約110" {
		t.Fatalf("approximate BPM was not preserved as a soft preference: %+v", control.createInput.SoftPreferences)
	}
	if !strings.HasPrefix(control.createKey, "rencrow-core-music-request-") || len(control.createKey) > 200 {
		t.Fatalf("request idempotency key = %q", control.createKey)
	}
	result, ok := response.Result.(MusicControlRequest)
	if !ok || result.RequestID != "req_music_01" || result.ProjectID != "prj_track_01" || result.ModeSource != "project" {
		t.Fatalf("request response = %#v, want normalized Music request receipt", response.Result)
	}

	forged := map[string]any{"project_id": "prj_track_01", "raw_text": "model echo", "operation": "generation", "layers": []any{}}
	bad, err := runner.ExecuteV2(ctx, "music.request.create", forged)
	if err != nil || bad == nil || bad.Error == nil || bad.Error.Code != tool.ErrValidationFailed || control.creates != 1 {
		t.Fatalf("model-authored raw_text = (%#v, %v), creates=%d", bad, err, control.creates)
	}
}

func TestMusicRequestCreateUsesExplicitLeadingModeAndRejectsUnverifiableExactBPM(t *testing.T) {
	ctx := delegatedMusicUserContext(t, "music-request-mode-1", "ren", []string{tool.DataScopePublic, tool.DataScopeUser})
	args := map[string]any{
		"project_id": "prj_track_01", "operation": "generation", "layers": []any{"L2"}, "explicit_mode": "DIRECT",
	}
	control := &musicControlGatewayStub{actorID: "ren"}
	runner := NewToolRunner(ToolRunnerConfig{MusicControl: control, DisableToolHarness: true})
	ctx = tool.WithOriginalUserMessage(ctx, "AUTOで曲を生成してください")
	response, err := runner.ExecuteV2(ctx, "music.request.create", args)
	if err != nil || response == nil || response.Error != nil || control.createInput.ExplicitMode == nil || *control.createInput.ExplicitMode != "AUTO" {
		t.Fatalf("trusted leading AUTO directive = (%#v, %v), request=%+v", response, err, control.createInput)
	}

	for _, rawText := range []string{
		"BPM120の曲を作って",
		"120 BPMの曲を作って",
		"曲のBPMは120に固定して",
	} {
		control := &musicControlGatewayStub{actorID: "ren"}
		runner := NewToolRunner(ToolRunnerConfig{MusicControl: control, DisableToolHarness: true})
		bpmCtx := tool.WithOriginalUserMessage(ctx, rawText)
		failed, err := runner.ExecuteV2(bpmCtx, "music.request.create", args)
		if err != nil || failed == nil || failed.Error == nil || failed.Error.Code != tool.ErrValidationFailed || control.creates != 0 {
			t.Errorf("unverifiable exact BPM %q = (%#v, %v), creates=%d", rawText, failed, err, control.creates)
		}
	}
	for _, rawText := range []string{"約120 BPM", "about 120 BPM", "110–120 BPM"} {
		if hasUnqualifiedExactMusicBPM(rawText) {
			t.Errorf("approximate or ranged BPM %q was classified as exact", rawText)
		}
	}
}

func TestMusicRequestCreateRejectsFreeformOrDuplicateLayers(t *testing.T) {
	control := &musicControlGatewayStub{actorID: "ren"}
	runner := NewToolRunner(ToolRunnerConfig{MusicControl: control, DisableToolHarness: true})
	ctx := delegatedMusicUserContext(t, "music-request-layers-1", "ren", []string{tool.DataScopePublic, tool.DataScopeUser})
	ctx = tool.WithOriginalUserMessage(ctx, "曲を作って")
	for _, layers := range [][]any{{"master"}, {"L1", "L1"}, {}} {
		args := map[string]any{"project_id": "prj_track_01", "operation": "generation", "layers": layers}
		response, err := runner.ExecuteV2(ctx, "music.request.create", args)
		if err != nil || response == nil || response.Error == nil || response.Error.Code != tool.ErrValidationFailed || control.creates != 0 {
			t.Errorf("invalid Music layers %v = (%#v, %v), creates=%d", layers, response, err, control.creates)
		}
	}
}
