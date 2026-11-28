package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domainattachment "github.com/Nyukimin/RenCrow_CORE/internal/domain/attachment"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type musicAudioImportGatewayStub struct {
	MusicAudioImportGateway
	actorID     string
	calls       int
	project     string
	source      string
	hash        string
	data        []byte
	key         string
	statusCalls int
	statusID    string
}

type musicAudioImportPlaybackStub struct {
	MusicArtifactPlaybackGateway
	actorID string
}

func (s musicAudioImportPlaybackStub) ActorID() string { return s.actorID }

func (s *musicAudioImportGatewayStub) ActorID() string { return s.actorID }
func (s *musicAudioImportGatewayStub) ImportMusicAudio(_ context.Context, projectID, sourceID, hash string, audio []byte, key string) (MusicAudioImportJobStatus, error) {
	s.calls++
	s.project, s.source, s.hash, s.key = projectID, sourceID, hash, key
	s.data = append([]byte(nil), audio...)
	return MusicAudioImportJobStatus{
		JobID: "job_import", Kind: "audio_import", ProjectID: projectID, State: "succeeded", ArtifactID: "art_imported",
		InputSHA256: hash, SourceModule: "core_attachment", SourceID: sourceID, SizeBytes: int64(len(audio)),
		MediaType: "audio/flac", Format: "flac", DurationFrames: 44100 * 2, DurationActualS: 2,
		SampleRate: 44100, Channels: 2, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}, nil
}
func (s *musicAudioImportGatewayStub) GetMusicAudioImportJob(_ context.Context, jobID string) (MusicAudioImportJobStatus, error) {
	s.statusCalls++
	s.statusID = jobID
	return MusicAudioImportJobStatus{
		JobID: jobID, Kind: "audio_import", ProjectID: "project_opaque", State: "succeeded", ArtifactID: "art_imported",
		InputSHA256: strings.Repeat("a", 64), SourceModule: "core_attachment", SourceID: "att_current", SizeBytes: 36,
		MediaType: "audio/flac", Format: "flac", DurationFrames: 44100 * 2, DurationActualS: 2,
		SampleRate: 44100, Channels: 2, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}, nil
}

func TestMusicAudioImportUsesOnlyCurrentTrustedAttachmentAndStableActionKey(t *testing.T) {
	audio := []byte("fLaC" + string(make([]byte, 32)))
	hash := sha256.Sum256(audio)
	attachmentID := "att_current"
	workspace := t.TempDir()
	path := trustedMusicAttachmentPath(t, workspace, attachmentID, "source.flac", audio)
	attachment := domainattachment.Attachment{
		ID: attachmentID, Kind: domainattachment.KindAudio, Filename: "source.flac", ContentType: "application/octet-stream",
		SizeBytes: int64(len(audio)), Path: path, SHA256: hex.EncodeToString(hash[:]), Data: audio,
	}
	ctx := musicAudioImportExecutionContext(t, "http-request-current", "task-trace-current", "ren")
	ctx = domainattachment.WithTrustedRequestAttachments(ctx, []domainattachment.Attachment{attachment})
	projects := &musicProjectGatewayStub{actorID: "ren"}
	imports := &musicAudioImportGatewayStub{actorID: "ren"}
	runner := NewToolRunner(ToolRunnerConfig{WorkspaceDir: workspace, MusicProjects: projects, MusicAudioImport: imports, MusicAudioImportReady: true, DisableToolHarness: true})
	response, err := runner.ExecuteV2(ctx, "music.audio.import", map[string]any{"project_id": "project_opaque", "attachment_id": attachmentID})
	if err != nil || response == nil || response.Error != nil {
		t.Fatalf("music.audio.import = (%#v, %v), want success", response, err)
	}
	if imports.calls != 1 || imports.project != "project_opaque" || imports.source != attachmentID || imports.hash != hex.EncodeToString(hash[:]) || string(imports.data) != string(audio) {
		t.Fatalf("Music import input calls=%d project=%q source=%q hash=%q dataMatches=%t", imports.calls, imports.project, imports.source, imports.hash, string(imports.data) == string(audio))
	}
	if imports.key == "" || imports.key == "http-request-current" || len(imports.key) > 200 {
		t.Fatalf("Music import key is not a bounded trusted action key: %q", imports.key)
	}
	if projects.got != 1 || projects.projectID != "project_opaque" {
		t.Fatalf("project owner verification calls=%d project=%q", projects.got, projects.projectID)
	}
}

func TestMusicAudioImportRejectsMissingCurrentAttachmentAndTamperedHash(t *testing.T) {
	audio := []byte("fLaC" + string(make([]byte, 32)))
	hash := sha256.Sum256(audio)
	workspace := t.TempDir()
	path := trustedMusicAttachmentPath(t, workspace, "att_current", "source.flac", audio)
	baseAttachment := domainattachment.Attachment{
		ID: "att_current", Kind: domainattachment.KindAudio, Filename: "source.flac", ContentType: "audio/flac",
		SizeBytes: int64(len(audio)), Path: path, SHA256: hex.EncodeToString(hash[:]), Data: audio,
	}
	for _, test := range []struct {
		name       string
		attachment *domainattachment.Attachment
		wantCode   tool.ErrorCode
	}{
		{name: "attachment from another request", wantCode: tool.ErrNotFound},
		{name: "tampered trusted digest", attachment: func() *domainattachment.Attachment {
			bad := baseAttachment
			bad.SHA256 = string(make([]byte, 64))
			return &bad
		}(), wantCode: tool.ErrValidationFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			imports := &musicAudioImportGatewayStub{actorID: "ren"}
			projects := &musicProjectGatewayStub{actorID: "ren"}
			runner := NewToolRunner(ToolRunnerConfig{WorkspaceDir: workspace, MusicProjects: projects, MusicAudioImport: imports, MusicAudioImportReady: true, DisableToolHarness: true})
			ctx := musicAudioImportExecutionContext(t, "http-request-other", "task-trace-other", "ren")
			if test.attachment != nil {
				ctx = domainattachment.WithTrustedRequestAttachments(ctx, []domainattachment.Attachment{*test.attachment})
			} else {
				ctx = domainattachment.WithTrustedRequestAttachments(ctx, []domainattachment.Attachment{{ID: "att_other", Kind: domainattachment.KindAudio, Data: audio}})
			}
			response, err := runner.ExecuteV2(ctx, "music.audio.import", map[string]any{"project_id": "project_opaque", "attachment_id": "att_current"})
			if err != nil || response == nil || response.Error == nil || response.Error.Code != test.wantCode {
				t.Fatalf("music.audio.import = (%#v, %v), want error %s", response, err, test.wantCode)
			}
			if imports.calls != 0 {
				t.Fatalf("invalid attachment reached Music %d times", imports.calls)
			}
		})
	}
}

func TestReadTrustedMusicAttachmentRejectsOutsidePathAndSymlink(t *testing.T) {
	workspace := t.TempDir()
	audio := []byte("fLaC" + string(make([]byte, 32)))
	outside := filepath.Join(t.TempDir(), "outside.flac")
	if err := os.WriteFile(outside, audio, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readTrustedMusicAttachment(workspace, domainattachment.Attachment{
		ID: "att_current", Filename: "outside.flac", Path: outside, SizeBytes: int64(len(audio)),
	}); err == nil {
		t.Fatal("outside workspace upload path was accepted")
	}
	symlinkPath := filepath.Join(workspace, "viewer_uploads", "20261128", "viewer", "att_current", "source.flac")
	if err := os.MkdirAll(filepath.Dir(symlinkPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, symlinkPath); err != nil {
		t.Fatal(err)
	}
	if _, err := readTrustedMusicAttachment(workspace, domainattachment.Attachment{
		ID: "att_current", Filename: "source.flac", Path: symlinkPath, SizeBytes: int64(len(audio)),
	}); err == nil {
		t.Fatal("symlink attachment path was accepted")
	}
}

func TestMusicAudioImportStatusRemainsRegisteredWhenNewImportCapabilityIsUnavailable(t *testing.T) {
	imports := &musicAudioImportGatewayStub{actorID: "ren"}
	runner := NewToolRunner(ToolRunnerConfig{
		MusicAudioImport: imports, MusicAudioImportReady: false, MusicPlaybackLinkReady: true,
		MusicArtifactPlayback: musicAudioImportPlaybackStub{actorID: "ren"}, DisableToolHarness: true,
	})
	metadata, err := runner.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	var statusRegistered, importRegistered bool
	for _, item := range metadata {
		statusRegistered = statusRegistered || item.ToolID == "music.audio.import.status"
		importRegistered = importRegistered || item.ToolID == "music.audio.import"
	}
	if !statusRegistered || importRegistered {
		t.Fatalf("import readiness registration status=%v import=%v", statusRegistered, importRegistered)
	}
	ctx := musicAudioImportExecutionContext(t, "http-request-status", "task-trace-status", "ren")
	response, err := runner.ExecuteV2(ctx, "music.audio.import.status", map[string]any{"job_id": "job_import"})
	if err != nil || response == nil || response.Error != nil {
		t.Fatalf("music.audio.import.status = (%#v, %v)", response, err)
	}
	result, ok := response.Result.(MusicAudioImportResult)
	if !ok || result.JobID != "job_import" || result.PlaybackURL != "/viewer/music/artifacts/art_imported/download?to=mio" || imports.statusCalls != 1 || imports.statusID != "job_import" {
		t.Fatalf("import recovery result=%#v statusCalls=%d statusID=%q", response.Result, imports.statusCalls, imports.statusID)
	}
}

func trustedMusicAttachmentPath(t *testing.T, workspace, attachmentID, filename string, audio []byte) string {
	t.Helper()
	path := filepath.Join(workspace, "viewer_uploads", "20261128", "viewer", attachmentID, filename)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, audio, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func musicAudioImportExecutionContext(t *testing.T, requestID, taskTraceID, userID string) context.Context {
	t.Helper()
	ctx := delegatedMusicUserContext(t, requestID, userID, []string{tool.DataScopePublic, tool.DataScopeUser})
	var err error
	ctx, err = domainexecution.WithIdentity(ctx, modulecore.NewTaskID(), modulecore.NewRunID(), modulecore.NewTraceID())
	if err != nil {
		t.Fatal(err)
	}
	ctx = tool.WithToolExecutionScope(ctx, func() tool.ToolExecutionScope {
		scope, _ := tool.ToolExecutionScopeFromContext(ctx)
		scope.RequestID = taskTraceID
		return scope
	}())
	ctx, err = domainexecution.WithBoundActionAttempt(ctx, modulecore.NewActionID(), modulecore.NewAttemptID())
	if err != nil {
		t.Fatal(err)
	}
	return ctx
}
