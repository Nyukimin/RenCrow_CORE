package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	c "github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/contract"
	"github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/engine"
	"github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/localbackend"
)

const (
	researchStructuredTerminalSchema = "rencrow.research.structured-index-result.v1"
	researchStructuredResponseSchema = "rencrow.research.structured-index-operation.v1"
	researchStructuredBatchSchema    = "rencrow.research.structured-index-batch.v1"
	researchPDFRegisterSchema        = "rencrow.research.local-pdf-register.v1"
	researchPDFIndexPayloadSchema    = "rencrow.research.page-index-payload.v1"
	researchPDFMaxBytes              = 16 << 20
	researchOwnerMaxRequestBytes     = 23 << 20
	researchStructuredReadTimeout    = 10 * time.Second
	researchStructuredExcerptMax     = 500
)

type researchStructuredIndexOwner struct {
	config    *config.Config
	ownerID   string
	store     engine.Store
	tasks     *taskmanager.Manager
	pageIndex engine.PageIndexPort
	codeGraph engine.CodeGraphPort
}

type researchPDFRegisterRequest struct {
	SchemaVersion string `json:"schema_version"`
	DocumentRef   string `json:"document_ref"`
	Revision      string `json:"revision"`
	PDFBase64     string `json:"pdf_base64,omitempty"`
}

type researchStructuredIndexResult struct {
	SchemaVersion    string                        `json:"schema_version"`
	Kind             string                        `json:"kind"`
	Status           string                        `json:"status"`
	TaskID           string                        `json:"task_id"`
	RunID            string                        `json:"run_id"`
	OwnerID          string                        `json:"owner_id"`
	DataScope        string                        `json:"data_scope"`
	RequestDigest    string                        `json:"request_digest"`
	DocumentRef      string                        `json:"document_ref,omitempty"`
	RepositoryRef    string                        `json:"repository_ref,omitempty"`
	Revision         string                        `json:"revision"`
	OriginalSHA256   string                        `json:"original_sha256,omitempty"`
	RightsBasis      string                        `json:"rights_basis,omitempty"`
	RetentionAllowed bool                          `json:"retention_allowed,omitempty"`
	SourceArtifact   *c.ArtifactRef                `json:"source_artifact,omitempty"`
	IndexArtifact    *c.ArtifactRef                `json:"index_artifact,omitempty"`
	PageIndex        *researchPageIndexMetadata    `json:"page_index,omitempty"`
	CodeGraph        *engine.CodeGraphIndexReceipt `json:"code_graph,omitempty"`
	Project          string                        `json:"project,omitempty"`
	SnapshotHash     string                        `json:"snapshot_hash,omitempty"`
	ReadbackVerified bool                          `json:"readback_verified"`
	ErrorCode        string                        `json:"error_code,omitempty"`
}

type researchPageIndexMetadata struct {
	BackendVersion        string `json:"backend_version"`
	PackageVersion        string `json:"package_version"`
	SourceCommit          string `json:"source_commit"`
	RuntimeManifestSHA256 string `json:"runtime_manifest_sha256"`
	PDFParserVersion      string `json:"pdf_parser_version"`
	TreeSource            string `json:"tree_source"`
	PageCount             int    `json:"page_count"`
}

type researchPageIndexPayload struct {
	SchemaVersion  string                    `json:"schema_version"`
	DocumentRef    string                    `json:"document_ref"`
	Revision       string                    `json:"revision"`
	OriginalSHA256 string                    `json:"original_sha256"`
	DataScope      string                    `json:"data_scope"`
	OwnerID        string                    `json:"owner_id"`
	RightsBasis    string                    `json:"rights_basis"`
	PageIndex      researchPageIndexMetadata `json:"page_index"`
	Nodes          []engine.PageIndexNode    `json:"nodes"`
}

type researchStructuredOperationResponse struct {
	SchemaVersion string                         `json:"schema_version"`
	State         string                         `json:"state"`
	OperationKey  string                         `json:"operation_key"`
	RequestDigest string                         `json:"request_digest"`
	ErrorCode     string                         `json:"error_code,omitempty"`
	Admission     *c.IndexAdmissionReceipt       `json:"admission,omitempty"`
	Result        *researchStructuredIndexResult `json:"result,omitempty"`
}

type researchStructuredBatchResponse struct {
	SchemaVersion string                                `json:"schema_version"`
	State         string                                `json:"state"`
	RequestDigest string                                `json:"request_digest"`
	Operations    []researchStructuredOperationResponse `json:"operations"`
	Missing       []string                              `json:"missing"`
}

func (owner *researchStructuredIndexOwner) handleOperation(
	w http.ResponseWriter,
	r *http.Request,
	input researchOwnerRequest,
	caller c.Envelope,
	trustedOwner string,
) bool {
	switch input.Operation {
	case "source_register", "source_register_status":
		owner.handlePDFRegistration(w, r, input, caller, trustedOwner)
		return true
	case "page_index_build", "page_index_status":
		owner.handlePageIndexOperation(w, r, input, caller, trustedOwner)
		return true
	case "code_graph_build", "code_graph_status":
		owner.handleCodeGraphOperation(w, r, input, caller, trustedOwner)
		return true
	default:
		return false
	}
}

func (owner *researchStructuredIndexOwner) handlePDFRegistration(w http.ResponseWriter, r *http.Request, input researchOwnerRequest, caller c.Envelope, trustedOwner string) {
	if owner == nil || owner.config == nil || !owner.config.Research.StructuredIndex.PageIndex.Enabled || owner.store == nil || owner.tasks == nil {
		writeResearchStructuredFailure(w, http.StatusServiceUnavailable, "source_owner_unavailable")
		return
	}
	build := input.Operation == "source_register"
	if !structuredOwnerFieldsOnly(input, false) || len(input.Request) == 0 {
		writeResearchStructuredFailure(w, http.StatusBadRequest, "mixed_operation_fields")
		return
	}
	request, err := decodeResearchPDFRegisterRequest(input.Request)
	if err != nil || request.SchemaVersion != researchPDFRegisterSchema || request.DocumentRef == "" || request.Revision == "" ||
		build != (request.PDFBase64 != "") {
		writeResearchStructuredFailure(w, http.StatusBadRequest, "invalid_source_registration_request")
		return
	}
	trusted, ok := owner.trustedPDF(request.DocumentRef, request.Revision, caller, trustedOwner)
	if !ok || !trusted.RetentionAllowed {
		writeResearchStructuredFailure(w, http.StatusForbidden, "trusted_source_unavailable")
		return
	}
	digest, err := researchPDFRegistrationDigest(trusted)
	if err != nil {
		writeResearchStructuredFailure(w, http.StatusBadRequest, "invalid_source_registration")
		return
	}
	operationKey := researchStructuredOperationKey("pdf-source", trusted.DocumentRef, trusted.Revision, trusted.DataScope, trusted.OwnerID)
	if !build {
		response, found, lookupErr := owner.readStructuredOperation(r.Context(), trustedOwner, operationKey, digest)
		if lookupErr != nil {
			writeResearchStructuredFailure(w, http.StatusConflict, "source_receipt_unavailable")
			return
		}
		if !found {
			response = researchStructuredNotFound(operationKey, digest)
		}
		_ = json.NewEncoder(w).Encode(response)
		return
	}
	if owner.store == nil || owner.tasks == nil {
		writeResearchStructuredFailure(w, http.StatusServiceUnavailable, "source_owner_unavailable")
		return
	}
	pdf, err := decodeResearchPDFBase64(request.PDFBase64)
	if err != nil || researchSHA256(pdf) != trusted.OriginalSHA256 {
		writeResearchStructuredFailure(w, http.StatusBadRequest, "original_hash_mismatch")
		return
	}
	response, found, lookupErr := owner.readStructuredOperation(r.Context(), trustedOwner, operationKey, digest)
	if lookupErr != nil {
		writeResearchStructuredFailure(w, http.StatusConflict, "source_receipt_conflict_or_unavailable")
		return
	}
	if found {
		_ = json.NewEncoder(w).Encode(response)
		return
	}
	admission, created, err := owner.tasks.EnsureResearchAdmission(r.Context(), trustedOwner, operationKey, digest, caller.PolicyRevision, researchIndexAdmissionKind)
	if err != nil {
		writeResearchStructuredFailure(w, http.StatusConflict, "source_admission_unavailable")
		return
	}
	if !created {
		response, found, err = owner.readStructuredOperation(r.Context(), trustedOwner, operationKey, digest)
		if err != nil || !found {
			writeResearchStructuredPending(w, operationKey, digest, admission)
			return
		}
		_ = json.NewEncoder(w).Encode(response)
		return
	}
	if !researchIndexAdmissionIsRunning(r.Context(), owner.tasks, trustedOwner, admission) {
		writeResearchStructuredPending(w, operationKey, digest, admission)
		return
	}
	guard := newResearchStartedRunGuard(owner.tasks, admission.TaskID, admission.InitialRunID, trustedOwner, r.Context())
	defer guard.DeferFallback()
	result := owner.structuredResult("source_register", "failed", trustedOwner, digest, admission)
	result.DocumentRef, result.Revision = trusted.DocumentRef, trusted.Revision
	result.OriginalSHA256, result.RightsBasis, result.RetentionAllowed = trusted.OriginalSHA256, trusted.RightsBasis, trusted.RetentionAllowed
	var runErr error
	artifact, putErr := owner.store.Put(r.Context(), pdf)
	if putErr != nil {
		runErr = errors.New("store trusted PDF source")
		result.ErrorCode = "source_store_failed"
	} else {
		result.SourceArtifact = &artifact
		readback, readErr := owner.store.Get(r.Context(), artifact)
		if readErr != nil || artifact.Kind != "sha256" || artifact.Hash != strings.TrimPrefix(trusted.OriginalSHA256, "sha256:") ||
			artifact.Ref != trusted.OriginalSHA256 || !bytes.Equal(readback, pdf) || researchSHA256(readback) != trusted.OriginalSHA256 {
			runErr = errors.New("trusted PDF source exact readback failed")
			result.Status, result.ErrorCode = "partial", "source_readback_failed"
		} else {
			result.Status, result.ReadbackVerified = "registered", true
		}
	}
	if errors.Is(runErr, context.Canceled) {
		result.Status, result.ErrorCode = "cancelled", "cancelled"
	}
	if err := owner.closeStructuredOperation(guard, &result, runErr); err != nil {
		writeResearchStructuredPending(w, operationKey, digest, admission)
		return
	}
	response, found, err = owner.readStructuredOperation(context.WithoutCancel(r.Context()), trustedOwner, operationKey, digest)
	if err != nil || !found {
		writeResearchStructuredPending(w, operationKey, digest, admission)
		return
	}
	_ = json.NewEncoder(w).Encode(response)
}

func (owner *researchStructuredIndexOwner) handlePageIndexOperation(w http.ResponseWriter, r *http.Request, input researchOwnerRequest, caller c.Envelope, trustedOwner string) {
	if owner == nil || owner.config == nil || !owner.config.Research.StructuredIndex.PageIndex.Enabled || owner.pageIndex == nil {
		writeResearchStructuredFailure(w, http.StatusServiceUnavailable, "page_index_unavailable")
		return
	}
	if !structuredOwnerFieldsOnly(input, false) || len(input.Request) == 0 || len(input.Request) > 64*1024 {
		writeResearchStructuredFailure(w, http.StatusBadRequest, "mixed_operation_fields")
		return
	}
	request, err := c.DecodePageIndexBuildRequest(input.Request)
	if err != nil {
		writeResearchStructuredFailure(w, http.StatusBadRequest, "invalid_page_index_request")
		return
	}
	request, _, requestDigest, err := c.CanonicalizePageIndexBuildRequest(request)
	if err != nil {
		writeResearchStructuredFailure(w, http.StatusBadRequest, "invalid_page_index_request")
		return
	}
	build := input.Operation == "page_index_build"
	response := researchStructuredBatchResponse{
		SchemaVersion: researchStructuredBatchSchema,
		State:         "completed",
		RequestDigest: requestDigest,
		Operations:    make([]researchStructuredOperationResponse, 0, len(request.Documents)),
		Missing:       []string{},
	}
	operationCtx := r.Context()
	var cancel context.CancelFunc
	if build {
		operationCtx, cancel = context.WithTimeout(r.Context(), time.Duration(owner.config.Research.StructuredIndex.PageIndex.TimeoutSeconds)*time.Second)
		defer cancel()
	}
	for _, document := range request.Documents {
		trusted, ok := owner.trustedPDF(document.DocumentRef, document.Revision, caller, trustedOwner)
		if !ok || !trusted.RetentionAllowed || owner.pageIndex == nil || owner.store == nil || owner.tasks == nil {
			response.Operations = append(response.Operations, researchStructuredUnavailable(document.DocumentRef, document.Revision, "source_unavailable"))
			response.Missing = append(response.Missing, document.DocumentRef)
			continue
		}
		source, sourceState, sourceErr := owner.readTrustedPDF(operationCtx, trustedOwner, trusted)
		if sourceErr != nil || !source.ReadbackVerified {
			code := "source_unavailable"
			if sourceState == "pending" {
				code = "source_registration_pending"
			}
			response.Operations = append(response.Operations, researchStructuredUnavailable(document.DocumentRef, document.Revision, code))
			response.Missing = append(response.Missing, document.DocumentRef)
			continue
		}
		operationKey, digest, err := owner.pageIndexOperationIdentity(document, trusted)
		if err != nil {
			response.Operations = append(response.Operations, researchStructuredUnavailable(document.DocumentRef, document.Revision, "index_identity_invalid"))
			response.Missing = append(response.Missing, document.DocumentRef)
			continue
		}
		operation, found, operationErr := owner.pageIndexOperation(operationCtx, trustedOwner, caller, trusted, source, operationKey, digest, build)
		if operationErr != nil {
			operation = researchStructuredUnavailable(document.DocumentRef, document.Revision, "index_receipt_unavailable")
		}
		response.Operations = append(response.Operations, operation)
		if operation.State != "completed" || operation.Result == nil || operation.Result.Status != "indexed" {
			response.Missing = append(response.Missing, document.DocumentRef)
		}
		if operation.State == "pending" {
			response.State = "pending"
		} else if !found && operation.Result == nil && response.State == "completed" {
			response.State = "partial"
		}
	}
	if len(response.Missing) > 0 && response.State != "pending" {
		response.State = "partial"
	}
	_ = json.NewEncoder(w).Encode(response)
}

func (owner *researchStructuredIndexOwner) pageIndexOperation(ctx context.Context, trustedOwner string, caller c.Envelope, trusted config.ResearchTrustedPDFConfig, source researchTrustedPDF, operationKey, digest string, build bool) (researchStructuredOperationResponse, bool, error) {
	response, found, err := owner.readStructuredOperation(ctx, trustedOwner, operationKey, digest)
	if err != nil || found || !build {
		return response, found, err
	}
	admission, created, err := owner.tasks.EnsureResearchAdmission(ctx, trustedOwner, operationKey, digest, caller.PolicyRevision, researchIndexAdmissionKind)
	if err != nil {
		return researchStructuredOperationResponse{}, false, err
	}
	if !created {
		response, found, err = owner.readStructuredOperation(ctx, trustedOwner, operationKey, digest)
		if err != nil {
			return researchStructuredOperationResponse{}, false, err
		}
		if !found {
			return researchStructuredPending(operationKey, digest, admission), false, nil
		}
		return response, true, nil
	}
	if !researchIndexAdmissionIsRunning(ctx, owner.tasks, trustedOwner, admission) {
		return researchStructuredPending(operationKey, digest, admission), false, nil
	}
	guard := newResearchStartedRunGuard(owner.tasks, admission.TaskID, admission.InitialRunID, trustedOwner, ctx)
	defer guard.DeferFallback()
	result := owner.structuredResult("page_index", "failed", trustedOwner, digest, admission)
	result.DocumentRef, result.Revision = trusted.DocumentRef, trusted.Revision
	result.OriginalSHA256, result.RightsBasis, result.RetentionAllowed = trusted.OriginalSHA256, trusted.RightsBasis, trusted.RetentionAllowed
	result.SourceArtifact = source.SourceArtifact
	index, buildErr := owner.pageIndex.Build(ctx, source.rawPDF)
	if buildErr != nil {
		result.ErrorCode = "page_index_build_failed"
	} else if !validResearchPageIndexResult(index, owner.config.Research.StructuredIndex.PageIndex.RuntimeManifestSHA256) {
		buildErr = errors.New("PageIndex result did not match its trusted runtime identity")
		result.ErrorCode = "page_index_result_invalid"
	} else {
		payload := researchPageIndexPayload{
			SchemaVersion: researchPDFIndexPayloadSchema,
			DocumentRef:   trusted.DocumentRef, Revision: trusted.Revision,
			OriginalSHA256: trusted.OriginalSHA256, DataScope: trusted.DataScope,
			OwnerID: trusted.OwnerID, RightsBasis: trusted.RightsBasis,
			PageIndex: *researchPageIndexMetadataFromResult(index), Nodes: index.Nodes,
		}
		encoded, encodeErr := json.Marshal(payload)
		if encodeErr != nil {
			buildErr = errors.New("encode PageIndex artifact")
			result.ErrorCode = "page_index_artifact_failed"
		} else if artifact, putErr := owner.store.Put(ctx, encoded); putErr != nil {
			buildErr = errors.New("store PageIndex artifact")
			result.ErrorCode = "page_index_artifact_failed"
		} else {
			result.IndexArtifact = &artifact
			readback, readErr := owner.store.Get(ctx, artifact)
			if readErr != nil || !bytes.Equal(readback, encoded) || researchSHA256(readback) != "sha256:"+artifact.Hash {
				buildErr = errors.New("PageIndex artifact exact readback failed")
				result.Status, result.ErrorCode = "partial", "page_index_artifact_readback_failed"
			} else {
				result.PageIndex = researchPageIndexMetadataFromResult(index)
				result.Status, result.ReadbackVerified = "indexed", true
			}
		}
	}
	if errors.Is(buildErr, context.Canceled) {
		result.Status, result.ErrorCode = "cancelled", "cancelled"
	}
	if err := owner.closeStructuredOperation(guard, &result, buildErr); err != nil {
		return researchStructuredPending(operationKey, digest, admission), false, nil
	}
	readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), researchStructuredReadTimeout)
	defer cancel()
	response, found, err = owner.readStructuredOperation(readCtx, trustedOwner, operationKey, digest)
	if err != nil || !found {
		return researchStructuredPending(operationKey, digest, admission), false, err
	}
	return response, true, nil
}

type researchTrustedPDF struct {
	config.ResearchTrustedPDFConfig
	SourceArtifact   *c.ArtifactRef
	rawPDF           []byte
	ReadbackVerified bool
}

func (owner *researchStructuredIndexOwner) readTrustedPDF(ctx context.Context, trustedOwner string, trusted config.ResearchTrustedPDFConfig) (researchTrustedPDF, string, error) {
	result := researchTrustedPDF{ResearchTrustedPDFConfig: trusted}
	if owner == nil || owner.tasks == nil || owner.store == nil || !trusted.RetentionAllowed {
		return result, "unavailable", errors.New("trusted PDF source is unavailable")
	}
	digest, err := researchPDFRegistrationDigest(trusted)
	if err != nil {
		return result, "unavailable", err
	}
	key := researchStructuredOperationKey("pdf-source", trusted.DocumentRef, trusted.Revision, trusted.DataScope, trusted.OwnerID)
	response, found, err := owner.readStructuredOperation(ctx, trustedOwner, key, digest)
	if err != nil || !found {
		return result, "not_found", errors.New("trusted PDF registration receipt is missing")
	}
	if response.State == "pending" {
		return result, "pending", nil
	}
	if response.State != "completed" || response.Result == nil || response.Result.Kind != "source_register" ||
		response.Result.Status != "registered" || !response.Result.ReadbackVerified ||
		response.Result.DocumentRef != trusted.DocumentRef || response.Result.Revision != trusted.Revision ||
		response.Result.OwnerID != trusted.OwnerID || response.Result.DataScope != trusted.DataScope ||
		response.Result.OriginalSHA256 != trusted.OriginalSHA256 || response.Result.RightsBasis != trusted.RightsBasis ||
		response.Result.RetentionAllowed != trusted.RetentionAllowed || response.Result.SourceArtifact == nil {
		return result, "unavailable", errors.New("trusted PDF registration metadata does not match current owner policy")
	}
	artifact := *response.Result.SourceArtifact
	if artifact.Kind != "sha256" || artifact.Hash != strings.TrimPrefix(trusted.OriginalSHA256, "sha256:") || artifact.Ref != trusted.OriginalSHA256 {
		return result, "unavailable", errors.New("trusted PDF artifact identity does not match current owner policy")
	}
	pdf, err := owner.store.Get(ctx, artifact)
	if err != nil || len(pdf) == 0 || len(pdf) > researchPDFMaxBytes || researchSHA256(pdf) != trusted.OriginalSHA256 || !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		return result, "unavailable", errors.New("trusted PDF artifact exact readback is unavailable")
	}
	result.SourceArtifact = &artifact
	result.rawPDF, result.ReadbackVerified = pdf, true
	return result, "registered", nil
}

func (owner *researchStructuredIndexOwner) RecallPageIndexDocuments(ctx context.Context, envelope c.Envelope, request c.RecallRequest, query string, limit int) ([]c.Hit, bool, error) {
	if owner == nil || owner.pageIndex == nil || owner.store == nil || owner.tasks == nil || owner.config == nil || ctx == nil {
		return []c.Hit{}, true, errors.New("PageIndex owner is unavailable")
	}
	selected, unavailable := owner.selectTrustedPDFs(envelope, request.DocumentRefs)
	if len(selected) == 0 {
		return []c.Hit{}, unavailable, nil
	}
	if request.TimeWindow != nil {
		return []c.Hit{}, true, errors.New("PageIndex sources do not declare a time basis")
	}
	hits := make([]c.Hit, 0, min(limit, len(selected)))
	var recallErr error
	for _, trusted := range selected {
		if !trusted.RetentionAllowed {
			unavailable = true
			continue
		}
		source, _, err := owner.readTrustedPDF(ctx, envelope.OwnerID, trusted)
		if err != nil {
			unavailable = true
			recallErr = errors.Join(recallErr, err)
			continue
		}
		if !source.ReadbackVerified {
			unavailable = true
			continue
		}
		document := c.PageIndexDocumentRef{DocumentRef: trusted.DocumentRef, Revision: trusted.Revision}
		operationKey, digest, err := owner.pageIndexOperationIdentity(document, trusted)
		if err != nil {
			unavailable = true
			continue
		}
		operation, found, err := owner.readStructuredOperation(ctx, envelope.OwnerID, operationKey, digest)
		if err != nil || !found || operation.State != "completed" || operation.Result == nil ||
			operation.Result.Status != "indexed" || !operation.Result.ReadbackVerified || operation.Result.IndexArtifact == nil ||
			operation.Result.OriginalSHA256 != trusted.OriginalSHA256 || operation.Result.RightsBasis != trusted.RightsBasis ||
			operation.Result.RetentionAllowed != trusted.RetentionAllowed || !validResearchPageIndexMetadata(operation.Result.PageIndex, owner.config.Research.StructuredIndex.PageIndex.RuntimeManifestSHA256) {
			unavailable = true
			continue
		}
		artifactBytes, err := owner.store.Get(ctx, *operation.Result.IndexArtifact)
		if err != nil || len(artifactBytes) == 0 || len(artifactBytes) > 16<<20 ||
			researchSHA256(artifactBytes) != "sha256:"+operation.Result.IndexArtifact.Hash ||
			!validResearchStoredArtifact(*operation.Result.IndexArtifact, "sha256:"+operation.Result.IndexArtifact.Hash) {
			unavailable = true
			continue
		}
		var payload researchPageIndexPayload
		if decodeResearchStructuredJSON(artifactBytes, &payload) != nil || payload.SchemaVersion != researchPDFIndexPayloadSchema ||
			payload.DocumentRef != trusted.DocumentRef || payload.Revision != trusted.Revision || payload.OriginalSHA256 != trusted.OriginalSHA256 ||
			payload.DataScope != trusted.DataScope || payload.OwnerID != trusted.OwnerID || payload.RightsBasis != trusted.RightsBasis ||
			payload.PageIndex != *operation.Result.PageIndex || !validResearchPageIndexNodes(payload.Nodes, payload.PageIndex.PageCount) {
			unavailable = true
			continue
		}
		canonicalPayload, marshalErr := json.Marshal(payload)
		if marshalErr != nil || !bytes.Equal(canonicalPayload, artifactBytes) {
			unavailable = true
			continue
		}
		hit, ok, readErr := owner.readPageIndexEvidence(ctx, trusted, source.rawPDF, payload, query)
		if readErr != nil {
			unavailable = true
			continue
		}
		if ok {
			hits = append(hits, hit)
			if len(hits) >= limit {
				break
			}
		}
	}
	return hits, unavailable, recallErr
}

func (owner *researchStructuredIndexOwner) selectTrustedPDFs(envelope c.Envelope, refs []string) ([]config.ResearchTrustedPDFConfig, bool) {
	if owner == nil || owner.config == nil {
		return nil, true
	}
	configured := owner.config.Research.StructuredIndex.TrustedPDFs
	selected := make([]config.ResearchTrustedPDFConfig, 0, len(configured))
	unavailable := false
	if len(refs) == 0 {
		for _, document := range configured {
			if document.DataScope == envelope.DataScope && document.OwnerID == envelope.OwnerID {
				selected = append(selected, document)
			}
		}
	} else {
		for _, requested := range refs {
			matched := false
			for _, document := range configured {
				if document.DataScope != envelope.DataScope || document.OwnerID != envelope.OwnerID {
					if requested == document.DocumentRef || strings.HasPrefix(requested, document.DocumentRef+"@") {
						unavailable = true
					}
					continue
				}
				if requested == document.DocumentRef || requested == document.DocumentRef+"@"+document.Revision {
					selected = append(selected, document)
					matched = true
				}
				if requested == document.DocumentRef+"@"+document.Revision || strings.HasPrefix(requested, document.DocumentRef+"@") {
					matched = true
					if requested != document.DocumentRef+"@"+document.Revision {
						unavailable = true
					}
				}
			}
			if !matched {
				// Non-PDF document refs remain owned by the existing L1 recall path.
				continue
			}
		}
	}
	sort.Slice(selected, func(i, j int) bool {
		if selected[i].DocumentRef == selected[j].DocumentRef {
			return selected[i].Revision < selected[j].Revision
		}
		return selected[i].DocumentRef < selected[j].DocumentRef
	})
	if len(selected) > c.IndexMaxDocuments {
		selected = selected[:c.IndexMaxDocuments]
		unavailable = true
	}
	return selected, unavailable
}

func (owner *researchStructuredIndexOwner) readPageIndexEvidence(ctx context.Context, trusted config.ResearchTrustedPDFConfig, pdf []byte, payload researchPageIndexPayload, query string) (c.Hit, bool, error) {
	node, ok := bestResearchPageIndexNode(payload.Nodes, query)
	if !ok {
		return c.Hit{}, false, nil
	}
	endPage := min(node.EndPage, node.StartPage+31)
	pages, err := owner.pageIndex.ReadPageText(ctx, pdf, node.StartPage, endPage)
	if err != nil || len(pages) != endPage-node.StartPage+1 {
		return c.Hit{}, false, errors.New("PageIndex page text readback failed")
	}
	for _, page := range pages {
		if page.Page < node.StartPage || page.Page > endPage || !utf8.ValidString(page.Text) || strings.TrimSpace(page.Text) == "" {
			return c.Hit{}, false, errors.New("PageIndex page text readback is invalid")
		}
	}
	page := pages[0]
	start, finish, exact := 0, 0, false
	for _, candidate := range pages {
		if candidateStart, candidateEnd, matched := researchPageIndexRuneRange(candidate.Text, query); matched {
			page, start, finish, exact = candidate, candidateStart, candidateEnd, true
			break
		}
	}
	if !exact {
		start, finish = 0, len([]rune(page.Text))
	}
	locator := fmt.Sprintf("pageindex://%s@%s/node/%s/pages/%d-%d", trusted.DocumentRef, trusted.Revision, node.NodeID, node.StartPage, endPage)
	return c.Hit{
		Target: "documents", ProviderID: "core.pageindex." + payload.PageIndex.BackendVersion,
		CanonicalRef: trusted.DocumentRef, Revision: trusted.Revision, Locator: locator,
		MatchKind: "pageindex_section", EvidenceState: "acquired", Title: node.Title,
		Excerpt:  researchStructuredExcerpt(page.Text, start, finish, researchStructuredExcerptMax),
		Evidence: &c.EvidenceRef{DocumentID: trusted.DocumentRef + "@" + trusted.Revision, ArtifactHash: strings.TrimPrefix(trusted.OriginalSHA256, "sha256:"), Start: page.Page, End: page.Page, Locator: locator},
	}, true, nil
}

func researchPageIndexRuneRange(text, query string) (int, int, bool) {
	byteStart := strings.Index(text, query)
	if byteStart < 0 || !utf8.ValidString(text) || query == "" {
		return 0, 0, false
	}
	start := len([]rune(text[:byteStart]))
	return start, start + len([]rune(query)), true
}

func researchStructuredExcerpt(text string, start, end, limit int) string {
	runes := []rune(text)
	left, right := start-80, end+80
	if left < 0 {
		left = 0
	}
	if right > len(runes) {
		right = len(runes)
	}
	if right-left > limit {
		right = left + limit
	}
	return string(runes[left:right])
}

func (owner *researchStructuredIndexOwner) pageIndexOperationIdentity(document c.PageIndexDocumentRef, trusted config.ResearchTrustedPDFConfig) (string, string, error) {
	request := c.PageIndexBuildRequest{SchemaVersion: c.PageIndexRequestSchema, Documents: []c.PageIndexDocumentRef{document}}
	_, _, requestDigest, err := c.CanonicalizePageIndexBuildRequest(request)
	if err != nil {
		return "", "", err
	}
	pageConfig := owner.config.Research.StructuredIndex.PageIndex
	identity, err := json.Marshal(struct {
		Kind                  string `json:"kind"`
		RequestDigest         string `json:"request_digest"`
		OriginalSHA256        string `json:"original_sha256"`
		DataScope             string `json:"data_scope"`
		OwnerID               string `json:"owner_id"`
		RightsBasis           string `json:"rights_basis"`
		RetentionAllowed      bool   `json:"retention_allowed"`
		RuntimeManifestSHA256 string `json:"runtime_manifest_sha256"`
	}{"page_index", requestDigest, trusted.OriginalSHA256, trusted.DataScope, trusted.OwnerID, trusted.RightsBasis, trusted.RetentionAllowed, pageConfig.RuntimeManifestSHA256})
	if err != nil {
		return "", "", err
	}
	digest := researchDigest(identity)
	key := researchStructuredOperationKey("page-index", trusted.DocumentRef, trusted.Revision, trusted.DataScope, trusted.OwnerID, digest)
	return key, digest, nil
}

func (owner *researchStructuredIndexOwner) trustedPDF(ref, revision string, envelope c.Envelope, trustedOwner string) (config.ResearchTrustedPDFConfig, bool) {
	if owner == nil || owner.config == nil || ref == "" || revision == "" || envelope.DataScope != "user" || envelope.OwnerID != trustedOwner || trustedOwner != owner.ownerID {
		return config.ResearchTrustedPDFConfig{}, false
	}
	var matched config.ResearchTrustedPDFConfig
	found := false
	for _, candidate := range owner.config.Research.StructuredIndex.TrustedPDFs {
		if candidate.DocumentRef != ref || candidate.Revision != revision || candidate.OwnerID != trustedOwner || candidate.DataScope != envelope.DataScope {
			continue
		}
		if found {
			return config.ResearchTrustedPDFConfig{}, false
		}
		matched, found = candidate, true
	}
	return matched, found
}

func researchPDFRegistrationDigest(trusted config.ResearchTrustedPDFConfig) (string, error) {
	canonical, err := json.Marshal(struct {
		SchemaVersion    string `json:"schema_version"`
		DocumentRef      string `json:"document_ref"`
		Revision         string `json:"revision"`
		OriginalSHA256   string `json:"original_sha256"`
		DataScope        string `json:"data_scope"`
		OwnerID          string `json:"owner_id"`
		RightsBasis      string `json:"rights_basis"`
		RetentionAllowed bool   `json:"retention_allowed"`
	}{researchPDFRegisterSchema, trusted.DocumentRef, trusted.Revision, trusted.OriginalSHA256, trusted.DataScope, trusted.OwnerID, trusted.RightsBasis, trusted.RetentionAllowed})
	if err != nil {
		return "", err
	}
	return researchDigest(canonical), nil
}

func researchStructuredOperationKey(kind string, parts ...string) string {
	joined := strings.Join(append([]string{kind}, parts...), "\x00")
	sum := sha256.Sum256([]byte(joined))
	return "core-" + kind + "-" + hex.EncodeToString(sum[:])
}

func researchDigest(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func researchSHA256(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func decodeResearchPDFRegisterRequest(raw []byte) (researchPDFRegisterRequest, error) {
	var request researchPDFRegisterRequest
	if err := decodeResearchStructuredJSON(raw, &request); err != nil {
		return researchPDFRegisterRequest{}, err
	}
	return request, nil
}

func decodeResearchPDFBase64(value string) ([]byte, error) {
	if value == "" || len(value) > base64.StdEncoding.EncodedLen(researchPDFMaxBytes) {
		return nil, errors.New("PDF bytes exceed the trusted source limit")
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(value)
	if err != nil || len(decoded) == 0 || len(decoded) > researchPDFMaxBytes || !bytes.HasPrefix(decoded, []byte("%PDF-")) || base64.StdEncoding.EncodeToString(decoded) != value {
		return nil, errors.New("trusted source is not a bounded canonical PDF payload")
	}
	return decoded, nil
}

func decodeResearchStructuredJSON(raw []byte, target any) error {
	if !utf8.Valid(raw) {
		return errors.New("structured JSON must be UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON")
	}
	return nil
}

func validResearchPageIndexResult(result engine.PageIndexResult, manifest string) bool {
	return result.BackendVersion == localbackend.PageIndexBackendVersion && result.PackageVersion == localbackend.PageIndexPackageVersion &&
		result.SourceCommit == localbackend.PageIndexSourceCommit && result.RuntimeManifestSHA256 == manifest &&
		result.PDFParserVersion == localbackend.PageIndexParserVersion && result.PageCount > 0 && result.PageCount <= 5000 &&
		result.NetworkAttempts == 0 && validResearchPageIndexNodes(result.Nodes, result.PageCount)
}

func researchPageIndexMetadataFromResult(result engine.PageIndexResult) *researchPageIndexMetadata {
	return &researchPageIndexMetadata{
		BackendVersion: result.BackendVersion, PackageVersion: result.PackageVersion,
		SourceCommit: result.SourceCommit, RuntimeManifestSHA256: result.RuntimeManifestSHA256,
		PDFParserVersion: result.PDFParserVersion, TreeSource: result.TreeSource, PageCount: result.PageCount,
	}
}

func validResearchPageIndexMetadata(metadata *researchPageIndexMetadata, manifest string) bool {
	return metadata != nil && metadata.BackendVersion == localbackend.PageIndexBackendVersion &&
		metadata.PackageVersion == localbackend.PageIndexPackageVersion && metadata.SourceCommit == localbackend.PageIndexSourceCommit &&
		metadata.RuntimeManifestSHA256 == manifest && metadata.PDFParserVersion == localbackend.PageIndexParserVersion &&
		metadata.PageCount > 0 && metadata.PageCount <= 5000 && metadata.TreeSource != ""
}

func validResearchPageIndexNodes(nodes []engine.PageIndexNode, pageCount int) bool {
	if pageCount < 1 || pageCount > 5000 || len(nodes) == 0 || len(nodes) > 10000 {
		return false
	}
	seen := make(map[string]struct{}, len(nodes))
	var visit func([]engine.PageIndexNode, int) bool
	visit = func(items []engine.PageIndexNode, depth int) bool {
		if depth > 32 {
			return false
		}
		for _, node := range items {
			if node.NodeID == "" || node.Title == "" || node.StartPage < 1 || node.EndPage < node.StartPage || node.EndPage > pageCount {
				return false
			}
			if _, duplicate := seen[node.NodeID]; duplicate {
				return false
			}
			seen[node.NodeID] = struct{}{}
			if !visit(node.Children, depth+1) {
				return false
			}
		}
		return true
	}
	return visit(nodes, 1) && len(seen) <= 10000
}

func bestResearchPageIndexNode(nodes []engine.PageIndexNode, query string) (engine.PageIndexNode, bool) {
	queryTokens := researchQueryTokens(query)
	if len(queryTokens) == 0 {
		return engine.PageIndexNode{}, false
	}
	var best engine.PageIndexNode
	bestScore := 0
	var visit func([]engine.PageIndexNode)
	visit = func(items []engine.PageIndexNode) {
		for _, node := range items {
			text := strings.ToLower(node.Title + " " + strings.Join(node.KeyItems, " "))
			score := 0
			if strings.Contains(text, strings.ToLower(strings.TrimSpace(query))) {
				score += 100
			}
			for _, token := range queryTokens {
				if strings.Contains(text, token) {
					score += 10
				}
			}
			if score > bestScore || score == bestScore && score > 0 && node.EndPage-node.StartPage < best.EndPage-best.StartPage {
				best, bestScore = node, score
			}
			visit(node.Children)
		}
	}
	visit(nodes)
	return best, bestScore > 0
}

func researchQueryTokens(query string) []string {
	seen := make(map[string]struct{})
	var tokens []string
	for _, token := range strings.Fields(strings.ToLower(query)) {
		token = strings.Trim(token, " \t\r\n.,;:!?()[]{}\"'")
		if len([]rune(token)) < 2 {
			continue
		}
		if _, duplicate := seen[token]; duplicate {
			continue
		}
		seen[token] = struct{}{}
		tokens = append(tokens, token)
	}
	return tokens
}

func researchStructuredUnavailable(ref, revision, code string) researchStructuredOperationResponse {
	return researchStructuredOperationResponse{
		SchemaVersion: researchStructuredResponseSchema, State: "unavailable",
		OperationKey: "", RequestDigest: "", ErrorCode: code,
		Result: &researchStructuredIndexResult{SchemaVersion: researchStructuredTerminalSchema, Status: "failed", DocumentRef: ref, Revision: revision, ErrorCode: code},
	}
}

func researchStructuredNotFound(operationKey, digest string) researchStructuredOperationResponse {
	return researchStructuredOperationResponse{SchemaVersion: researchStructuredResponseSchema, State: "not_found", OperationKey: operationKey, RequestDigest: digest}
}

func researchStructuredPending(operationKey, digest string, admission taskmanager.ResearchAdmissionReceipt) researchStructuredOperationResponse {
	return researchStructuredOperationResponse{
		SchemaVersion: researchStructuredResponseSchema, State: "pending", OperationKey: operationKey,
		RequestDigest: digest, ErrorCode: "outcome_unknown", Admission: indexAdmissionReceipt(admission),
	}
}

func writeResearchStructuredPending(w http.ResponseWriter, operationKey, digest string, admission taskmanager.ResearchAdmissionReceipt) {
	_ = json.NewEncoder(w).Encode(researchStructuredPending(operationKey, digest, admission))
}

func writeResearchStructuredFailure(w http.ResponseWriter, status int, code string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "failed", "error": code})
}

func structuredOwnerFieldsOnly(input researchOwnerRequest, allowOperationKey bool) bool {
	return (allowOperationKey || input.OperationKey == "") && input.RunID == "" && input.WatchID == "" && input.URL == "" && input.Origin == ""
}

func (owner *researchStructuredIndexOwner) structuredResult(kind, status, trustedOwner, digest string, admission taskmanager.ResearchAdmissionReceipt) researchStructuredIndexResult {
	return researchStructuredIndexResult{
		SchemaVersion: researchStructuredTerminalSchema, Kind: kind, Status: status,
		TaskID: string(admission.TaskID), RunID: string(admission.InitialRunID),
		OwnerID: trustedOwner, DataScope: "user", RequestDigest: digest,
	}
}

func (owner *researchStructuredIndexOwner) closeStructuredOperation(guard *researchStartedRunGuard, result *researchStructuredIndexResult, cause error) error {
	if guard == nil {
		return errors.New("structured index Task Run owner is unavailable")
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return errors.Join(cause, errors.New("encode structured index receipt"))
	}
	taskStatus := domaintask.StatusFailed
	if result.Status == "registered" || result.Status == "indexed" {
		taskStatus = domaintask.StatusSucceeded
	} else if result.Status == "cancelled" {
		taskStatus = domaintask.StatusCancelled
	}
	closeErr := guard.Close(taskStatus, string(encoded), "", cause)
	if guard.closed {
		return nil
	}
	if closeErr != nil {
		return closeErr
	}
	return errors.New("structured index Task Run did not close")
}

func (owner *researchStructuredIndexOwner) readStructuredOperation(ctx context.Context, trustedOwner, operationKey, digest string) (researchStructuredOperationResponse, bool, error) {
	if owner == nil || owner.tasks == nil {
		return researchStructuredOperationResponse{}, false, errors.New("structured index Task owner is unavailable")
	}
	response := researchStructuredNotFound(operationKey, digest)
	admission, found, err := owner.tasks.LookupResearchAdmission(ctx, trustedOwner, operationKey, digest, researchIndexAdmissionKind)
	if err != nil || !found {
		return response, found, err
	}
	response = researchStructuredPending(operationKey, digest, admission)
	task, err := owner.tasks.Get(ctx, admission.TaskID)
	if err != nil || !validResearchIndexTask(task, trustedOwner, admission) {
		return response, true, nil
	}
	run, err := owner.tasks.GetRun(ctx, admission.InitialRunID)
	if err != nil || !validResearchIndexRun(run, trustedOwner, admission) || run.Status == domaintask.RunStatusRunning || run.CompletedAt == nil {
		return response, true, nil
	}
	var result researchStructuredIndexResult
	if err := decodeResearchStructuredJSON([]byte(run.Summary), &result); err != nil {
		return response, true, nil
	}
	canonical, err := json.Marshal(result)
	if err != nil || string(canonical) != run.Summary || !validResearchStructuredResult(result, trustedOwner, digest, admission) ||
		!researchIndexTaskRunStatusMatches(task.Status, run.Status, structuredTaskIndexStatus(result.Status)) || task.Summary != run.Summary {
		return response, true, nil
	}
	response.State, response.ErrorCode, response.Result = "completed", "", &result
	return response, true, nil
}

func validResearchStructuredResult(result researchStructuredIndexResult, trustedOwner, digest string, admission taskmanager.ResearchAdmissionReceipt) bool {
	if result.SchemaVersion != researchStructuredTerminalSchema || result.TaskID != string(admission.TaskID) ||
		result.RunID != string(admission.InitialRunID) || result.OwnerID != trustedOwner || result.DataScope != "user" ||
		result.RequestDigest != digest || result.Revision == "" ||
		(result.Status != "registered" && result.Status != "indexed" && result.Status != "partial" && result.Status != "failed" && result.Status != "cancelled") ||
		result.ReadbackVerified != (result.Status == "registered" || result.Status == "indexed") ||
		(result.Status != "registered" && result.Status != "indexed" && result.ErrorCode == "") {
		return false
	}
	switch result.Kind {
	case "source_register":
		if result.DocumentRef == "" || result.OriginalSHA256 == "" || result.RightsBasis == "" || !result.RetentionAllowed {
			return false
		}
		if result.Status == "registered" {
			return result.SourceArtifact != nil && validResearchStoredArtifact(*result.SourceArtifact, result.OriginalSHA256)
		}
		return result.SourceArtifact == nil || validResearchStoredArtifact(*result.SourceArtifact, result.OriginalSHA256)
	case "page_index":
		if result.DocumentRef == "" || result.OriginalSHA256 == "" || result.SourceArtifact == nil || !validResearchStoredArtifact(*result.SourceArtifact, result.OriginalSHA256) {
			return false
		}
		if result.Status == "indexed" {
			return result.IndexArtifact != nil && result.PageIndex != nil && result.ErrorCode == "" &&
				validResearchStoredArtifact(*result.IndexArtifact, "sha256:"+result.IndexArtifact.Hash) &&
				validResearchPageIndexMetadata(result.PageIndex, result.PageIndex.RuntimeManifestSHA256)
		}
		if result.IndexArtifact != nil && !validResearchStoredArtifact(*result.IndexArtifact, "sha256:"+result.IndexArtifact.Hash) {
			return false
		}
		return result.PageIndex == nil || validResearchPageIndexMetadata(result.PageIndex, result.PageIndex.RuntimeManifestSHA256)
	case "code_graph":
		if result.RepositoryRef == "" {
			return false
		}
		if result.Status == "indexed" {
			return result.Project != "" && result.SnapshotHash != "" && result.CodeGraph != nil &&
				result.CodeGraph.Status == engine.CodeGraphIndexIndexed && result.CodeGraph.Project == result.Project &&
				result.CodeGraph.SourceHash == result.SnapshotHash && result.ErrorCode == ""
		}
		return result.CodeGraph == nil || result.CodeGraph.Status == engine.CodeGraphIndexDegraded || result.CodeGraph.Status == engine.CodeGraphIndexIndexed
	default:
		return false
	}
}

func validResearchStoredArtifact(artifact c.ArtifactRef, hash string) bool {
	if artifact.Kind != "sha256" || artifact.Ref != "sha256:"+artifact.Hash || artifact.Hash != strings.TrimPrefix(hash, "sha256:") || len(artifact.Hash) != 64 {
		return false
	}
	for _, char := range artifact.Hash {
		if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f') {
			return false
		}
	}
	return true
}

func structuredTaskIndexStatus(resultStatus string) string {
	switch resultStatus {
	case "registered", "indexed":
		return "succeeded"
	case "cancelled":
		return "cancelled"
	case "partial":
		return "partial"
	default:
		return "failed"
	}
}
