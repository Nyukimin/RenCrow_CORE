package task

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func validResearchProjection(task Task) ResearchResultProjection {
	return ResearchResultProjection{
		SchemaVersion: ResearchResultSchema, TaskID: task.TaskID, ExecutionRunID: modulecore.NewRunID(),
		ResearchRunID: "research-run-1", ActorID: "mio", DataScope: "public",
		ResultDigest: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Status:       "succeeded", Counts: ResearchResultCounts{BoardPostsCreated: 0},
		ReceiptCount: 1, ReceiptState: "verified", RequiredReceiptsComplete: true,
		VerifiedReceiptRefs: []ResearchVerifiedReceipt{{Owner: "board", Route: "board", CanonicalID: "post-1",
			Digest: "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789", VerifiedAt: time.Date(2026, 11, 28, 1, 0, 0, 0, time.UTC)}},
	}
}

func TestResearchResultTaskSummaryIsDeterministicAndContainsOnlyMachineFacts(t *testing.T) {
	projection := ResearchResultProjection{
		Status: "partial", Counts: ResearchResultCounts{DocumentsFetched: 2, DocumentsAnalyzed: 1, RecordsSaved: 1, BoardPostsCreated: 0},
		ReceiptCount: 2, ReceiptState: "partially_verified", RequiredReceiptsComplete: false,
		Missing: []string{"save:knowledge_staging"}, ArtifactRef: "research-result:opaque",
		VerifiedReceiptRefs: []ResearchVerifiedReceipt{{Route: "knowledge_staging", CanonicalID: "record-123", Digest: "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"}},
	}
	summary := projection.TaskSummary()
	for _, expected := range []string{
		"Research status=partial", "sources_fetched=2", "documents_analyzed=1", "records_saved=1", "board_posts_created=0",
		"receipt_state=partially_verified", "verified_receipts=1/2", "required_receipts_complete=false",
		`"save:knowledge_staging"`, `"research-result:opaque"`, `"record-123"`,
	} {
		if !strings.Contains(summary, expected) {
			t.Errorf("Research summary %q missing fact %q", summary, expected)
		}
	}
	for _, forbidden := range []string{"BOARD登録済み", "query", "https://", "source body", "report text"} {
		if strings.Contains(summary, forbidden) {
			t.Errorf("Research summary contains non-fact data %q: %s", forbidden, summary)
		}
	}
}

func TestResearchResultLocalReuseIsSeparatedFromExternalAcquisition(t *testing.T) {
	task := Task{TaskID: modulecore.NewTaskID()}
	projection := validResearchProjection(task)
	projection.Counts = ResearchResultCounts{DocumentsFetched: 2, LocalDocumentsReused: 1}
	if err := projection.Validate(task); err != nil {
		t.Fatal(err)
	}
	summary := projection.TaskSummary()
	for _, fact := range []string{"local_documents_reused=1", "local_materials_acquired=0", "external_documents_acquired=1"} {
		if !strings.Contains(summary, fact) {
			t.Fatalf("local evidence was counted as external acquisition: %s", summary)
		}
	}
	for _, invalid := range []int{-1, 3} {
		projection.Counts.LocalDocumentsReused = invalid
		if err := projection.Validate(task); err == nil {
			t.Fatalf("impossible local evidence count %d was accepted", invalid)
		}
	}
}

func TestResearchResultLocalMaterialAcquisitionIsSeparatedFromExternal(t *testing.T) {
	task := Task{TaskID: modulecore.NewTaskID()}
	projection := validResearchProjection(task)
	projection.Counts = ResearchResultCounts{DocumentsFetched: 3, LocalMaterialsAcquired: 2}
	if err := projection.Validate(task); err != nil {
		t.Fatal(err)
	}
	for _, fact := range []string{"sources_fetched=3", "local_documents_reused=0", "local_materials_acquired=2", "external_documents_acquired=1"} {
		if !strings.Contains(projection.TaskSummary(), fact) {
			t.Fatalf("local material count was not projected accurately: %s", projection.TaskSummary())
		}
	}
	for _, counts := range []ResearchResultCounts{
		{DocumentsFetched: 3, LocalMaterialsAcquired: -1},
		{DocumentsFetched: 3, LocalMaterialsAcquired: 4},
		{DocumentsFetched: 3, LocalDocumentsReused: 2, LocalMaterialsAcquired: 2},
	} {
		projection.Counts = counts
		if err := projection.Validate(task); err == nil {
			t.Fatalf("impossible local material counts were accepted: %+v", counts)
		}
	}
	legacy, err := json.Marshal(ResearchResultCounts{DocumentsFetched: 3})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(legacy), "local_materials_acquired") {
		t.Fatalf("zero local material count changed the legacy Task projection wire: %s", legacy)
	}
}

func TestResearchResultProjectionValidatesScopeAndMachineFacts(t *testing.T) {
	value := Task{TaskID: modulecore.NewTaskID()}
	projection := validResearchProjection(value)
	if err := projection.Validate(value); err != nil {
		t.Fatalf("valid projection rejected: %v", err)
	}

	projection.Counts.BoardPostsCreated = -1
	if err := projection.Validate(value); err == nil {
		t.Fatal("negative count accepted")
	}
	projection = validResearchProjection(value)
	projection.Status = "succeeded"
	projection.DataScope = "user"
	projection.OwnerID = "user-1"
	if err := projection.Validate(value); err == nil {
		t.Fatal("user-scope projection without Task binding accepted")
	}

	value.PrivateAccess = &TaskPrivateAccess{Scope: TaskAccessScopeUser, AuthenticatedUserID: "user-1"}
	value.GoalID, value.WorkstreamID = modulecore.NewGoalID(), modulecore.NewWorkstreamID()
	projection = validResearchProjection(value)
	projection.DataScope, projection.OwnerID = "user", "user-1"
	if err := projection.Validate(value); err != nil {
		t.Fatalf("exact user-scope binding rejected: %v", err)
	}
	projection.OwnerID = "user-2"
	if err := projection.Validate(value); err == nil {
		t.Fatal("different user owner accepted")
	}
}

func TestResearchResultProjectionUsesPrivateOriginOwnerForGenericResearchTask(t *testing.T) {
	task := Task{TaskID: modulecore.NewTaskID(), Route: RouteCHAT, OwnerID: "mio", Assignee: "mio"}
	task.OriginSessionID = modulecore.NewSessionID()
	address, err := conversation.NewChannelAddress("viewer", "cmd:ren")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := conversation.NewSessionBindingWithPublicationIncarnation(
		task.OriginSessionID, address, conversation.SessionAudiencePrivate, "ren", 1, "0123456789abcdef0123456789abcdef",
	)
	if err != nil {
		t.Fatal(err)
	}
	publication, err := NewOriginPublicationProvenance(binding)
	if err != nil {
		t.Fatal(err)
	}
	task.OriginPublication = &publication
	if ResearchResultDataScopeMatches(task, "public", "") {
		t.Fatal("private OriginPublication passed the public Research scope helper")
	}
	if !ResearchResultDataScopeMatches(task, "user", "ren") {
		t.Fatal("canonical private-origin owner failed the user Research scope helper")
	}
	if ResearchResultDataScopeMatches(task, "user", "other") {
		t.Fatal("different private-origin owner passed the user Research scope helper")
	}
	projection := validResearchProjection(task)
	projection.DataScope, projection.OwnerID = "user", "ren"
	if err := projection.Validate(task); err != nil {
		t.Fatalf("canonical private-origin user scope rejected: %v", err)
	}
	if task.OwnerID != "mio" || task.Assignee != "mio" {
		t.Fatalf("Research scope changed execution identity: owner=%q assignee=%q", task.OwnerID, task.Assignee)
	}
	publicProjection := projection
	publicProjection.DataScope, publicProjection.OwnerID = "public", ""
	if err := publicProjection.Validate(task); err == nil {
		t.Fatal("private-origin Task accepted a public Research projection")
	}

	projection.OwnerID = "other-user"
	if err := projection.Validate(task); err == nil {
		t.Fatal("different user owner accepted")
	}
	projection.OwnerID = "ren"
	badPublication := *task.OriginPublication
	badPublication.OwnerID = "other-user"
	task.OriginPublication = &badPublication
	if err := projection.Validate(task); err == nil {
		t.Fatal("forged private-origin owner accepted")
	}
	task.OriginPublication = &publication
	task.OriginSessionID = "invalid-session"
	if err := projection.Validate(task); err == nil {
		t.Fatal("invalid origin session accepted")
	}
}

func TestResearchResultProjectionDistinguishesPartialReceiptVerification(t *testing.T) {
	task := Task{TaskID: modulecore.NewTaskID()}
	projection := validResearchProjection(task)
	projection.ReceiptCount = 2
	projection.ReceiptState = "partially_verified"
	projection.RequiredReceiptsComplete = false
	if err := projection.Validate(task); err != nil {
		t.Fatalf("partially verified receipt projection rejected: %v", err)
	}
	projection.ReceiptState = "verified"
	if err := projection.Validate(task); err == nil {
		t.Fatal("partially verified receipts were represented as fully verified")
	}
}

func TestResearchResultProjectionPreservesKnownEngineStatuses(t *testing.T) {
	task := Task{TaskID: modulecore.NewTaskID()}
	for _, status := range []string{"accepted", "running", "waiting_input", "succeeded", "partial", "failed", "cancelled", "unknown"} {
		projection := validResearchProjection(task)
		projection.Status = status
		if err := projection.Validate(task); err != nil {
			t.Errorf("known Engine status %q rejected: %v", status, err)
		}
	}
}

func TestResearchExecutionBindingPinsLocalUserScopeWithoutPayload(t *testing.T) {
	task := Task{TaskID: modulecore.NewTaskID(), Title: "local research", Route: RouteResearch, OwnerID: "local-1", Assignee: "local-1"}
	task.ApplyDefaults(time.Now().UTC())
	binding := ResearchExecutionBinding{
		SchemaVersion: ResearchExecutionBindingSchema, DataScope: "user", OwnerID: "local-1", OwnerRoute: "core.research.local",
	}
	task.ResearchBinding = &binding
	if err := task.Validate(); err != nil {
		t.Fatalf("valid Research binding rejected: %v", err)
	}
	projection := validResearchProjection(task)
	projection.DataScope, projection.OwnerID = "user", "local-1"
	if err := projection.Validate(task); err != nil {
		t.Fatalf("projection does not match binding: %v", err)
	}
	task.ResearchBinding.OwnerID = "other-user"
	if err := task.Validate(); err == nil {
		t.Fatal("binding owner mismatch accepted")
	}
}
