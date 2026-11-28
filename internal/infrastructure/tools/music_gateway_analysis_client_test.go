package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

const musicAnalysisClientToken = "music-analysis-client-token-0123456789abcdef"

type musicRoundTripFunc func(*http.Request) (*http.Response, error)

func (f musicRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestMusicAnalysisResultHashSurvivesOwnerJSONRoundTripAtNativeRates(t *testing.T) {
	for _, sampleRate := range []int{48000, 44100} {
		t.Run(strconv.Itoa(sampleRate), func(t *testing.T) {
			analysis := successfulMusicAnalysis(sampleRate)
			if sampleRate == 44100 {
				analysis.Operation = "captions"
				var document MusicAnalysisDocument
				if err := json.Unmarshal(analysis.Result, &document); err != nil {
					t.Fatal(err)
				}
				document.Operation = "captions"
				document.BPM = nil
				document.Beats = []MusicAnalysisBeat{}
				document.Words = []MusicAnalysisWord{{Text: "A <tag> & 'quoted'", StartFrame: int64(sampleRate), EndFrame: int64(sampleRate) * 2}}
				document.Versions.WhisperX = "3.8.6"
				analysis.Result, _ = json.Marshal(document)
				hash := sha256.Sum256(analysis.Result)
				analysis.ResultSHA256 = hex.EncodeToString(hash[:])
			}
			responseBytes, err := json.Marshal(analysis)
			if err != nil {
				t.Fatal(err)
			}
			var decoded MusicAnalysis
			if err := json.Unmarshal(responseBytes, &decoded); err != nil {
				t.Fatalf("owner Analysis DTO decode failed: %v", err)
			}
			if err := validateMusicAnalysis(decoded); err != nil {
				t.Fatalf("validate owner Analysis DTO: %v", err)
			}
			var document MusicAnalysisDocument
			if err := json.Unmarshal(decoded.Result, &document); err != nil {
				t.Fatal(err)
			}
			if document.SampleRate != sampleRate || document.DurationFrames != int64(sampleRate)*8 {
				t.Fatalf("analysis source timeline = %d Hz / %d frames", document.SampleRate, document.DurationFrames)
			}
		})
	}
}

func TestMusicAnalysisClientAcceptsScopedLargeResponseAndSetsOperationDeadline(t *testing.T) {
	analysis := successfulMusicAnalysis(48000)
	document := MusicAnalysisDocument{
		SchemaVersion: "rencrow.music-analysis.v1", Operation: "beats", SourceSHA256: analysis.SourceSHA256,
		SampleRate: 48000, DurationFrames: 48000 * 180, BPM: ptrFloat64(120), Beats: make([]MusicAnalysisBeat, 4000), Words: []MusicAnalysisWord{},
		Versions: MusicAnalysisVersions{Worker: "rencrow.music-analysis-python.v1", Python: "3.12.0", Librosa: "0.11.0"},
	}
	for i := range document.Beats {
		document.Beats[i] = MusicAnalysisBeat{Frame: int64(i * 1000), TimeSeconds: float64(i) / 25}
	}
	result, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) <= maxMusicGatewayResponse || len(result) > maxMusicAnalysisResult {
		t.Fatalf("analysis result fixture size=%d, want >128 KiB and <=2 MiB", len(result))
	}
	resultHash := sha256.Sum256(result)
	analysis.Result = result
	analysis.ResultSHA256 = hex.EncodeToString(resultHash[:])
	responseBody, err := json.Marshal(analysis)
	if err != nil {
		t.Fatal(err)
	}
	var observedDeadline time.Time
	var observedHeader http.Header
	var observedPath string
	client, err := NewMusicProjectGatewayClient(MusicGatewayClientConfig{
		Endpoint: "http://127.0.0.1:18796", ActorID: "ren", BearerToken: musicAnalysisClientToken,
		HTTPClient: &http.Client{Transport: musicRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			observedPath = request.URL.Path
			observedHeader = request.Header.Clone()
			observedDeadline, _ = request.Context().Deadline()
			return &http.Response{StatusCode: http.StatusCreated, Header: http.Header{"Content-Type": []string{"application/json"}}, ContentLength: int64(len(responseBody)), Body: io.NopCloser(bytes.NewReader(responseBody)), Request: request}, nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := client.CreateMusicAnalysis(context.Background(), "project_opaque", MusicAnalysisRequest{SourceArtifactID: "art_opaque", Operation: "beats"}, "analysis-key")
	if err != nil {
		t.Fatalf("CreateMusicAnalysis() error = %v", err)
	}
	if created.State != "succeeded" || observedPath != "/v1/music/projects/project_opaque/analyses" {
		t.Fatalf("analysis result state=%q request path=%q", created.State, observedPath)
	}
	if observedHeader.Get("Authorization") != "Bearer "+musicAnalysisClientToken || observedHeader.Get("X-RenCrow-Music-Expected-Actor") != "ren" || observedHeader.Get("Idempotency-Key") != "analysis-key" {
		t.Fatalf("Music analysis request headers = %#v", observedHeader)
	}
	if remaining := time.Until(observedDeadline); remaining < 4*time.Minute || remaining > 4*time.Minute+10*time.Second {
		t.Fatalf("analysis request deadline remaining=%s; want operation-scoped 4m10s", remaining)
	}
}

func TestMusicAnalysisClientPreservesDurableOwnerFailureProjection(t *testing.T) {
	now := time.Now().UTC()
	failed := MusicAnalysis{
		AnalysisID: "analysis_opaque", ProjectID: "project_opaque", SourceArtifactID: "art_opaque", SourceSHA256: strings.Repeat("b", 64),
		Operation: "beats", State: "failed", FailureCode: "WORKER_FAILED", CreatedAt: now, UpdatedAt: now,
	}
	body, err := json.Marshal(failed)
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewMusicProjectGatewayClient(MusicGatewayClientConfig{
		Endpoint: "http://127.0.0.1:18796", ActorID: "ren", BearerToken: musicAnalysisClientToken,
		HTTPClient: &http.Client{Transport: musicRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusBadGateway, Header: http.Header{"Content-Type": []string{"application/json"}}, ContentLength: int64(len(body)), Body: io.NopCloser(bytes.NewReader(body)), Request: request}, nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.CreateMusicAnalysis(context.Background(), "project_opaque", MusicAnalysisRequest{SourceArtifactID: "art_opaque", Operation: "beats"}, "analysis-retry-key")
	if err != nil {
		t.Fatalf("CreateMusicAnalysis() discarded durable 502 projection: %v", err)
	}
	if got.AnalysisID != failed.AnalysisID || got.State != "failed" || got.FailureCode != failed.FailureCode || len(got.Result) != 0 {
		t.Fatalf("durable analysis failure projection = %+v", got)
	}
}

func TestMusicAnalysisClientAcceptsOnlyDocumentedDurableReplayStates(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		state      string
		failure    string
		wantError  bool
	}{
		{name: "created success", statusCode: http.StatusCreated, state: "succeeded"},
		{name: "success replay", statusCode: http.StatusOK, state: "succeeded"},
		{name: "first worker failure", statusCode: http.StatusBadGateway, state: "failed", failure: "WORKER_FAILED"},
		{name: "failed replay", statusCode: http.StatusOK, state: "failed", failure: "WORKER_FAILED"},
		{name: "reconciliation replay", statusCode: http.StatusOK, state: "needs_reconciliation", failure: "WORKER_INTERRUPTED"},
		{name: "in progress replay", statusCode: http.StatusOK, state: "running"},
		{name: "created response cannot be pending", statusCode: http.StatusCreated, state: "running", wantError: true},
		{name: "gateway failure cannot be reconciliation", statusCode: http.StatusBadGateway, state: "needs_reconciliation", failure: "WORKER_INTERRUPTED", wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			analysis := successfulMusicAnalysis(48000)
			analysis.State = test.state
			if test.state != "succeeded" {
				analysis.Result = nil
				analysis.ResultSHA256 = ""
				analysis.FailureCode = test.failure
			}
			body, err := json.Marshal(analysis)
			if err != nil {
				t.Fatal(err)
			}
			client, err := NewMusicProjectGatewayClient(MusicGatewayClientConfig{
				Endpoint: "http://127.0.0.1:18796", ActorID: "ren", BearerToken: musicAnalysisClientToken,
				HTTPClient: &http.Client{Transport: musicRoundTripFunc(func(request *http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode: test.statusCode, Header: http.Header{"Content-Type": []string{"application/json"}},
						ContentLength: int64(len(body)), Body: io.NopCloser(bytes.NewReader(body)), Request: request,
					}, nil
				})},
			})
			if err != nil {
				t.Fatal(err)
			}
			got, err := client.CreateMusicAnalysis(context.Background(), "project_opaque", MusicAnalysisRequest{SourceArtifactID: "art_opaque", Operation: "beats"}, "analysis-stable-key")
			if test.wantError {
				if err == nil {
					t.Fatalf("CreateMusicAnalysis() accepted HTTP %d with state %q", test.statusCode, test.state)
				}
				return
			}
			if err != nil {
				t.Fatalf("CreateMusicAnalysis() rejected HTTP %d state %q: %v", test.statusCode, test.state, err)
			}
			if got.AnalysisID != analysis.AnalysisID || got.State != analysis.State || got.FailureCode != analysis.FailureCode || string(got.Result) != string(analysis.Result) {
				t.Fatalf("CreateMusicAnalysis() lost durable projection: got=%+v want=%+v", got, analysis)
			}
		})
	}
}

func TestMusicAudioImportClientSendsRawFLACAndSeparateImportDTO(t *testing.T) {
	audio := []byte("fLaC" + strings.Repeat("x", 32))
	hash := sha256.Sum256(audio)
	shaText := hex.EncodeToString(hash[:])
	status := MusicAudioImportJobStatus{
		JobID: "job_import", Kind: "audio_import", ProjectID: "project_opaque", State: "succeeded", ArtifactID: "art_imported",
		InputSHA256: shaText, SourceModule: "core_attachment", SourceID: "att_opaque", SizeBytes: int64(len(audio)),
		MediaType: "audio/flac", Format: "flac", DurationFrames: 88200, DurationActualS: 2, SampleRate: 44100, Channels: 2,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	body, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	var requestBody []byte
	var observedPath string
	var observedHeader http.Header
	var observedDeadline time.Time
	client, err := NewMusicProjectGatewayClient(MusicGatewayClientConfig{
		Endpoint: "http://127.0.0.1:18796", ActorID: "ren", BearerToken: musicAnalysisClientToken,
		HTTPClient: &http.Client{Transport: musicRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			observedPath = request.URL.Path
			observedHeader = request.Header.Clone()
			observedDeadline, _ = request.Context().Deadline()
			requestBody, _ = io.ReadAll(request.Body)
			return &http.Response{StatusCode: http.StatusAccepted, Header: http.Header{"Content-Type": []string{"application/json"}}, ContentLength: int64(len(body)), Body: io.NopCloser(bytes.NewReader(body)), Request: request}, nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.ImportMusicAudio(context.Background(), status.ProjectID, status.SourceID, shaText, audio, "audio-import-key")
	if err != nil {
		t.Fatalf("ImportMusicAudio() error = %v", err)
	}
	if got.Kind != "audio_import" || got.SampleRate != 44100 || observedPath != "/v1/music/imports" || !bytes.Equal(requestBody, audio) {
		t.Fatalf("Music import receipt=%#v path=%q bodyMatches=%t", got, observedPath, bytes.Equal(requestBody, audio))
	}
	if remaining := time.Until(observedDeadline); remaining < 4*time.Minute || remaining > 4*time.Minute+10*time.Second {
		t.Fatalf("import request deadline remaining=%s; want operation-scoped 4m10s", remaining)
	}
	for header, want := range map[string]string{
		"Authorization":                  "Bearer " + musicAnalysisClientToken,
		"Content-Type":                   "audio/flac",
		"X-RenCrow-Music-Expected-Actor": "ren",
		"X-RenCrow-Music-Project-ID":     status.ProjectID,
		"X-Source-Module":                "core_attachment",
		"X-Source-ID":                    status.SourceID,
		"X-Expected-SHA256":              shaText,
		"Idempotency-Key":                "audio-import-key",
	} {
		if observedHeader.Get(header) != want {
			t.Errorf("header %s=%q want %q", header, observedHeader.Get(header), want)
		}
	}
}

func TestImportedMusicArtifactAcceptsNativeMetadataWithoutWeakeningGenerationValidator(t *testing.T) {
	imported := MusicArtifact{
		ID: "art_imported", ProjectID: "project_opaque", Kind: "imported_audio", SHA256: strings.Repeat("a", 64),
		SizeBytes: 1 << 20, MediaType: "audio/flac", Format: "flac", DurationFrames: 44100 * 8, SampleRate: 44100, Channels: 2,
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := validateMusicArtifactMetadata(imported); err != nil {
		t.Fatalf("native imported artifact metadata rejected: %v", err)
	}
	if err := validateMusicArtifact(imported); err == nil {
		t.Fatal("generated/remix validator accepted a native-rate imported artifact")
	}
	generated := imported
	generated.Kind = "generated_audio"
	generated.MediaType = "audio/wav"
	generated.Format = "wav"
	generated.SampleRate = 48000
	generated.DurationFrames = 48000 * 8
	if err := validateMusicArtifactMetadata(generated); err != nil {
		t.Fatalf("generated 48 kHz stereo artifact rejected: %v", err)
	}
}

func TestImportedMusicArtifactMetadataAndRangePlaybackUseActorBoundRoutes(t *testing.T) {
	audio := []byte("fLaC" + strings.Repeat("x", 32))
	hash := sha256.Sum256(audio)
	shaText := hex.EncodeToString(hash[:])
	artifact := MusicArtifact{
		ID: "art_imported", ProjectID: "project_opaque", Kind: "imported_audio", SHA256: shaText,
		SizeBytes: int64(len(audio)), MediaType: "audio/flac", Format: "flac", DurationFrames: 44100 * 2,
		SampleRate: 44100, Channels: 2, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	metadataBody, err := json.Marshal(artifact)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	var expectedActors []string
	client, err := NewMusicProjectGatewayClient(MusicGatewayClientConfig{
		Endpoint: "http://127.0.0.1:18796", ActorID: "ren", BearerToken: musicAnalysisClientToken,
		HTTPClient: &http.Client{Transport: musicRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			paths = append(paths, request.URL.Path)
			expectedActors = append(expectedActors, request.Header.Get("X-RenCrow-Music-Expected-Actor"))
			if request.URL.Path == "/v1/music/artifacts/art_imported" {
				return musicTestJSONResponse(http.StatusOK, metadataBody, request), nil
			}
			return &http.Response{
				StatusCode:    http.StatusOK,
				Header:        http.Header{"Content-Type": []string{"audio/flac"}, "Etag": []string{`"sha256:` + shaText + `"`}},
				ContentLength: int64(len(audio)), Body: io.NopCloser(bytes.NewReader(audio)), Request: request,
			}, nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.GetMusicArtifact(context.Background(), artifact.ID)
	if err != nil {
		t.Fatalf("GetMusicArtifact(imported_audio) error = %v", err)
	}
	if got.ID != artifact.ID || got.SampleRate != 44100 || got.DurationFrames != 88200 || got.SHA256 != shaText {
		t.Fatalf("native Music artifact metadata = %+v", got)
	}
	response, err := client.OpenMusicArtifact(context.Background(), artifact.ID, "")
	if err != nil {
		t.Fatalf("OpenMusicArtifact(imported_audio) error = %v", err)
	}
	defer response.Body.Close()
	played, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != http.StatusOK || !bytes.Equal(played, audio) {
		t.Fatalf("imported audio playback status=%d bodyMatches=%v err=%v", response.StatusCode, bytes.Equal(played, audio), err)
	}
	if len(paths) != 2 || paths[0] != "/v1/music/artifacts/art_imported" || paths[1] != "/v1/music/artifacts/art_imported/download" ||
		expectedActors[0] != "ren" || expectedActors[1] != "ren" {
		t.Fatalf("Music artifact routes=%v expected actors=%v", paths, expectedActors)
	}
}

func musicTestJSONResponse(status int, body []byte, request *http.Request) *http.Response {
	return &http.Response{
		StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}},
		ContentLength: int64(len(body)), Body: io.NopCloser(bytes.NewReader(body)), Request: request,
	}
}

func successfulMusicAnalysis(sampleRate int) MusicAnalysis {
	sourceHash := strings.Repeat("b", 64)
	document := MusicAnalysisDocument{
		SchemaVersion: "rencrow.music-analysis.v1", Operation: "beats", SourceSHA256: sourceHash,
		SampleRate: sampleRate, DurationFrames: int64(sampleRate) * 8, BPM: ptrFloat64(120),
		Beats: []MusicAnalysisBeat{{Frame: int64(sampleRate), TimeSeconds: 1}}, Words: []MusicAnalysisWord{},
		Versions: MusicAnalysisVersions{Worker: "rencrow.music-analysis-python.v1", Python: "3.12.0", Librosa: "0.11.0"},
	}
	encoded, _ := json.Marshal(document)
	resultHash := sha256.Sum256(encoded)
	now := time.Now().UTC()
	return MusicAnalysis{
		AnalysisID: "analysis_opaque", ProjectID: "project_opaque", SourceArtifactID: "art_opaque", SourceSHA256: sourceHash,
		Operation: "beats", State: "succeeded", Result: encoded, ResultSHA256: hex.EncodeToString(resultHash[:]), CreatedAt: now, UpdatedAt: now,
	}
}

func ptrFloat64(value float64) *float64 { return &value }
