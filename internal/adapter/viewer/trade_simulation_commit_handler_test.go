package viewer

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	applicationcommit "github.com/Nyukimin/RenCrow_CORE/internal/application/tradesimulationcommit"
	moduletrade "github.com/Nyukimin/RenCrow_CORE/modules/trade"
)

type simulationCommitRunnerStub struct {
	got    applicationcommit.Request
	result applicationcommit.Result
}

func (stub *simulationCommitRunnerStub) Commit(_ context.Context, request applicationcommit.Request) (applicationcommit.Result, error) {
	stub.got = request
	return stub.result, nil
}

func TestHandleTradeSimulationCommitRequiresExplicitAllowAndReturnsNoExternalAuthority(t *testing.T) {
	runner := &simulationCommitRunnerStub{result: applicationcommit.Result{Commit: &moduletrade.PrivateSimulationCommit{PortfolioID: "main-sim", Mode: "SIMULATION", PortfolioMutated: true}}}
	payload, err := json.Marshal(tradeSimulationCommitRequest{
		RequestID: "sim-1", AllowCommit: boolPointer(true), IdempotencyKey: "key-1",
		ExpectedPortfolioEventCount: 1, ExpectedPortfolioLatestEventHash: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ExpectedInputSnapshotSHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Plan:                        moduletrade.RiskPreviewPlan{ContractVersion: moduletrade.RiskPreviewPlanContractVersion},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/viewer/trade/simulation-commit", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Trace-ID", "trace-1")
	response := httptest.NewRecorder()
	HandleTradeSimulationCommit(TradeSimulationCommitOptions{Enabled: true, Runner: runner})(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var result tradeSimulationCommitProjection
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "committed" || result.AuthorizesExternalExecution || result.Commit == nil || runner.got.Requester != "viewer-trade-simulation-commit" || runner.got.TraceID != "trace-1" {
		t.Fatalf("result=%+v request=%+v", result, runner.got)
	}
}

func TestHandleTradeSimulationCommitRejectsMissingExplicitAllow(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/viewer/trade/simulation-commit", bytes.NewBufferString(`{"request_id":"sim-1","allow_commit":false}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	HandleTradeSimulationCommit(TradeSimulationCommitOptions{Enabled: true, Runner: &simulationCommitRunnerStub{}})(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func boolPointer(value bool) *bool { return &value }
