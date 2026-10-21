package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/speech"
	"github.com/Nyukimin/RenCrow_CORE/internal/speecheval"
)

const speechEvalTestToken = "speech-eval-test-owner-token"

type gatewayRequest struct {
	SpeakerID      string            `json:"speaker_id"`
	SessionID      string            `json:"session_id"`
	TurnID         string            `json:"turn_id"`
	UtteranceIndex int               `json:"utterance_index"`
	SpeechPlan     speech.SpeechPlan `json:"speech_plan"`
}

func TestRunWritesTwentyBlindPairsAndKeepsAnswerOutsideAudioRoot(t *testing.T) {
	root := t.TempDir()
	outputDir := filepath.Join(root, "tts-output")
	if err := os.Mkdir(outputDir, 0o755); err != nil {
		t.Fatal(err)
	}
	answerPath := filepath.Join(root, "private", "speech-eval-answer.json")
	if err := os.Mkdir(filepath.Dir(answerPath), 0o700); err != nil {
		t.Fatal(err)
	}
	tokenPath := filepath.Join(root, "owner.token")
	if err := os.WriteFile(tokenPath, []byte(speechEvalTestToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var requestMu sync.Mutex
	var requests []gatewayRequest
	var requestIDs []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/tts" {
			t.Errorf("Gateway request = %s %s, want POST /api/tts", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer "+speechEvalTestToken {
			t.Error("Gateway request did not carry the configured token file value")
		}
		var request gatewayRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode Gateway request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		requestID := r.Header.Get("X-RenCrow-TTS-Request-Id")
		if requestID == "" {
			t.Error("Gateway request has no canonical request ID")
		}
		requestMu.Lock()
		requests = append(requests, request)
		requestIDs = append(requestIDs, requestID)
		requestMu.Unlock()
		hash := sha256.Sum256([]byte(request.TurnID))
		seed := binary.BigEndian.Uint64(hash[:8])
		w.Header().Set("Content-Type", "audio/wav")
		w.Header().Set("X-RenCrow-TTS-Gateway-Service", "tts-gateway")
		w.Header().Set("X-RenCrow-TTS-Request-Id", requestID)
		w.Header().Set("X-RenCrow-TTS-Profile-Revision", "1")
		w.Header().Set("X-RenCrow-TTS-Utterance-Index", fmt.Sprint(request.UtteranceIndex))
		w.Header().Set("X-RenCrow-TTS-Irodori-Seed", fmt.Sprint(seed))
		w.Header().Set("X-RenCrow-TTS-Synthesis-Ms", "219")
		_, _ = w.Write(speechEvalTestWAV())
	}))
	defer server.Close()

	var stdout, stderr strings.Builder
	orderIndex := 0
	chooseAExpressive := func() (bool, error) {
		aExpressive := orderIndex%2 == 1
		orderIndex++
		return aExpressive, nil
	}
	err := runWithVariantOrder(context.Background(), []string{
		"--gateway-url", server.URL,
		"--token-file", tokenPath,
		"--output-dir", outputDir,
		"--answer-file", answerPath,
		"--expected-profile-revision", "1",
	}, &stdout, &stderr, func(string) (string, bool) { return "", false }, chooseAExpressive)
	if err != nil {
		t.Fatalf("run() error = %v; stderr=%q", err, stderr.String())
	}
	if strings.Contains(stdout.String()+stderr.String(), speechEvalTestToken) {
		t.Fatal("CLI output leaked the Gateway token")
	}

	requestMu.Lock()
	gotRequests := append([]gatewayRequest(nil), requests...)
	gotRequestIDs := append([]string(nil), requestIDs...)
	requestMu.Unlock()
	if len(gotRequests) != 40 {
		t.Fatalf("Gateway request count = %d, want 40", len(gotRequests))
	}
	if len(gotRequestIDs) != 40 {
		t.Fatalf("Gateway request ID count = %d, want 40", len(gotRequestIDs))
	}
	seenRequestIDs := make(map[string]bool)
	for i, id := range gotRequestIDs {
		if id == "" || seenRequestIDs[id] {
			t.Errorf("Gateway request ID[%d] = %q, want unique non-empty ID", i, id)
		}
		seenRequestIDs[id] = true
	}

	manifestPath := filepath.Join(outputDir, "speech-eval", "manifest.json")
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read public manifest: %v", err)
	}
	var manifest struct {
		SchemaVersion string `json:"schema_version"`
		SetID         string `json:"set_id"`
		Items         []struct {
			ID            string `json:"id"`
			Text          string `json:"text"`
			Intent        string `json:"intent"`
			CueApplicable bool   `json:"cue_applicable"`
			CueHint       string `json:"cue_hint"`
			AURL          string `json:"a_url"`
			BURL          string `json:"b_url"`
		} `json:"items"`
	}
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("decode public manifest: %v", err)
	}
	if manifest.SchemaVersion != "rencrow.speech-eval/1" || len(manifest.Items) != 20 {
		t.Fatalf("public manifest schema/items = %q/%d", manifest.SchemaVersion, len(manifest.Items))
	}
	if !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(manifest.SetID) {
		t.Errorf("set_id is not opaque lowercase hex: %q", manifest.SetID)
	}
	var manifestShape map[string]json.RawMessage
	if err := json.Unmarshal(manifestBytes, &manifestShape); err != nil || len(manifestShape) != 3 {
		t.Errorf("public manifest has extra top-level metadata: keys=%v err=%v", mapKeys(manifestShape), err)
	}
	var itemShapes struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(manifestBytes, &itemShapes); err != nil {
		t.Fatalf("decode public manifest item shape: %v", err)
	}
	for i, item := range itemShapes.Items {
		if len(item) != 7 {
			t.Errorf("public manifest item[%d] has %d fields, want exactly 7", i, len(item))
		}
	}
	publicText := string(manifestBytes)
	for _, privateValue := range []string{"neutral", "expressive", "seed", "profile_revision", "sha256", "synthesis_ms", "session_id", "turn_id"} {
		if strings.Contains(publicText, privateValue) {
			t.Errorf("public manifest contains private value/metadata %q", privateValue)
		}
	}

	answerBytes, err := os.ReadFile(answerPath)
	if err != nil {
		t.Fatalf("read private answer: %v", err)
	}
	if strings.Contains(string(answerBytes), speechEvalTestToken) {
		t.Fatal("private answer leaked the Gateway token")
	}
	var answer speecheval.Answer
	if err := json.Unmarshal(answerBytes, &answer); err != nil {
		t.Fatalf("decode private answer: %v", err)
	}
	if answer.SchemaVersion != "rencrow.speech-eval-answer/1" || answer.SetID != manifest.SetID || answer.Profile != 1 || len(answer.Items) != 20 || len(answer.Mapping) != 20 {
		t.Fatalf("private answer contract = %#v", answer)
	}
	seenOrderings := map[string]bool{}
	for i, mapping := range answer.Mapping {
		if mapping.ID != manifest.Items[i].ID || (mapping.A != "neutral" && mapping.A != "expressive") || (mapping.B != "neutral" && mapping.B != "expressive") || mapping.A == mapping.B {
			t.Errorf("private per-case A/B mapping[%d] is invalid: %#v", i, mapping)
		}
		seenOrderings[mapping.A+"/"+mapping.B] = true
	}
	if len(seenOrderings) != 2 {
		t.Errorf("private mapping should contain both A/B orderings: %#v", answer.Mapping)
	}

	filenamePattern := regexp.MustCompile(`^[a-f0-9]{32}\.wav$`)
	for i, item := range manifest.Items {
		wantID := fmt.Sprintf("%02d", i+1)
		if item.ID != wantID {
			t.Errorf("manifest item[%d].id = %q, want %q", i, item.ID, wantID)
		}
		if item.Intent != speecheval.AllCases()[i].Intent {
			t.Errorf("manifest intent[%d] = %q, want Japanese rating description %q", i, item.Intent, speecheval.AllCases()[i].Intent)
		}
		if item.CueApplicable != (item.CueHint != "") {
			t.Errorf("manifest cue hint[%d] = %q is inconsistent with cue_applicable=%t", i, item.CueHint, item.CueApplicable)
		}
		for _, rawURL := range []string{item.AURL, item.BURL} {
			parsed, err := url.Parse(rawURL)
			if err != nil || parsed.Path != "/viewer/tts/audio" {
				t.Fatalf("manifest audio URL %q has invalid route: %v", rawURL, err)
			}
			rel := parsed.Query().Get("path")
			parts := strings.Split(filepath.ToSlash(rel), "/")
			if len(parts) != 3 || parts[0] != "speech-eval" || parts[1] != manifest.SetID || !filenamePattern.MatchString(parts[2]) {
				t.Errorf("manifest audio path is not an opaque in-root WAV: %q", rel)
			}
			if strings.Contains(strings.ToLower(parts[2]), "neutral") || strings.Contains(strings.ToLower(parts[2]), "expressive") {
				t.Errorf("audio filename reveals variant: %q", parts[2])
			}
			if _, err := os.Stat(filepath.Join(outputDir, filepath.FromSlash(rel))); err != nil {
				t.Errorf("manifest WAV %q is unavailable: %v", rel, err)
			}
		}
	}

	for i := 0; i < len(gotRequests); i += 2 {
		a, b := gotRequests[i], gotRequests[i+1]
		if a.SpeakerID != "mio" || b.SpeakerID != "mio" || a.SessionID == "" || a.SessionID != b.SessionID || a.TurnID == "" || a.TurnID != b.TurnID || a.UtteranceIndex != 0 || b.UtteranceIndex != 0 {
			t.Errorf("A/B request identities differ or are invalid: A=%#v B=%#v", a, b)
		}
		textA := speech.Normalize(a.SpeechPlan).DisplayText
		textB := speech.Normalize(b.SpeechPlan).DisplayText
		if textA != textB || textA != manifest.Items[i/2].Text {
			t.Errorf("A/B text = %q / %q, manifest %q", textA, textB, manifest.Items[i/2].Text)
		}
		pair := speecheval.BuildPair(speecheval.AllCases()[i/2], a.SessionID, a.TurnID)
		for side, request := range []gatewayRequest{a, b} {
			mapping := answer.Mapping[i/2]
			variant := mapping.A
			wantPlan := pair.NeutralPlan
			if side == 1 {
				variant = mapping.B
			}
			if variant == "expressive" {
				wantPlan = pair.ExpressivePlan
			}
			plan := speech.Normalize(request.SpeechPlan).Plan
			if !reflect.DeepEqual(plan, wantPlan) {
				t.Errorf("case %s %s plan does not match private variant mapping %q: got=%#v want=%#v", manifest.Items[i/2].ID, map[int]string{0: "A", 1: "B"}[side], variant, plan, wantPlan)
			}
			if variant == "neutral" {
				if plan.Utterances[0].Delivery.Tone != "neutral" {
					t.Errorf("case %s %s neutral delivery = %#v", manifest.Items[i/2].ID, map[int]string{0: "A", 1: "B"}[side], plan.Utterances[0].Delivery)
				}
				for _, segment := range plan.Utterances[0].Segments {
					if len(segment.Before) > 0 || len(segment.After) > 0 {
						t.Errorf("case %s %s neutral plan retained expressive cues: %#v", manifest.Items[i/2].ID, map[int]string{0: "A", 1: "B"}[side], segment)
					}
				}
			}
		}
	}

	if runtime.GOOS != "windows" {
		answerInfo, err := os.Stat(answerPath)
		if err != nil || answerInfo.Mode().Perm() != 0o600 {
			t.Errorf("private answer permissions = %v, err=%v; want 0600", answerInfo, err)
		}
	}
	for i, item := range answer.Items {
		if item.A.Seed != item.B.Seed || item.A.ProfileRevision != 1 || item.B.ProfileRevision != 1 || item.A.SynthesisMS == 0 || item.B.SynthesisMS == 0 || len(item.A.SHA256) != 64 || len(item.B.SHA256) != 64 {
			t.Errorf("private answer item[%d] is missing required pair metadata: %#v", i, item)
		}
		for _, artifact := range []speecheval.AnswerArtifact{item.A, item.B} {
			wav, err := os.ReadFile(filepath.Join(outputDir, "speech-eval", manifest.SetID, artifact.Filename))
			if err != nil {
				t.Errorf("read private answer WAV %q: %v", artifact.Filename, err)
				continue
			}
			hash := sha256.Sum256(wav)
			if got := hex.EncodeToString(hash[:]); got != artifact.SHA256 {
				t.Errorf("private answer SHA-256 for %q = %s, want %s", artifact.Filename, artifact.SHA256, got)
			}
		}
	}
	if filepath.Clean(answerPath) == filepath.Clean(filepath.Join(outputDir, "speech-eval", "manifest.json")) || isPathWithin(outputDir, answerPath) {
		t.Errorf("private answer path is inside Viewer audio root: %q", answerPath)
	}
}

func TestRunRejectsAnswerPathInsideViewerRootBeforeSynthesis(t *testing.T) {
	outputDir := t.TempDir()
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
	}))
	defer server.Close()
	err := run(context.Background(), []string{
		"--gateway-url", server.URL,
		"--token-env", "SPEECH_EVAL_TOKEN",
		"--output-dir", outputDir,
		"--answer-file", filepath.Join(outputDir, "speech-eval", "answer.json"),
		"--expected-profile-revision", "1",
	}, io.Discard, io.Discard, func(string) (string, bool) { return speechEvalTestToken, true })
	if err == nil {
		t.Fatal("run() accepted a private answer path under the Viewer audio root")
	}
	if requests != 0 {
		t.Fatalf("Gateway request count = %d, want 0 after unsafe answer path", requests)
	}
	alias := filepath.Join(t.TempDir(), "audio-root-alias")
	if err := os.Symlink(outputDir, alias); err == nil {
		err := run(context.Background(), []string{
			"--gateway-url", server.URL,
			"--token-env", "SPEECH_EVAL_TOKEN",
			"--output-dir", outputDir,
			"--answer-file", filepath.Join(alias, "answer.json"),
			"--expected-profile-revision", "1",
		}, io.Discard, io.Discard, func(string) (string, bool) { return speechEvalTestToken, true })
		if err == nil {
			t.Fatal("run() accepted a private answer path under the Viewer root through a symlink")
		}
	}
	if requests != 0 {
		t.Fatalf("Gateway request count = %d after symlink path check, want 0", requests)
	}
}

func TestRunRejectsUnapprovedProfileWithoutPublishingOutput(t *testing.T) {
	root := t.TempDir()
	outputDir := filepath.Join(root, "tts-output")
	if err := os.Mkdir(outputDir, 0o755); err != nil {
		t.Fatal(err)
	}
	answerPath := filepath.Join(root, "answer.json")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request gatewayRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode Gateway request: %v", err)
		}
		requestID := r.Header.Get("X-RenCrow-TTS-Request-Id")
		w.Header().Set("Content-Type", "audio/wav")
		w.Header().Set("X-RenCrow-TTS-Gateway-Service", "tts-gateway")
		w.Header().Set("X-RenCrow-TTS-Request-Id", requestID)
		w.Header().Set("X-RenCrow-TTS-Profile-Revision", "2")
		w.Header().Set("X-RenCrow-TTS-Utterance-Index", "0")
		w.Header().Set("X-RenCrow-TTS-Irodori-Seed", "12345")
		w.Header().Set("X-RenCrow-TTS-Synthesis-Ms", "219")
		_, _ = w.Write(speechEvalTestWAV())
	}))
	defer server.Close()
	err := run(context.Background(), []string{
		"--gateway-url", server.URL,
		"--token-env", "SPEECH_EVAL_TOKEN",
		"--output-dir", outputDir,
		"--answer-file", answerPath,
		"--expected-profile-revision", "1",
	}, io.Discard, io.Discard, func(string) (string, bool) { return speechEvalTestToken, true })
	if err == nil {
		t.Fatal("run() accepted Gateway profile revision 2 while revision 1 was approved")
	}
	if _, err := os.Stat(filepath.Join(outputDir, "speech-eval", "manifest.json")); !os.IsNotExist(err) {
		t.Fatalf("public manifest exists after profile mismatch, stat error=%v", err)
	}
	if _, err := os.Stat(answerPath); !os.IsNotExist(err) {
		t.Fatalf("private answer exists after profile mismatch, stat error=%v", err)
	}
}

func TestParseOptionsUsesFiveMinuteBoundedRequestTimeout(t *testing.T) {
	baseArgs := []string{
		"--gateway-url", "http://127.0.0.1:7870",
		"--token-env", "SPEECH_EVAL_TOKEN",
		"--output-dir", "/tmp/tts-output",
		"--answer-file", "/tmp/speech-eval-answer.json",
		"--expected-profile-revision", "1",
	}
	opts, err := parseOptions(baseArgs, io.Discard)
	if err != nil {
		t.Fatalf("parseOptions() error = %v", err)
	}
	if opts.requestTimeout != 300*time.Second {
		t.Errorf("default request timeout = %s, want 300s", opts.requestTimeout)
	}
	customArgs := append(append([]string(nil), baseArgs...), "--request-timeout", "2m")
	custom, err := parseOptions(customArgs, io.Discard)
	if err != nil || custom.requestTimeout != 2*time.Minute {
		t.Errorf("custom request timeout = %s, err=%v; want 2m", custom.requestTimeout, err)
	}
	tooLongArgs := append(append([]string(nil), baseArgs...), "--request-timeout", "6m")
	if _, err := parseOptions(tooLongArgs, io.Discard); err == nil {
		t.Fatal("parseOptions accepted a request timeout above the five-minute bound")
	}
}

func TestRunPassesRequestTimeoutToGatewayBridge(t *testing.T) {
	root := t.TempDir()
	outputDir := filepath.Join(root, "tts-output")
	if err := os.Mkdir(outputDir, 0o755); err != nil {
		t.Fatal(err)
	}
	answerPath := filepath.Join(root, "answer.json")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(250 * time.Millisecond):
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer server.Close()
	err := run(context.Background(), []string{
		"--gateway-url", server.URL,
		"--token-env", "SPEECH_EVAL_TOKEN",
		"--output-dir", outputDir,
		"--answer-file", answerPath,
		"--expected-profile-revision", "1",
		"--request-timeout", "50ms",
	}, io.Discard, io.Discard, func(string) (string, bool) { return speechEvalTestToken, true })
	if err == nil || !strings.Contains(err.Error(), "Client.Timeout exceeded") {
		t.Fatalf("run() error = %v, want the configured 50ms Gateway client timeout", err)
	}
	if _, err := os.Stat(filepath.Join(outputDir, "speech-eval", "manifest.json")); !os.IsNotExist(err) {
		t.Fatalf("public manifest exists after Gateway timeout, stat error=%v", err)
	}
	if _, err := os.Stat(answerPath); !os.IsNotExist(err) {
		t.Fatalf("private answer exists after Gateway timeout, stat error=%v", err)
	}
}

func TestReadOwnerTokenFileRequiresOwnerOnlyRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owner.token")
	if err := os.WriteFile(path, []byte("owner-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := readOwnerTokenFile(path); err != nil || got != "owner-token" {
		t.Fatalf("readOwnerTokenFile() = %q, %v", got, err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := readOwnerTokenFile(path); err == nil {
		t.Fatal("readOwnerTokenFile accepted group-readable token file")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(filepath.Dir(path), "target"), path); err == nil {
		if _, err := readOwnerTokenFile(path); err == nil {
			t.Fatal("readOwnerTokenFile accepted a symlink")
		}
	}
}

func TestRunCanResolveTokenFromEnvironment(t *testing.T) {
	got, err := resolveGatewayToken("", "SPEECH_EVAL_TOKEN", func(name string) (string, bool) {
		if name == "SPEECH_EVAL_TOKEN" {
			return speechEvalTestToken, true
		}
		return "", false
	})
	if err != nil || got != speechEvalTestToken {
		t.Fatalf("resolveGatewayToken() = %q, %v", got, err)
	}
}

func speechEvalTestWAV() []byte {
	const sampleCount = 160
	dataSize := sampleCount * 2
	wav := make([]byte, 44+dataSize)
	copy(wav[0:4], "RIFF")
	binary.LittleEndian.PutUint32(wav[4:8], uint32(len(wav)-8))
	copy(wav[8:12], "WAVE")
	copy(wav[12:16], "fmt ")
	binary.LittleEndian.PutUint32(wav[16:20], 16)
	binary.LittleEndian.PutUint16(wav[20:22], 1)
	binary.LittleEndian.PutUint16(wav[22:24], 1)
	binary.LittleEndian.PutUint32(wav[24:28], 16000)
	binary.LittleEndian.PutUint32(wav[28:32], 32000)
	binary.LittleEndian.PutUint16(wav[32:34], 2)
	binary.LittleEndian.PutUint16(wav[34:36], 16)
	copy(wav[36:40], "data")
	binary.LittleEndian.PutUint32(wav[40:44], uint32(dataSize))
	for i := 0; i < sampleCount; i++ {
		binary.LittleEndian.PutUint16(wav[44+i*2:46+i*2], uint16(1000+i%3))
	}
	return wav
}

func isPathWithin(root, candidate string) bool {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	candidateAbs, err := filepath.Abs(candidate)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(rootAbs, candidateAbs)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func mapKeys(values map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}
