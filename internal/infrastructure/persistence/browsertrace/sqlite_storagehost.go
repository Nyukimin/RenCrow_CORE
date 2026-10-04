package browsertrace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"

	domaintrace "github.com/Nyukimin/RenCrow_CORE/internal/domain/browsertrace"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	BrowserTraceSaveTraceRunStorageHostOperation                   = "save_trace_run"
	BrowserTraceSaveAPICandidateStorageHostOperation               = "save_api_candidate"
	BrowserTraceSaveAPICandidateSchemaStorageHostOperation         = "save_api_candidate_schema"
	BrowserTraceSaveAPIValidationStorageHostOperation              = "save_api_candidate_validation_result"
	BrowserTraceSaveAPICoverageStorageHostOperation                = "save_api_coverage_report"
	BrowserTraceSaveAPIArtifactStorageHostOperation                = "save_api_artifact"
	BrowserTraceCreateAPIArtifactWithPublicationIntentOperation    = "create_api_artifact_with_publication_intent"
	BrowserTraceSupersedeAPIArtifactWithPublicationIntentOperation = "supersede_api_artifact_with_publication_intent"
)

const browserTraceStorageHostReceiptTable = "browser_trace_storage_host_operation_receipt"

var browserTraceStorageHostOpIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

type BrowserTraceStorageHostOperationIdentity struct {
	OpID             string
	Operation        string
	PayloadSHA256    string
	WriterGeneration int64
}

type BrowserTraceStorageHostEffectKind string

const (
	BrowserTraceStorageHostTraceRunEffect             BrowserTraceStorageHostEffectKind = "trace_run"
	BrowserTraceStorageHostCandidateEffect            BrowserTraceStorageHostEffectKind = "api_candidate"
	BrowserTraceStorageHostSchemaEffect               BrowserTraceStorageHostEffectKind = "api_candidate_schema"
	BrowserTraceStorageHostValidationEffect           BrowserTraceStorageHostEffectKind = "api_candidate_validation"
	BrowserTraceStorageHostCoverageEffect             BrowserTraceStorageHostEffectKind = "api_coverage_report"
	BrowserTraceStorageHostArtifactEffect             BrowserTraceStorageHostEffectKind = "api_artifact"
	BrowserTraceStorageHostArtifactCreationEffect     BrowserTraceStorageHostEffectKind = "api_artifact_creation"
	BrowserTraceStorageHostArtifactSupersessionEffect BrowserTraceStorageHostEffectKind = "api_artifact_supersession"
)

type BrowserTraceStorageHostEffect struct {
	Kind BrowserTraceStorageHostEffectKind `json:"kind"`
	ID   string                            `json:"id"`
}

type browserTraceStorageHostReceiptProof struct {
	Version          int                           `json:"version"`
	Operation        string                        `json:"operation"`
	PayloadSHA256    string                        `json:"payload_sha256"`
	WriterGeneration int64                         `json:"writer_generation"`
	Effect           BrowserTraceStorageHostEffect `json:"effect"`
	Payload          json.RawMessage               `json:"payload"`
}

type browserTraceStorageHostReceiptRow struct {
	Operation        string
	PayloadSHA256    string
	WriterGeneration int64
	Effect           BrowserTraceStorageHostEffect
	Result           []byte
	ResultSHA256     string
	Proof            []byte
	ProofSHA256      string
}

type browserTraceStorageHostCreationPayload struct {
	Item   domaintrace.APIArtifact `json:"item"`
	Intent json.RawMessage         `json:"intent"`
}

type browserTraceStorageHostSupersessionPayload struct {
	PredecessorID modulecore.ArtifactID `json:"predecessor_id"`
	SuccessorID   modulecore.ArtifactID `json:"successor_id"`
	Fact          json.RawMessage       `json:"fact"`
}

func (identity BrowserTraceStorageHostOperationIdentity) validate() error {
	if !browserTraceStorageHostOpIDPattern.MatchString(identity.OpID) || !validBrowserTraceStorageHostOperation(identity.Operation) ||
		!validBrowserTraceSHA256(identity.PayloadSHA256) || identity.WriterGeneration <= 0 {
		return errors.New("browser trace storage-host operation identity is incomplete")
	}
	return nil
}

func validBrowserTraceSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && value == string(bytes.ToLower([]byte(value)))
}

func validBrowserTraceStorageHostOperation(operation string) bool {
	switch operation {
	case BrowserTraceSaveTraceRunStorageHostOperation,
		BrowserTraceSaveAPICandidateStorageHostOperation,
		BrowserTraceSaveAPICandidateSchemaStorageHostOperation,
		BrowserTraceSaveAPIValidationStorageHostOperation,
		BrowserTraceSaveAPICoverageStorageHostOperation,
		BrowserTraceSaveAPIArtifactStorageHostOperation,
		BrowserTraceCreateAPIArtifactWithPublicationIntentOperation,
		BrowserTraceSupersedeAPIArtifactWithPublicationIntentOperation:
		return true
	default:
		return false
	}
}

func browserTraceEffectKindForOperation(operation string) (BrowserTraceStorageHostEffectKind, bool) {
	switch operation {
	case BrowserTraceSaveTraceRunStorageHostOperation:
		return BrowserTraceStorageHostTraceRunEffect, true
	case BrowserTraceSaveAPICandidateStorageHostOperation:
		return BrowserTraceStorageHostCandidateEffect, true
	case BrowserTraceSaveAPICandidateSchemaStorageHostOperation:
		return BrowserTraceStorageHostSchemaEffect, true
	case BrowserTraceSaveAPIValidationStorageHostOperation:
		return BrowserTraceStorageHostValidationEffect, true
	case BrowserTraceSaveAPICoverageStorageHostOperation:
		return BrowserTraceStorageHostCoverageEffect, true
	case BrowserTraceSaveAPIArtifactStorageHostOperation:
		return BrowserTraceStorageHostArtifactEffect, true
	case BrowserTraceCreateAPIArtifactWithPublicationIntentOperation:
		return BrowserTraceStorageHostArtifactCreationEffect, true
	case BrowserTraceSupersedeAPIArtifactWithPublicationIntentOperation:
		return BrowserTraceStorageHostArtifactSupersessionEffect, true
	default:
		return "", false
	}
}

func (s *SQLiteStore) SaveTraceRunForStorageHostOperation(ctx context.Context, identity BrowserTraceStorageHostOperationIdentity, item domaintrace.TraceRun) error {
	if err := domaintrace.ValidateTraceRun(item); err != nil {
		return err
	}
	return s.saveStorageHostOperation(ctx, identity, BrowserTraceStorageHostEffect{Kind: BrowserTraceStorageHostTraceRunEffect, ID: string(item.RunID)}, item,
		func(ctx context.Context, conn *sql.Conn) error {
			return saveBrowserTraceStorageHostRow(ctx, conn, BrowserTraceStorageHostTraceRunEffect, item)
		})
}

func (s *SQLiteStore) SaveAPICandidateForStorageHostOperation(ctx context.Context, identity BrowserTraceStorageHostOperationIdentity, item domaintrace.APICandidate) error {
	if err := domaintrace.ValidateAPICandidate(item); err != nil {
		return err
	}
	return s.saveStorageHostOperation(ctx, identity, BrowserTraceStorageHostEffect{Kind: BrowserTraceStorageHostCandidateEffect, ID: item.CandidateID}, item,
		func(ctx context.Context, conn *sql.Conn) error {
			return saveBrowserTraceStorageHostRow(ctx, conn, BrowserTraceStorageHostCandidateEffect, item)
		})
}

func (s *SQLiteStore) SaveAPICandidateSchemaForStorageHostOperation(ctx context.Context, identity BrowserTraceStorageHostOperationIdentity, item domaintrace.APICandidateSchema) error {
	if err := domaintrace.ValidateAPICandidateSchema(item); err != nil {
		return err
	}
	return s.saveStorageHostOperation(ctx, identity, BrowserTraceStorageHostEffect{Kind: BrowserTraceStorageHostSchemaEffect, ID: item.SchemaID}, item,
		func(ctx context.Context, conn *sql.Conn) error {
			return saveBrowserTraceStorageHostRow(ctx, conn, BrowserTraceStorageHostSchemaEffect, item)
		})
}

func (s *SQLiteStore) SaveAPICandidateValidationResultForStorageHostOperation(ctx context.Context, identity BrowserTraceStorageHostOperationIdentity, item domaintrace.APICandidateValidationResult) error {
	if err := domaintrace.ValidateAPICandidateValidationResult(item); err != nil {
		return err
	}
	return s.saveStorageHostOperation(ctx, identity, BrowserTraceStorageHostEffect{Kind: BrowserTraceStorageHostValidationEffect, ID: item.ValidationID}, item,
		func(ctx context.Context, conn *sql.Conn) error {
			candidate, found, err := findAPICandidateOnConn(ctx, conn, item.CandidateID)
			if err != nil {
				return err
			}
			if !found {
				return fmt.Errorf("candidate %s for validation %s was not found", item.CandidateID, item.ValidationID)
			}
			if item.TaskID != candidate.TaskID || item.RunID != candidate.RunID || item.ActorID != candidate.ActorID {
				return fmt.Errorf("validation %s task/run/actor identity does not match candidate %s", item.ValidationID, item.CandidateID)
			}
			return saveBrowserTraceStorageHostRow(ctx, conn, BrowserTraceStorageHostValidationEffect, item)
		})
}

func (s *SQLiteStore) SaveAPICoverageReportForStorageHostOperation(ctx context.Context, identity BrowserTraceStorageHostOperationIdentity, item domaintrace.APICoverageReport) error {
	if err := domaintrace.ValidateAPICoverageReport(item); err != nil {
		return err
	}
	return s.saveStorageHostOperation(ctx, identity, BrowserTraceStorageHostEffect{Kind: BrowserTraceStorageHostCoverageEffect, ID: string(item.ArtifactID)}, item,
		func(ctx context.Context, conn *sql.Conn) error {
			return saveBrowserTraceStorageHostRow(ctx, conn, BrowserTraceStorageHostCoverageEffect, item)
		})
}

func (s *SQLiteStore) SaveAPIArtifactForStorageHostOperation(ctx context.Context, identity BrowserTraceStorageHostOperationIdentity, item domaintrace.APIArtifact) error {
	if err := domaintrace.ValidateAPIArtifact(item); err != nil {
		return err
	}
	return s.saveStorageHostOperation(ctx, identity, BrowserTraceStorageHostEffect{Kind: BrowserTraceStorageHostArtifactEffect, ID: string(item.ArtifactID)}, item,
		func(ctx context.Context, conn *sql.Conn) error {
			payload, err := json.Marshal(item)
			if err != nil {
				return err
			}
			return saveAPIArtifactOnConn(ctx, conn, item, payload)
		})
}

func (s *SQLiteStore) CreateAPIArtifactWithPublicationIntentForStorageHostOperation(ctx context.Context, identity BrowserTraceStorageHostOperationIdentity, item domaintrace.APIArtifact, intent modulecore.EventEnvelope) error {
	if err := domaintrace.ValidateAPIArtifact(item); err != nil {
		return err
	}
	if err := domaintrace.ValidateAPIArtifactPublicationIntent(item, intent); err != nil {
		return err
	}
	payload := browserTraceStorageHostCreationPayload{Item: item}
	intentBytes, err := json.Marshal(intent)
	if err != nil {
		return err
	}
	payload.Intent = intentBytes
	return s.saveStorageHostOperation(ctx, identity, BrowserTraceStorageHostEffect{Kind: BrowserTraceStorageHostArtifactCreationEffect, ID: string(item.ArtifactID)}, payload,
		func(ctx context.Context, conn *sql.Conn) error {
			artifactBytes, err := json.Marshal(item)
			if err != nil {
				return err
			}
			return createAPIArtifactWithPublicationIntentOnConn(ctx, conn, item, intent, artifactBytes, intentBytes)
		})
}

func (s *SQLiteStore) SupersedeAPIArtifactWithPublicationIntentForStorageHostOperation(ctx context.Context, identity BrowserTraceStorageHostOperationIdentity, predecessorID, successorID modulecore.ArtifactID, fact modulecore.EventEnvelope) error {
	if err := modulecore.ValidateArtifactSupersession(predecessorID, successorID); err != nil {
		return err
	}
	if err := domaintrace.ValidatePersistedAPIArtifactSupersessionFact(fact); err != nil {
		return err
	}
	factBytes, err := json.Marshal(fact)
	if err != nil {
		return err
	}
	payload := browserTraceStorageHostSupersessionPayload{PredecessorID: predecessorID, SuccessorID: successorID, Fact: factBytes}
	return s.saveStorageHostOperation(ctx, identity, BrowserTraceStorageHostEffect{Kind: BrowserTraceStorageHostArtifactSupersessionEffect, ID: string(predecessorID)}, payload,
		func(ctx context.Context, conn *sql.Conn) error {
			return supersedeAPIArtifactWithPublicationIntentOnConn(ctx, conn, predecessorID, successorID, fact, factBytes)
		})
}

func (s *SQLiteStore) saveStorageHostOperation(ctx context.Context, identity BrowserTraceStorageHostOperationIdentity, effect BrowserTraceStorageHostEffect, payload any, work func(context.Context, *sql.Conn) error) error {
	if err := validateBrowserTraceStorageHostReceiptIdentity(identity, effect); err != nil {
		return err
	}
	if s == nil || s.db == nil {
		return errors.New("browser trace sqlite store is closed")
	}
	encodedPayload, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	proof, err := json.Marshal(browserTraceStorageHostReceiptProof{
		Version: 1, Operation: identity.Operation, PayloadSHA256: identity.PayloadSHA256,
		WriterGeneration: identity.WriterGeneration, Effect: effect, Payload: encodedPayload,
	})
	if err != nil {
		return err
	}
	result := []byte("null")
	return s.withArtifactTransaction(ctx, "storage-host "+identity.Operation, func(ctx context.Context, conn *sql.Conn) error {
		stored, found, err := readBrowserTraceStorageHostReceipt(ctx, conn, identity.OpID)
		if err != nil {
			return err
		}
		if found {
			if err := validateBrowserTraceStorageHostReceipt(stored, identity, effect); err != nil {
				return err
			}
			return validateBrowserTraceStorageHostEffectProof(ctx, conn, stored.Proof, identity, effect)
		}
		if err := work(ctx, conn); err != nil {
			return err
		}
		if err := validateBrowserTraceStorageHostEffectProof(ctx, conn, proof, identity, effect); err != nil {
			return fmt.Errorf("verify browser trace %s effect before receipt: %w", identity.Operation, err)
		}
		resultHash := sha256.Sum256(result)
		proofHash := sha256.Sum256(proof)
		_, err = conn.ExecContext(ctx,
			`INSERT INTO `+browserTraceStorageHostReceiptTable+` (op_id, operation, payload_sha256, writer_generation, effect_kind, effect_id, result_json, result_sha256, effect_json, effect_sha256) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			identity.OpID, identity.Operation, identity.PayloadSHA256, identity.WriterGeneration, string(effect.Kind), effect.ID,
			string(result), hex.EncodeToString(resultHash[:]), string(proof), hex.EncodeToString(proofHash[:]))
		if err != nil {
			return fmt.Errorf("write browser trace %s owner receipt: %w", identity.Operation, err)
		}
		return nil
	})
}

func validateBrowserTraceStorageHostReceiptIdentity(identity BrowserTraceStorageHostOperationIdentity, effect BrowserTraceStorageHostEffect) error {
	if err := identity.validate(); err != nil {
		return err
	}
	wantKind, ok := browserTraceEffectKindForOperation(identity.Operation)
	if !ok || effect.Kind != wantKind || effect.ID == "" {
		return errors.New("browser trace storage-host operation effect does not match operation")
	}
	return nil
}

func saveBrowserTraceStorageHostRow(ctx context.Context, conn *sql.Conn, kind BrowserTraceStorageHostEffectKind, item any) error {
	payload, err := json.Marshal(item)
	if err != nil {
		return err
	}
	var query string
	var args []any
	switch value := item.(type) {
	case domaintrace.TraceRun:
		if kind != BrowserTraceStorageHostTraceRunEffect {
			return errors.New("browser trace effect kind does not match trace run")
		}
		query = `INSERT OR REPLACE INTO browser_trace_run (run_id, created_at, payload) VALUES (?, ?, ?)`
		args = []any{string(value.RunID), value.CreatedAt.Format(timeFormatRFC3339Nano), string(payload)}
	case domaintrace.APICandidate:
		if kind != BrowserTraceStorageHostCandidateEffect {
			return errors.New("browser trace effect kind does not match candidate")
		}
		query = `INSERT OR REPLACE INTO api_candidate (candidate_id, run_id, created_at, payload) VALUES (?, ?, ?, ?)`
		args = []any{value.CandidateID, string(value.RunID), value.CreatedAt.Format(timeFormatRFC3339Nano), string(payload)}
	case domaintrace.APICandidateSchema:
		if kind != BrowserTraceStorageHostSchemaEffect {
			return errors.New("browser trace effect kind does not match schema")
		}
		query = `INSERT OR REPLACE INTO api_candidate_schema (schema_id, created_at, payload) VALUES (?, ?, ?)`
		args = []any{value.SchemaID, value.CreatedAt.Format(timeFormatRFC3339Nano), string(payload)}
	case domaintrace.APICandidateValidationResult:
		if kind != BrowserTraceStorageHostValidationEffect {
			return errors.New("browser trace effect kind does not match validation")
		}
		query = `INSERT OR REPLACE INTO api_candidate_validation (validation_id, candidate_id, run_id, created_at, payload) VALUES (?, ?, ?, ?, ?)`
		args = []any{value.ValidationID, value.CandidateID, string(value.RunID), value.CreatedAt.Format(timeFormatRFC3339Nano), string(payload)}
	case domaintrace.APICoverageReport:
		if kind != BrowserTraceStorageHostCoverageEffect {
			return errors.New("browser trace effect kind does not match coverage report")
		}
		query = `INSERT OR REPLACE INTO api_coverage_report (artifact_id, run_id, created_at, payload) VALUES (?, ?, ?, ?)`
		args = []any{string(value.ArtifactID), string(value.RunID), value.CreatedAt.Format(timeFormatRFC3339Nano), string(payload)}
	default:
		return errors.New("unsupported browser trace storage-host row type")
	}
	if _, err := conn.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("write browser trace %s row: %w", kind, err)
	}
	return nil
}

func readBrowserTraceStorageHostReceipt(ctx context.Context, querier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, opID string) (browserTraceStorageHostReceiptRow, bool, error) {
	var row browserTraceStorageHostReceiptRow
	var effectKind string
	var result, proof string
	err := querier.QueryRowContext(ctx,
		`SELECT operation, payload_sha256, writer_generation, effect_kind, effect_id, result_json, result_sha256, effect_json, effect_sha256 FROM `+browserTraceStorageHostReceiptTable+` WHERE op_id = ?`, opID).Scan(
		&row.Operation, &row.PayloadSHA256, &row.WriterGeneration, &effectKind, &row.Effect.ID, &result, &row.ResultSHA256, &proof, &row.ProofSHA256)
	if errors.Is(err, sql.ErrNoRows) {
		return browserTraceStorageHostReceiptRow{}, false, nil
	}
	if err != nil {
		return browserTraceStorageHostReceiptRow{}, false, fmt.Errorf("read browser trace owner receipt: %w", err)
	}
	row.Effect.Kind = BrowserTraceStorageHostEffectKind(effectKind)
	row.Result = []byte(result)
	row.Proof = []byte(proof)
	return row, true, nil
}

func validateBrowserTraceStorageHostReceipt(row browserTraceStorageHostReceiptRow, identity BrowserTraceStorageHostOperationIdentity, effect BrowserTraceStorageHostEffect) error {
	if row.Operation != identity.Operation || row.PayloadSHA256 != identity.PayloadSHA256 || row.WriterGeneration != identity.WriterGeneration || row.Effect != effect {
		return errors.New("browser trace storage-host operation receipt identity conflicts")
	}
	resultHash := sha256.Sum256(row.Result)
	proofHash := sha256.Sum256(row.Proof)
	if !bytes.Equal(row.Result, []byte("null")) || row.ResultSHA256 != hex.EncodeToString(resultHash[:]) ||
		row.ProofSHA256 != hex.EncodeToString(proofHash[:]) || len(row.Proof) == 0 {
		return errors.New("browser trace storage-host operation receipt proof is corrupt")
	}
	return nil
}

func validateBrowserTraceStorageHostEffectProof(ctx context.Context, conn *sql.Conn, raw []byte, identity BrowserTraceStorageHostOperationIdentity, effect BrowserTraceStorageHostEffect) error {
	var proof browserTraceStorageHostReceiptProof
	if err := decodeBrowserTraceJSON(raw, &proof); err != nil {
		return fmt.Errorf("decode owner effect proof: %w", err)
	}
	if proof.Version != 1 || proof.Operation != identity.Operation || proof.PayloadSHA256 != identity.PayloadSHA256 || proof.WriterGeneration != identity.WriterGeneration || proof.Effect != effect || len(proof.Payload) == 0 {
		return errors.New("owner effect proof does not match operation identity")
	}
	switch effect.Kind {
	case BrowserTraceStorageHostTraceRunEffect:
		var item domaintrace.TraceRun
		if err := decodeBrowserTraceJSON(proof.Payload, &item); err != nil || domaintrace.ValidateTraceRun(item) != nil || string(item.RunID) != effect.ID {
			return errors.New("trace run effect proof is invalid")
		}
		stored, found, err := findTraceRunForStorageHost(ctx, conn, item.RunID)
		if err != nil || !found || stored.TaskID != item.TaskID || stored.RunID != item.RunID || stored.ActorID != item.ActorID {
			return errors.New("trace run effect proof does not resolve to its stored identity")
		}
	case BrowserTraceStorageHostCandidateEffect:
		var item domaintrace.APICandidate
		if err := decodeBrowserTraceJSON(proof.Payload, &item); err != nil || domaintrace.ValidateAPICandidate(item) != nil || item.CandidateID != effect.ID {
			return errors.New("candidate effect proof is invalid")
		}
		stored, found, err := findAPICandidateOnConn(ctx, conn, item.CandidateID)
		if err != nil || !found || stored.TaskID != item.TaskID || stored.RunID != item.RunID || stored.ActorID != item.ActorID {
			return errors.New("candidate effect proof does not resolve to its stored identity")
		}
	case BrowserTraceStorageHostSchemaEffect:
		var item domaintrace.APICandidateSchema
		if err := decodeBrowserTraceJSON(proof.Payload, &item); err != nil || domaintrace.ValidateAPICandidateSchema(item) != nil || item.SchemaID != effect.ID {
			return errors.New("schema effect proof is invalid")
		}
		var payload string
		if err := conn.QueryRowContext(ctx, `SELECT payload FROM api_candidate_schema WHERE schema_id = ?`, effect.ID).Scan(&payload); err != nil {
			return fmt.Errorf("resolve schema effect proof: %w", err)
		}
		var stored domaintrace.APICandidateSchema
		if err := decodeBrowserTraceJSON([]byte(payload), &stored); err != nil || domaintrace.ValidateAPICandidateSchema(stored) != nil || stored.SchemaID != item.SchemaID || stored.CandidateID != item.CandidateID {
			return errors.New("schema effect proof does not resolve to its stored identity")
		}
	case BrowserTraceStorageHostValidationEffect:
		var item domaintrace.APICandidateValidationResult
		if err := decodeBrowserTraceJSON(proof.Payload, &item); err != nil || domaintrace.ValidateAPICandidateValidationResult(item) != nil || item.ValidationID != effect.ID {
			return errors.New("validation effect proof is invalid")
		}
		stored, found, err := findAPICandidateValidationForStorageHost(ctx, conn, item.ValidationID)
		if err != nil || !found || stored.CandidateID != item.CandidateID || stored.TaskID != item.TaskID || stored.RunID != item.RunID || stored.ActorID != item.ActorID {
			return errors.New("validation effect proof does not resolve to its stored identity")
		}
	case BrowserTraceStorageHostCoverageEffect:
		var item domaintrace.APICoverageReport
		if err := decodeBrowserTraceJSON(proof.Payload, &item); err != nil || domaintrace.ValidateAPICoverageReport(item) != nil || string(item.ArtifactID) != effect.ID {
			return errors.New("coverage effect proof is invalid")
		}
		var payload string
		if err := conn.QueryRowContext(ctx, `SELECT payload FROM api_coverage_report WHERE artifact_id = ?`, effect.ID).Scan(&payload); err != nil {
			return fmt.Errorf("resolve coverage effect proof: %w", err)
		}
		var stored domaintrace.APICoverageReport
		if err := decodeBrowserTraceJSON([]byte(payload), &stored); err != nil || domaintrace.ValidateAPICoverageReport(stored) != nil || stored.ArtifactID != item.ArtifactID || stored.TaskID != item.TaskID || stored.RunID != item.RunID || stored.ActorID != item.ActorID {
			return errors.New("coverage effect proof does not resolve to its stored identity")
		}
	case BrowserTraceStorageHostArtifactEffect:
		var item domaintrace.APIArtifact
		if err := decodeBrowserTraceJSON(proof.Payload, &item); err != nil || domaintrace.ValidateAPIArtifact(item) != nil || string(item.ArtifactID) != effect.ID {
			return errors.New("artifact effect proof is invalid")
		}
		stored, found, err := findAPIArtifactOnConn(ctx, conn, item.ArtifactID)
		if err != nil || !found || domaintrace.ValidateAPIArtifactScope(item, stored) != nil {
			return errors.New("artifact effect proof does not resolve to its stored identity")
		}
		if stored.SupersededBy == item.SupersededBy {
			break
		}
		if item.SupersededBy != "" || stored.SupersededBy == "" {
			return errors.New("artifact effect proof does not resolve to its stored supersession edge")
		}
		fact, factFound, err := findAPIArtifactSupersessionIntentOnConn(ctx, conn, item.ArtifactID)
		if err != nil || !factFound {
			return errors.New("artifact effect proof has no persisted fact for its later supersession edge")
		}
		successor, err := loadAPIArtifactOnConn(ctx, conn, stored.SupersededBy)
		if err != nil || domaintrace.ValidateAPIArtifactSupersessionFactRow(fact, stored, successor) != nil {
			return errors.New("artifact effect proof later supersession edge does not resolve to its persisted fact")
		}
	case BrowserTraceStorageHostArtifactCreationEffect:
		var payload browserTraceStorageHostCreationPayload
		if err := decodeBrowserTraceJSON(proof.Payload, &payload); err != nil || domaintrace.ValidateAPIArtifact(payload.Item) != nil || string(payload.Item.ArtifactID) != effect.ID {
			return errors.New("artifact creation effect proof is invalid")
		}
		var intent modulecore.EventEnvelope
		if err := decodeBrowserTraceJSON(payload.Intent, &intent); err != nil || domaintrace.ValidateAPIArtifactPublicationIntent(payload.Item, intent) != nil {
			return errors.New("artifact creation intent proof is invalid")
		}
		stored, found, err := findAPIArtifactOnConn(ctx, conn, payload.Item.ArtifactID)
		if err != nil || !found || domaintrace.ValidateAPIArtifactPublicationIntentRow(intent, stored) != nil {
			return errors.New("artifact creation proof does not resolve to its stored artifact")
		}
		storedIntent, found, err := findAPIArtifactPublicationIntentOnConn(ctx, conn, payload.Item.ArtifactID)
		if err != nil || !found || !bytes.Equal(mustBrowserTraceJSON(storedIntent), mustBrowserTraceJSON(intent)) {
			return errors.New("artifact creation proof does not resolve to its stored publication intent")
		}
	case BrowserTraceStorageHostArtifactSupersessionEffect:
		var payload browserTraceStorageHostSupersessionPayload
		if err := decodeBrowserTraceJSON(proof.Payload, &payload); err != nil || payload.PredecessorID.Validate() != nil || payload.SuccessorID.Validate() != nil || string(payload.PredecessorID) != effect.ID {
			return errors.New("artifact supersession effect proof is invalid")
		}
		var fact modulecore.EventEnvelope
		if err := decodeBrowserTraceJSON(payload.Fact, &fact); err != nil || domaintrace.ValidatePersistedAPIArtifactSupersessionFact(fact) != nil || fact.ArtifactID != payload.PredecessorID {
			return errors.New("artifact supersession fact proof is invalid")
		}
		predecessor, err := loadAPIArtifactOnConn(ctx, conn, payload.PredecessorID)
		if err != nil {
			return errors.New("artifact supersession predecessor proof does not resolve")
		}
		successor, err := loadAPIArtifactOnConn(ctx, conn, payload.SuccessorID)
		if err != nil || predecessor.SupersededBy != payload.SuccessorID || domaintrace.ValidateAPIArtifactSupersessionFactRow(fact, predecessor, successor) != nil {
			return errors.New("artifact supersession edge proof does not resolve")
		}
		storedFact, found, err := findAPIArtifactSupersessionIntentOnConn(ctx, conn, payload.PredecessorID)
		if err != nil || !found || !bytes.Equal(mustBrowserTraceJSON(storedFact), mustBrowserTraceJSON(fact)) {
			return errors.New("artifact supersession proof does not resolve to its stored fact")
		}
	default:
		return errors.New("unknown browser trace storage-host effect proof")
	}
	return nil
}

func (s *SQLiteStore) LookupBrowserTraceStorageHostOperationReceipt(ctx context.Context, identity BrowserTraceStorageHostOperationIdentity, effect BrowserTraceStorageHostEffect) (json.RawMessage, bool, error) {
	if err := validateBrowserTraceStorageHostReceiptIdentity(identity, effect); err != nil {
		return nil, false, err
	}
	if s == nil || s.db == nil {
		return nil, false, errors.New("browser trace sqlite store is closed")
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("reserve browser trace receipt connection: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, `BEGIN`); err != nil {
		return nil, false, fmt.Errorf("begin browser trace receipt read: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), artifactTransactionCleanupTimeout)
			defer cancel()
			_, _ = conn.ExecContext(cleanupCtx, `ROLLBACK`)
		}
	}()
	row, found, err := readBrowserTraceStorageHostReceipt(ctx, conn, identity.OpID)
	if err != nil {
		return nil, false, err
	}
	if found {
		if err := validateBrowserTraceStorageHostReceipt(row, identity, effect); err != nil {
			return nil, false, err
		}
		if err := validateBrowserTraceStorageHostEffectProof(ctx, conn, row.Proof, identity, effect); err != nil {
			return nil, false, err
		}
		if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
			return nil, false, fmt.Errorf("commit browser trace receipt read: %w", err)
		}
		committed = true
		return append(json.RawMessage(nil), row.Result...), true, nil
	}
	exists, err := browserTraceStorageHostEffectExists(ctx, conn, effect)
	if err != nil {
		return nil, false, err
	}
	if exists {
		return nil, false, errors.New("browser trace owner effect exists without its same-transaction operation receipt")
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return nil, false, fmt.Errorf("commit browser trace receipt read: %w", err)
	}
	committed = true
	return nil, false, nil
}

func browserTraceStorageHostEffectExists(ctx context.Context, conn *sql.Conn, effect BrowserTraceStorageHostEffect) (bool, error) {
	if effect.Kind == BrowserTraceStorageHostArtifactCreationEffect {
		var found int
		err := conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM api_artifact WHERE artifact_id = ?) OR EXISTS(SELECT 1 FROM `+publicationIntentTable+` WHERE artifact_id = ?)`, effect.ID, effect.ID).Scan(&found)
		return found != 0, err
	}
	if effect.Kind == BrowserTraceStorageHostArtifactSupersessionEffect {
		_, found, err := findAPIArtifactSupersessionIntentOnConn(ctx, conn, modulecore.ArtifactID(effect.ID))
		if err != nil || found {
			return found, err
		}
		predecessor, predecessorFound, err := findAPIArtifactOnConn(ctx, conn, modulecore.ArtifactID(effect.ID))
		if err != nil || !predecessorFound {
			return false, err
		}
		return predecessor.SupersededBy != "", nil
	}
	var table, column string
	switch effect.Kind {
	case BrowserTraceStorageHostTraceRunEffect:
		table, column = "browser_trace_run", "run_id"
	case BrowserTraceStorageHostCandidateEffect:
		table, column = "api_candidate", "candidate_id"
	case BrowserTraceStorageHostSchemaEffect:
		table, column = "api_candidate_schema", "schema_id"
	case BrowserTraceStorageHostValidationEffect:
		table, column = "api_candidate_validation", "validation_id"
	case BrowserTraceStorageHostCoverageEffect:
		table, column = "api_coverage_report", "artifact_id"
	case BrowserTraceStorageHostArtifactEffect:
		table, column = "api_artifact", "artifact_id"
	default:
		return false, errors.New("unknown browser trace storage-host effect kind")
	}
	var found int
	query := `SELECT EXISTS(SELECT 1 FROM ` + table + ` WHERE ` + column + ` = ?)`
	if err := conn.QueryRowContext(ctx, query, effect.ID).Scan(&found); err != nil {
		return false, err
	}
	return found != 0, nil
}

func findTraceRunForStorageHost(ctx context.Context, conn *sql.Conn, runID modulecore.RunID) (domaintrace.TraceRun, bool, error) {
	var payload string
	err := conn.QueryRowContext(ctx, `SELECT payload FROM browser_trace_run WHERE run_id = ?`, string(runID)).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return domaintrace.TraceRun{}, false, nil
	}
	if err != nil {
		return domaintrace.TraceRun{}, false, err
	}
	var item domaintrace.TraceRun
	if err := decodeBrowserTraceJSON([]byte(payload), &item); err != nil || domaintrace.ValidateTraceRun(item) != nil || item.RunID != runID {
		return domaintrace.TraceRun{}, false, errors.New("stored trace run is corrupt")
	}
	return item, true, nil
}

func findAPICandidateOnConn(ctx context.Context, conn *sql.Conn, candidateID string) (domaintrace.APICandidate, bool, error) {
	var payload string
	err := conn.QueryRowContext(ctx, `SELECT payload FROM api_candidate WHERE candidate_id = ?`, candidateID).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return domaintrace.APICandidate{}, false, nil
	}
	if err != nil {
		return domaintrace.APICandidate{}, false, err
	}
	var item domaintrace.APICandidate
	if err := decodeBrowserTraceJSON([]byte(payload), &item); err != nil || domaintrace.ValidateAPICandidate(item) != nil || item.CandidateID != candidateID {
		return domaintrace.APICandidate{}, false, errors.New("stored api candidate is corrupt")
	}
	return item, true, nil
}

func findAPICandidateValidationForStorageHost(ctx context.Context, conn *sql.Conn, validationID string) (domaintrace.APICandidateValidationResult, bool, error) {
	var payload string
	err := conn.QueryRowContext(ctx, `SELECT payload FROM api_candidate_validation WHERE validation_id = ?`, validationID).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return domaintrace.APICandidateValidationResult{}, false, nil
	}
	if err != nil {
		return domaintrace.APICandidateValidationResult{}, false, err
	}
	var item domaintrace.APICandidateValidationResult
	if err := decodeBrowserTraceJSON([]byte(payload), &item); err != nil || domaintrace.ValidateAPICandidateValidationResult(item) != nil || item.ValidationID != validationID {
		return domaintrace.APICandidateValidationResult{}, false, errors.New("stored api candidate validation is corrupt")
	}
	return item, true, nil
}

func decodeBrowserTraceJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("browser trace JSON contains trailing value")
		}
		return err
	}
	return nil
}

func mustBrowserTraceJSON(value any) []byte {
	encoded, _ := json.Marshal(value)
	return encoded
}
