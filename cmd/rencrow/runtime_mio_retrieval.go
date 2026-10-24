package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	domainagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	conversationpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	runtimeMioRetrievalMaxQueryBytes = 512
	runtimeMioRetrievalMaxItems      = 8
)

type runtimeMioRetrieval struct {
	glossary        runtimeGlossaryLookupExecutor
	catalog         domainconversation.CategoryRecallRegistry
	userMemoryStore *l1sqlite.L1SQLiteStore
	preparedRecall  *conversationpersistence.PreparedRecallRuntime
	now             func() time.Time
}

func newRuntimeMioRetrieval(glossary runtimeGlossaryLookupExecutor, catalog domainconversation.CategoryRecallRegistry) *runtimeMioRetrieval {
	return &runtimeMioRetrieval{glossary: glossary, catalog: catalog, now: func() time.Time { return time.Now().UTC() }}
}

func (r *runtimeMioRetrieval) withClock(now func() time.Time) *runtimeMioRetrieval {
	if r != nil && now != nil {
		r.now = now
	}
	return r
}

func (r *runtimeMioRetrieval) withPreparedUserMemoryOwner(store *l1sqlite.L1SQLiteStore, prepared *conversationpersistence.PreparedRecallRuntime) *runtimeMioRetrieval {
	if r != nil {
		r.userMemoryStore = store
		r.preparedRecall = prepared
	}
	return r
}

func (r *runtimeMioRetrieval) Retrieve(ctx context.Context, input domainconversation.TurnInput, request domainagent.MioRetrievalRequest) (domainagent.MioRetrievalResult, error) {
	unavailable := runtimeMioRetrievalResult(request.SourceKind, domainagent.MioRetrievalStatusUnavailable)
	if r == nil || ctx == nil {
		return unavailable, nil
	}
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.Canceled) {
			return unavailable, err
		}
		return unavailable, nil
	}
	query, ok := normalizeRuntimeMioRetrievalQuery(request.Query)
	if !ok {
		return unavailable, nil
	}
	userID, ok := runtimeMioRetrievalAuthenticatedUser(ctx)
	if !ok {
		return unavailable, nil
	}

	switch request.SourceKind {
	case domainagent.MioRetrievalSourceUserMemory:
		return r.retrieveUserMemory(ctx, input, request, query)
	case domainagent.MioRetrievalSourceGlossary:
		return r.retrieveGlossary(ctx, userID, query, request.SourceKind)
	case domainagent.MioRetrievalSourceCatalog:
		return r.retrieveCatalog(ctx, userID, query, request.SourceKind)
	default:
		// The remaining registered sources are not implemented here yet. They
		// stay typed and fail closed until their own read owners are wired.
		return unavailable, nil
	}
}

func (r *runtimeMioRetrieval) retrieveUserMemory(ctx context.Context, input domainconversation.TurnInput, request domainagent.MioRetrievalRequest, query string) (domainagent.MioRetrievalResult, error) {
	unavailable := runtimeMioRetrievalResult(request.SourceKind, domainagent.MioRetrievalStatusUnavailable)
	if r == nil || r.userMemoryStore == nil || r.preparedRecall == nil {
		return unavailable, nil
	}
	ownerID, ok := runtimeMioRetrievalDelegatedAuthenticatedUser(ctx)
	if !ok || !validRuntimeMioRetrievalTurnBinding(ctx, input, ownerID) {
		return unavailable, nil
	}
	before, ok := r.preparedRecall.CurrentCapability()
	if !ok || before.OwnerID() != ownerID || before.UserScope() != "user:"+ownerID || before.SourceVersion() == "" {
		return unavailable, nil
	}
	readRequest := domainmemory.UserMemoryChatReadRequest{
		OwnerID: ownerID, SessionID: modulecore.SessionID(input.SessionID()),
		TurnID: input.TurnID(), TraceID: input.TraceID(), RootTaskID: input.RootTaskID(),
		Persona: "mio", Query: query, References: append([]string(nil), request.References...),
		Limit: runtimeMioRetrievalMaxItems,
	}
	ownerResult, readErr := r.userMemoryStore.ReadUserMemoriesForChat(ctx, readRequest)
	after, current := r.preparedRecall.CurrentCapability()
	if !current || after.OwnerID() != before.OwnerID() || after.UserScope() != before.UserScope() || after.SourceVersion() != before.SourceVersion() {
		return unavailable, nil
	}
	if readErr != nil {
		if ctxErr := ctx.Err(); errors.Is(ctxErr, context.Canceled) {
			return unavailable, ctxErr
		}
		return unavailable, nil
	}
	if !runtimeMioUserMemoryReadMatches(readRequest, ownerResult) || len(ownerResult.Items) > runtimeMioRetrievalMaxItems {
		return unavailable, nil
	}
	qualified := domainagent.MioRetrievalResult{SourceKind: request.SourceKind, Qualification: before}
	qualified.Status = runtimeMioUserMemoryResultStatus(ownerResult)
	if qualified.Status == domainagent.MioRetrievalStatusNotFound || qualified.Status == domainagent.MioRetrievalStatusUnavailable {
		return qualified, nil
	}
	qualified.Items = make([]domainagent.MioRetrievalItem, 0, len(ownerResult.Items))
	qualified.Provenance = make([]domainagent.MioRetrievalProvenance, 0, len(ownerResult.Items))
	for index, recalled := range ownerResult.Items {
		text := recalled.Item.Statement
		if !runtimeMioUserMemoryEvidenceMatches(ownerID, readRequest.SessionID, recalled) || !validRuntimeMioUserMemoryText(text) {
			return unavailable, nil
		}
		qualified.Items = append(qualified.Items, domainagent.MioRetrievalItem{Text: text})
		qualified.Provenance = append(qualified.Provenance, domainagent.MioRetrievalProvenance{
			Reference:       cloneRuntimeMioUserMemoryReference(recalled.Reference),
			TurnID:          string(readRequest.TurnID),
			TraceID:         string(readRequest.TraceID),
			RootTaskID:      string(readRequest.RootTaskID),
			StatementSHA256: sha256.Sum256([]byte(text)),
		})
		if index >= runtimeMioRetrievalMaxItems {
			return unavailable, nil
		}
	}
	return qualified, nil
}

func runtimeMioRetrievalDelegatedAuthenticatedUser(ctx context.Context) (string, bool) {
	scope, found := domaintool.ToolExecutionScopeFromContext(ctx)
	if !found || scope.Validate() != nil || scope.ActorKind != domaintool.ActorKindAgent || scope.ActorID != "mio" ||
		scope.AuthenticationSource != domaintool.AuthenticationSourceAgentOrchestrator ||
		(scope.AgentRole != "chat" && scope.AgentRole != "conversation") || !scope.Allows(domaintool.DataScopeUser) || scope.Allows(domaintool.DataScopeInternal) {
		return "", false
	}
	proof, found := domaintool.AuthenticatedHTTPUserScopeFromContext(ctx)
	if !found || proof.Validate() != nil || proof.ActorKind != domaintool.ActorKindUser ||
		proof.AuthenticationSource != domaintool.AuthenticationSourceHTTP || proof.ActorID != proof.AuthenticatedUserID ||
		!proof.Allows(domaintool.DataScopeUser) || proof.AuthenticatedUserID != scope.AuthenticatedUserID {
		return "", false
	}
	return proof.AuthenticatedUserID, proof.AuthenticatedUserID != ""
}

func validRuntimeMioRetrievalTurnBinding(ctx context.Context, input domainconversation.TurnInput, ownerID string) bool {
	if input.Validate() != nil || ownerID == "" || strings.TrimSpace(input.SessionID()) == "" ||
		modulecore.SessionID(input.SessionID()).Validate() != nil {
		return false
	}
	binding, found := domainconversation.SessionBindingFromContext(ctx)
	return found && binding.ValidateForPublication() == nil && binding.Audience() == domainconversation.SessionAudiencePrivate &&
		binding.OwnerID() == ownerID && binding.BindingGeneration() != domainconversation.BindingGenerationUnavailable &&
		binding.SessionID() == modulecore.SessionID(input.SessionID()) && binding.ChannelAddress() == input.ChannelAddress()
}

func runtimeMioUserMemoryReadMatches(request domainmemory.UserMemoryChatReadRequest, result domainmemory.UserMemoryChatReadResult) bool {
	return result.OwnerID == request.OwnerID && result.SessionID == request.SessionID && result.TurnID == request.TurnID &&
		result.TraceID == request.TraceID && result.RootTaskID == request.RootTaskID && result.Persona == "mio" &&
		result.TotalCandidates >= len(result.Items) && result.TotalCandidates <= domainmemory.UserMemoryRecallMaxScan &&
		(!result.HasMore || result.TotalCandidates == domainmemory.UserMemoryRecallMaxScan) &&
		len(result.Items) <= request.Limit
}

func runtimeMioUserMemoryResultStatus(result domainmemory.UserMemoryChatReadResult) domainagent.MioRetrievalStatus {
	if len(result.Items) > 0 {
		if result.HasMore {
			return domainagent.MioRetrievalStatusPartial
		}
		return domainagent.MioRetrievalStatusFound
	}
	if result.HasMore {
		return domainagent.MioRetrievalStatusUnavailable
	}
	return domainagent.MioRetrievalStatusNotFound
}

func runtimeMioUserMemoryEvidenceMatches(ownerID string, sessionID modulecore.SessionID, recalled domainmemory.UserMemoryChatRecallItem) bool {
	item := recalled.Item
	reference := recalled.Reference
	if item.ID == "" || item.Statement == "" || len(item.EvidenceEventIDs) == 0 ||
		item.State != domainmemory.MemoryStateConfirmed && item.State != domainmemory.MemoryStatePinned ||
		item.Sensitivity != "normal" || !item.Active || strings.TrimSpace(item.SupersededBy) != "" {
		return false
	}
	if reference.OwnerID != ownerID || reference.SessionID != sessionID || reference.MemoryID != item.ID ||
		reference.Namespace != "user:"+ownerID || reference.SourceType != "user_memory" || strings.TrimSpace(reference.Source) == "" ||
		reference.State != item.State || reference.Sensitivity != item.Sensitivity || reference.PersonaScope != item.PersonaScope ||
		reference.Active != item.Active || reference.SupersededBy != item.SupersededBy || !reference.UpdatedAt.Equal(item.UpdatedAt) ||
		reference.OwnerVersion != reference.UpdatedAt.UTC().Format(time.RFC3339Nano) || len(reference.EvidenceEventIDs) != len(item.EvidenceEventIDs) {
		return false
	}
	for index := range reference.EvidenceEventIDs {
		if strings.TrimSpace(reference.EvidenceEventIDs[index]) == "" || reference.EvidenceEventIDs[index] != item.EvidenceEventIDs[index] {
			return false
		}
	}
	return true
}

func validRuntimeMioUserMemoryText(text string) bool {
	return text != "" && strings.TrimSpace(text) == text && utf8.ValidString(text) && len(text) <= domainconversation.PreparedRecallMaxIndexTextBytes
}

func cloneRuntimeMioUserMemoryReference(reference domainmemory.UserMemoryChatRecallSourceReference) domainmemory.UserMemoryChatRecallSourceReference {
	reference.EvidenceEventIDs = append([]string(nil), reference.EvidenceEventIDs...)
	return reference
}

func (r *runtimeMioRetrieval) retrieveGlossary(ctx context.Context, _ string, query string, source domainagent.MioRetrievalSourceKind) (domainagent.MioRetrievalResult, error) {
	unavailable := runtimeMioRetrievalResult(source, domainagent.MioRetrievalStatusUnavailable)
	if r.glossary == nil {
		return unavailable, nil
	}
	value, err := r.glossary.Lookup(ctx, "define_term", query, "", runtimeMioRetrievalMaxItems)
	if err != nil {
		if ctxErr := ctx.Err(); errors.Is(ctxErr, context.Canceled) {
			return unavailable, ctxErr
		}
		return unavailable, nil
	}
	lookup, err := runtimeGlossaryLookupResult(value)
	if err != nil || lookup.Operation != "define_term" {
		return unavailable, nil
	}
	if len(lookup.Items) == 0 {
		return runtimeMioRetrievalResult(source, domainagent.MioRetrievalStatusNotFound), nil
	}
	if len(lookup.Items) > runtimeMioRetrievalMaxItems {
		return unavailable, nil
	}
	for _, item := range lookup.Items {
		id := strings.TrimSpace(item.ID)
		term := strings.TrimSpace(item.Term)
		explanation := strings.TrimSpace(item.Explanation)
		sourceName := strings.TrimSpace(item.Source)
		category := strings.TrimSpace(item.Category)
		updatedAt := strings.TrimSpace(item.UpdatedAt)
		if id == "" || term == "" || explanation == "" || sourceName == "" || category == "" || updatedAt == "" || term != query {
			return unavailable, nil
		}
	}
	// Indexed Lookup currently returns no owner revision/revocation proof that
	// remains checkable through Mio's continuation and final publication. Keep
	// non-empty evidence out of the continuation until that owner contract exists.
	return unavailable, nil
}

func (r *runtimeMioRetrieval) retrieveCatalog(ctx context.Context, userID string, query string, source domainagent.MioRetrievalSourceKind) (domainagent.MioRetrievalResult, error) {
	unavailable := runtimeMioRetrievalResult(source, domainagent.MioRetrievalStatusUnavailable)
	if r.catalog == nil {
		return unavailable, nil
	}
	now := time.Now().UTC()
	if r.now != nil {
		now = r.now().UTC()
	}
	ownerQuery := domainconversation.CategoryRecallQuery{
		Message:   query,
		UserScope: userID,
		Time:      now,
		Limit:     runtimeMioRetrievalMaxItems,
	}
	ownerResult, err := r.catalog.Recall(ctx, ownerQuery)
	if err != nil {
		if ctxErr := ctx.Err(); errors.Is(ctxErr, context.Canceled) {
			return unavailable, ctxErr
		}
		return unavailable, nil
	}
	if ownerResult.Status != domainconversation.CategoryRecallStatusCompleted || len(ownerResult.Failures) != 0 {
		return unavailable, nil
	}
	if len(ownerResult.Records) == 0 {
		if len(ownerResult.SourcesQueried) == 0 {
			return unavailable, nil
		}
		return runtimeMioRetrievalResult(source, domainagent.MioRetrievalStatusNotFound), nil
	}
	if len(ownerResult.Records) > runtimeMioRetrievalMaxItems {
		return unavailable, nil
	}
	for _, record := range ownerResult.Records {
		if _, rejected := domainconversation.CheckCategoryRecallRecordForRole(record, ownerQuery, "chat"); rejected {
			return unavailable, nil
		}
		if strings.TrimSpace(record.ToPromptText()) == "" {
			return unavailable, nil
		}
	}
	// CategoryRecallResult carries no owner revision/revocation proof that can
	// be revalidated through Mio's continuation and final publication. The
	// registry read is still bounded and validated above, but its content cannot
	// cross this boundary until that proof is part of the owner contract.
	return unavailable, nil
}

func runtimeMioRetrievalAuthenticatedUser(ctx context.Context) (string, bool) {
	current, found := domaintool.ToolExecutionScopeFromContext(ctx)
	if !found || current.Validate() != nil || !current.Allows(domaintool.DataScopePublic) {
		return "", false
	}
	proof, found := domaintool.AuthenticatedHTTPUserScopeFromContext(ctx)
	if !found || proof.Validate() != nil || !proof.Allows(domaintool.DataScopeUser) {
		return "", false
	}
	userID := strings.TrimSpace(proof.AuthenticatedUserID)
	return userID, userID != ""
}

func normalizeRuntimeMioRetrievalQuery(query string) (string, bool) {
	if !utf8.ValidString(query) || len(query) > runtimeMioRetrievalMaxQueryBytes {
		return "", false
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return "", false
	}
	for _, char := range query {
		if unicode.IsControl(char) {
			return "", false
		}
	}
	return query, true
}

func runtimeMioRetrievalResult(source domainagent.MioRetrievalSourceKind, status domainagent.MioRetrievalStatus) domainagent.MioRetrievalResult {
	return domainagent.MioRetrievalResult{SourceKind: source, Status: status}
}
