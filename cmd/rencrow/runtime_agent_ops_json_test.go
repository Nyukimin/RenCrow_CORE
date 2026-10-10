package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type agentOpsExecutorWithToolsStub struct {
	*agentOpsExecutorStub
	toolCalls int
}

func (s *agentOpsExecutorWithToolsStub) ExecuteTool(context.Context, string, map[string]interface{}) (string, error) {
	s.toolCalls++
	return s.output, s.err
}

func TestDecodeStrictAgentOpsRequest(t *testing.T) {
	deeplyNested := []byte(`{"message":` + strings.Repeat("[", 10_001) + `0` + strings.Repeat("]", 10_001) + `}`)
	invalidUTF8 := append([]byte(`{"message":"x`), 0xff)
	invalidUTF8 = append(invalidUTF8, []byte(`"}`)...)
	cases := []struct {
		name    string
		body    []byte
		want    agentOpsRequest
		wantErr bool
	}{
		{
			name: "preserves decoded message with literal replacement and surrogate pair",
			body: []byte(`{"message":" \t日本語\n� \ud83d\ude80 "}`),
			want: agentOpsRequest{Message: " \t日本語\n� 🚀 "},
		},
		{
			name: "retains DTO casefold field matching",
			body: []byte(`{"MESSAGE":"run"}`),
			want: agentOpsRequest{Message: "run"},
		},
		{
			name: "preserves a literal unicode escape sequence",
			body: []byte(`{"message":"\\ud800"}`),
			want: agentOpsRequest{Message: `\ud800`},
		},
		{
			name:    "rejects duplicate escaped field",
			body:    []byte(`{"message":"first","mess\u0061ge":"second"}`),
			wantErr: true,
		},
		{
			name:    "rejects duplicate casefold alias",
			body:    []byte(`{"operation":"dci_identity_acceptance","OPERATION":"dci_identity_acceptance"}`),
			wantErr: true,
		},
		{
			name:    "rejects unknown field",
			body:    []byte(`{"message":"run","source":"human"}`),
			wantErr: true,
		},
		{
			name:    "rejects non-object request",
			body:    []byte(`[ {"message":"run"} ]`),
			wantErr: true,
		},
		{
			name:    "rejects wrong field type",
			body:    []byte(`{"message":["run"]}`),
			wantErr: true,
		},
		{
			name:    "rejects malformed nested value",
			body:    []byte(`{"message":"run","query":[[[}`),
			wantErr: true,
		},
		{
			name:    "rejects excessive nesting",
			body:    deeplyNested,
			wantErr: true,
		},
		{
			name:    "rejects trailing value",
			body:    []byte(`{"message":"run"} {}`),
			wantErr: true,
		},
		{
			name:    "rejects invalid utf8",
			body:    invalidUTF8,
			wantErr: true,
		},
		{
			name:    "rejects unpaired high surrogate",
			body:    []byte(`{"message":"\ud800"}`),
			wantErr: true,
		},
		{
			name:    "rejects unpaired low surrogate",
			body:    []byte(`{"message":"\udc00"}`),
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeStrictAgentOpsRequest(tc.body)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("decodeStrictAgentOpsRequest(%q) succeeded: %#v", tc.body, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("decodeStrictAgentOpsRequest() error=%v", err)
			}
			if got != tc.want {
				t.Fatalf("decoded request=%#v want=%#v", got, tc.want)
			}
		})
	}
}

func TestDecodeStrictAgentOpsRequestPreservesNullAndMissingDTOValues(t *testing.T) {
	request, err := decodeStrictAgentOpsRequest([]byte(`{"message":null,"operation":"dci_identity_acceptance","query":"  identity  "}`))
	if err != nil {
		t.Fatalf("decode null message: %v", err)
	}
	if request.Message != "" || request.Query != "  identity  " {
		t.Fatalf("null DTO values changed: %#v", request)
	}
	branch, err := normalizeAgentOpsRequest(&request)
	if err != nil || branch != agentOpsRequestBranchDCIIdentityAcceptance || request.Query != "identity" {
		t.Fatalf("normalized null-message DCI request branch=%d query=%q err=%v", branch, request.Query, err)
	}

	request, err = decodeStrictAgentOpsRequest([]byte(`{"message":"run","operation":null,"query":null}`))
	if err != nil {
		t.Fatalf("decode null optional fields: %v", err)
	}
	branch, err = normalizeAgentOpsRequest(&request)
	if err != nil || branch != agentOpsRequestBranchLegacy {
		t.Fatalf("normalized null optional fields branch=%d request=%#v err=%v", branch, request, err)
	}

	request, err = decodeStrictAgentOpsRequest([]byte(`{}`))
	if err != nil {
		t.Fatalf("decode missing DTO fields: %v", err)
	}
	if _, err := normalizeAgentOpsRequest(&request); err == nil {
		t.Fatal("empty request unexpectedly passed the existing tagged-union validation")
	}
}

func TestDecodeStrictAgentOpsNativeResumeTarget(t *testing.T) {
	valid := []byte(`{"operation":"native_resume","target":{"task_id":"tsk_01a120e8-5f16-748f-9cc6-ebe0b51d8cc7","expected_core_run_id":"run_01a120e8-5f16-7511-9bdf-0c3f6a24a40d"}}`)
	request, err := decodeStrictAgentOpsRequest(valid)
	if err != nil {
		t.Fatalf("decode Resume request: %v", err)
	}
	branch, err := normalizeAgentOpsRequest(&request)
	if err != nil || branch != agentOpsRequestBranchNativeResume || request.ResumeTarget == nil {
		t.Fatalf("normalized Resume branch=%d target=%+v err=%v", branch, request.ResumeTarget, err)
	}
	if string(request.ResumeTarget.TaskID) != "tsk_01a120e8-5f16-748f-9cc6-ebe0b51d8cc7" ||
		string(request.ResumeTarget.ExpectedCoreRunID) != "run_01a120e8-5f16-7511-9bdf-0c3f6a24a40d" {
		t.Fatalf("decoded target=%+v", request.ResumeTarget)
	}

	for _, body := range [][]byte{
		[]byte(`{"operation":"native_resume","target":{"task_id":"tsk_01a120e8-5f16-748f-9cc6-ebe0b51d8cc7","TASK_ID":"tsk_01a120e8-5f16-748f-9cc6-ebe0b51d8cc7","expected_core_run_id":"run_01a120e8-5f16-7511-9bdf-0c3f6a24a40d"}}`),
		[]byte(`{"operation":"native_resume","target":{"task_id":"tsk_01a120e8-5f16-748f-9cc6-ebe0b51d8cc7"}}`),
		[]byte(`{"operation":"native_resume","target":{"task_id":"tsk_01a120e8-5f16-748f-9cc6-ebe0b51d8cc7","expected_core_run_id":"run_01a120e8-5f16-7511-9bdf-0c3f6a24a40d","other":"x"}}`),
		[]byte(`{"operation":"native_resume","target":null}`),
		[]byte(`{"operation":"native_resume","target":{"task_id":null,"expected_core_run_id":"run_01a120e8-5f16-7511-9bdf-0c3f6a24a40d"}}`),
	} {
		if _, err := decodeStrictAgentOpsRequest(body); err == nil {
			t.Fatalf("invalid Resume request was accepted: %s", body)
		}
	}
	mixed, err := decodeStrictAgentOpsRequest([]byte(`{"operation":"native_resume","message":"resume","target":{"task_id":"tsk_01a120e8-5f16-748f-9cc6-ebe0b51d8cc7","expected_core_run_id":"run_01a120e8-5f16-7511-9bdf-0c3f6a24a40d"}}`))
	if err != nil {
		t.Fatalf("decode mixed-shape request: %v", err)
	}
	if _, err := normalizeAgentOpsRequest(&mixed); err == nil {
		t.Fatal("mixed native_resume and message fields were accepted")
	}
	legacyWithResumeTarget, err := decodeStrictAgentOpsRequest([]byte(`{"message":"legacy","target":{"task_id":"tsk_01a120e8-5f16-748f-9cc6-ebe0b51d8cc7","expected_core_run_id":"run_01a120e8-5f16-7511-9bdf-0c3f6a24a40d"}}`))
	if err != nil {
		t.Fatalf("decode legacy request with Resume target: %v", err)
	}
	if _, err := normalizeAgentOpsRequest(&legacyWithResumeTarget); err == nil {
		t.Fatal("legacy message shape with native_resume target was accepted")
	}
}

func TestAgentOpsStrictDecoderPreservesMessageThroughAdmissionAndExecutor(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef"
	message := " \t日本語\n� 🚀 "
	executor := &agentOpsExecutorStub{output: "ok"}
	admission := &agentOpsNativeCodingAdmissionStub{}
	handler := newAgentOpsTestHandlerWithNativeAdmission(t, token, executor, admission)
	req := httptest.NewRequest(http.MethodPost, "/v1/agent/ops", strings.NewReader(`{"message":" \t日本語\n� \ud83d\ude80 "}`))
	setAgentOpsHeaders(req, token, "req-strict-message")
	req.RemoteAddr = "127.0.0.1:18791"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if admission.calls != 1 || len(admission.inputs) != 1 || admission.inputs[0].MessageText() != message {
		t.Fatalf("admission calls=%d inputs=%#v", admission.calls, admission.inputs)
	}
	if executor.calls != 1 || executor.input.MessageText() != message {
		t.Fatalf("executor calls=%d message=%q want=%q", executor.calls, executor.input.MessageText(), message)
	}
}

func TestAgentOpsStrictDecoderAcceptsExactBodyAndMessageLimits(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef"
	message := strings.Repeat("m", agentOpsMaxMessageBytes)
	payload := []byte(`{"message":"` + message + `"}`)
	if len(payload) > agentOpsMaxBodyBytes {
		t.Fatalf("message-limit payload is larger than body limit: %d", len(payload))
	}
	body := []byte(strings.Repeat(" ", agentOpsMaxBodyBytes-len(payload)))
	body = append(body, payload...)
	if len(body) != agentOpsMaxBodyBytes {
		t.Fatalf("request body length=%d want=%d", len(body), agentOpsMaxBodyBytes)
	}

	executor := &agentOpsExecutorStub{output: "ok"}
	admission := &agentOpsNativeCodingAdmissionStub{}
	handler := newAgentOpsTestHandlerWithNativeAdmission(t, token, executor, admission)
	req := httptest.NewRequest(http.MethodPost, "/v1/agent/ops", strings.NewReader(string(body)))
	setAgentOpsHeaders(req, token, "req-strict-limits")
	req.RemoteAddr = "127.0.0.1:18791"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if admission.calls != 1 || len(admission.inputs) != 1 {
		t.Fatalf("admission calls=%d inputs=%d", admission.calls, len(admission.inputs))
	}
	if admission.inputs[0].MessageText() != message {
		t.Fatalf("admission message length=%d want=%d", len(admission.inputs[0].MessageText()), len(message))
	}
	if executor.calls != 1 {
		t.Fatalf("executor calls=%d", executor.calls)
	}
	if executor.input.MessageText() != message {
		t.Fatalf("executor message length=%d want=%d", len(executor.input.MessageText()), len(message))
	}
}

var _ agentOpsExecutor = (*agentOpsExecutorStub)(nil)
