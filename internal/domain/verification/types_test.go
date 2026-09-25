package verification

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestVerificationStatusValid(t *testing.T) {
	valid := []VerificationStatus{
		StatusVerified,
		StatusWeaklySupported,
		StatusUnsupported,
		StatusConflict,
		StatusNotChecked,
	}
	for _, status := range valid {
		if !status.Valid() {
			t.Fatalf("expected status %s to be valid", status)
		}
	}
	if VerificationStatus("success").Valid() {
		t.Fatal("success must not be a verification status")
	}
}

func TestTriggerLevelAndEvidenceSourceValidity(t *testing.T) {
	for _, level := range []TriggerLevel{TriggerLow, TriggerMedium, TriggerHigh} {
		if !level.Valid() {
			t.Fatalf("expected trigger level %s to be valid", level)
		}
	}
	if TriggerLevel("urgent").Valid() {
		t.Fatal("urgent must not be a trigger level")
	}

	for _, sourceType := range []EvidenceSourceType{
		EvidenceRecallPack,
		EvidenceConversationMemory,
		EvidenceL1SQLite,
		EvidenceVectorThreadMemory,
		EvidenceVectorKB,
		EvidenceSQLiteArchive,
		EvidenceSourceRegistry,
		EvidenceSearchCache,
		EvidenceRawExternalSource,
		EvidenceExecutionReport,
	} {
		if !sourceType.Valid() {
			t.Fatalf("expected source type %s to be valid", sourceType)
		}
	}
	if EvidenceSourceType("screenshot").Valid() {
		t.Fatal("screenshot must not be accepted as an evidence source type")
	}
	if !EvidenceSourceType("duckdb_archive").Valid() {
		t.Fatal("persisted archive evidence must remain readable")
	}
}

func TestClaimValidateRejectsEmptyText(t *testing.T) {
	claim := Claim{ID: "claim-1", Priority: TriggerHigh, Status: StatusNotChecked}
	if err := claim.Validate(); err == nil {
		t.Fatal("expected empty claim text to be rejected")
	}
	claim = Claim{Text: "answer", Priority: TriggerHigh, Status: StatusNotChecked}
	if err := claim.Validate(); err == nil || !strings.Contains(err.Error(), "claim id") {
		t.Fatalf("expected empty claim id to be rejected, got %v", err)
	}
}

func TestClaimValidateAcceptsEvidenceAndRejectsInvalidMetadata(t *testing.T) {
	claim := Claim{
		ID:       "claim-1",
		Text:     "RenCrow has memory layers",
		Priority: TriggerMedium,
		Status:   StatusWeaklySupported,
		Evidence: []EvidenceRef{{
			ID:         "ev-1",
			SourceType: EvidenceRecallPack,
			Supports:   true,
		}},
	}
	if err := claim.Validate(); err != nil {
		t.Fatalf("expected claim to validate: %v", err)
	}
	claim.Priority = TriggerLevel("urgent")
	if err := claim.Validate(); err == nil {
		t.Fatal("invalid claim priority should fail")
	}
	claim.Priority = TriggerMedium
	claim.Status = VerificationStatus("passed")
	if err := claim.Validate(); err == nil {
		t.Fatal("invalid claim status should fail")
	}
}

func TestEvidenceRefRejectsInvalidSource(t *testing.T) {
	evidence := EvidenceRef{ID: "ev-1", SourceType: EvidenceSourceType("raw_log"), Supports: true}
	if err := evidence.Validate(); err == nil {
		t.Fatal("raw_log must not be accepted as evidence source")
	}
}

func TestEvidenceRefRejectsMissingIDAndConflictWithSupport(t *testing.T) {
	if err := (EvidenceRef{SourceType: EvidenceRecallPack, Supports: true}).Validate(); err == nil {
		t.Fatal("missing evidence id should fail")
	}
	if err := (EvidenceRef{ID: "ev-1", SourceType: EvidenceRecallPack, Supports: true, Conflicts: true}).Validate(); err == nil {
		t.Fatal("evidence cannot both support and conflict")
	}
}

func TestVerificationQuestionValidate(t *testing.T) {
	question := VerificationQuestion{ID: "q-1", ClaimID: "claim-1", Query: "What supports this?"}
	if err := question.Validate(); err != nil {
		t.Fatalf("expected question to validate: %v", err)
	}
	for _, invalid := range []VerificationQuestion{
		{ClaimID: "claim-1", Query: "query"},
		{ID: "q-1", Query: "query"},
		{ID: "q-1", ClaimID: "claim-1"},
	} {
		if err := invalid.Validate(); err == nil {
			t.Fatalf("invalid question should fail: %#v", invalid)
		}
	}
}

func TestVerificationPolicyNormalized(t *testing.T) {
	policy := (VerificationPolicy{}).Normalized()
	if policy.Default != TriggerLow || policy.Mode != "dry_run" {
		t.Fatalf("unexpected defaults: %#v", policy)
	}
	policy = (VerificationPolicy{Enabled: true, Mode: "revise", Default: TriggerHigh}).Normalized()
	if !policy.Enabled || policy.Default != TriggerHigh || policy.Mode != "revise" {
		t.Fatalf("explicit policy should be preserved: %#v", policy)
	}
}

func TestVerificationReportValidate(t *testing.T) {
	taskID := modulecore.NewTaskID()
	report := VerificationReport{
		ArtifactID:   modulecore.NewArtifactID(),
		Kind:         modulecore.ArtifactKindReport,
		TaskID:       taskID,
		SessionID:    "session-1",
		Route:        "CHAT",
		Status:       StatusNotChecked,
		TriggerLevel: TriggerLow,
		CreatedAt:    time.Now().UTC(),
	}
	report.ContentHash = verificationReportDigest(verificationReportEmptyBodyFixture)
	if err := report.Validate(); err != nil {
		t.Fatalf("expected report to validate: %v", err)
	}
	report.Status = VerificationStatus("passed")
	if err := report.Validate(); err == nil {
		t.Fatal("expected invalid report status to be rejected")
	}
}

func TestVerificationReportJSONUsesOnlyCanonicalTaskID(t *testing.T) {
	report := VerificationReport{
		ArtifactID: modulecore.NewArtifactID(), Kind: modulecore.ArtifactKindReport, TaskID: modulecore.NewTaskID(), SessionID: "session-1",
		Status: StatusNotChecked, CreatedAt: time.Now().UTC(),
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	legacyKey := "job" + "_" + "id"
	if strings.Contains(string(encoded), legacyKey) || !strings.Contains(string(encoded), `"task_id":"tsk_`) {
		t.Fatalf("unexpected report JSON: %s", encoded)
	}
}

func TestVerificationReportValidateRejectsMissingFields(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		name string
		item VerificationReport
		want string
	}{
		{name: "missing artifact id", item: VerificationReport{Kind: modulecore.ArtifactKindReport, TaskID: modulecore.NewTaskID(), SessionID: "session-1", Status: StatusNotChecked, CreatedAt: now}, want: "artifact_id"},
		{name: "missing artifact kind", item: VerificationReport{ArtifactID: modulecore.NewArtifactID(), TaskID: modulecore.NewTaskID(), SessionID: "session-1", Status: StatusNotChecked, CreatedAt: now}, want: "artifact_kind"},
		{name: "invalid artifact kind", item: VerificationReport{ArtifactID: modulecore.NewArtifactID(), Kind: modulecore.ArtifactKindDraft, TaskID: modulecore.NewTaskID(), SessionID: "session-1", Status: StatusNotChecked, CreatedAt: now}, want: "artifact_kind must be"},
		{name: "missing task", item: VerificationReport{ArtifactID: modulecore.NewArtifactID(), Kind: modulecore.ArtifactKindReport, SessionID: "session-1", Status: StatusNotChecked, CreatedAt: now}, want: "task_id"},
		{name: "invalid task", item: VerificationReport{ArtifactID: modulecore.NewArtifactID(), Kind: modulecore.ArtifactKindReport, TaskID: "not-canonical", SessionID: "session-1", Status: StatusNotChecked, CreatedAt: now}, want: "task_id"},
		{name: "missing session", item: VerificationReport{ArtifactID: modulecore.NewArtifactID(), Kind: modulecore.ArtifactKindReport, TaskID: modulecore.NewTaskID(), Status: StatusNotChecked, CreatedAt: now}, want: "session_id"},
		{name: "missing status", item: VerificationReport{ArtifactID: modulecore.NewArtifactID(), Kind: modulecore.ArtifactKindReport, TaskID: modulecore.NewTaskID(), SessionID: "session-1", CreatedAt: now}, want: "status"},
		{name: "missing created", item: VerificationReport{ArtifactID: modulecore.NewArtifactID(), Kind: modulecore.ArtifactKindReport, TaskID: modulecore.NewTaskID(), SessionID: "session-1", Status: StatusNotChecked}, want: "created_at"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.item.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v, want %q", err, tc.want)
			}
		})
	}
}

func TestVerificationReportValidateNestedObjects(t *testing.T) {
	report := VerificationReport{
		ArtifactID:   modulecore.NewArtifactID(),
		Kind:         modulecore.ArtifactKindReport,
		TaskID:       modulecore.NewTaskID(),
		SessionID:    "session-1",
		Route:        "CHAT",
		Status:       StatusVerified,
		TriggerLevel: TriggerHigh,
		Claims: []Claim{{
			ID:       "claim-1",
			Text:     "answer",
			Priority: TriggerLow,
			Status:   StatusVerified,
		}},
		Questions: []VerificationQuestion{{
			ID:      "q-1",
			ClaimID: "claim-1",
			Query:   "verify answer",
		}},
		Evidence: []EvidenceRef{{
			ID:         "ev-1",
			SourceType: EvidenceExecutionReport,
			Supports:   true,
		}},
		CreatedAt: time.Now().UTC(),
	}
	report.ContentHash = verificationReportContentHash(t, report)
	if err := report.Validate(); err != nil {
		t.Fatalf("expected nested report to validate: %v", err)
	}
	report.TriggerLevel = TriggerLevel("urgent")
	if err := report.Validate(); err == nil {
		t.Fatal("invalid report trigger level should fail")
	}
	report.TriggerLevel = TriggerHigh
	report.Claims[0].Text = ""
	if err := report.Validate(); err == nil || !strings.Contains(err.Error(), "claim text") {
		t.Fatalf("expected nested claim error, got %v", err)
	}
	report.Claims[0].Text = "answer"
	report.Questions[0].Query = ""
	if err := report.Validate(); err == nil || !strings.Contains(err.Error(), "query") {
		t.Fatalf("expected nested question error, got %v", err)
	}
	report.Questions[0].Query = "verify answer"
	report.Evidence[0].Supports = true
	report.Evidence[0].Conflicts = true
	if err := report.Validate(); err == nil || !strings.Contains(err.Error(), "both support and conflict") {
		t.Fatalf("expected nested evidence error, got %v", err)
	}
}

// verificationReportBodyFixture is the exact body projection bytes that the content
// digest of the report built by verificationReportPayload must cover: the final
// verification result (status), the counts the producer derived from the claims, the
// claims, the questions and the evidence in declaration order, plus skip_reason.
// Identity metadata (artifact_id, artifact_kind, task_id, session_id, route), the policy
// input (trigger_level) and created_at sit outside these bytes, so reminting an
// ArtifactID or appending a supersession later never changes the digest. The nested
// evidence carries retrieved_at because a json omitempty tag has no effect on time.Time:
// even a zero value is written as 0001-01-01T00:00:00Z instead of being dropped.
// These are fixtures for the contract under test, not a claim that such a row exists in
// the live database.
const (
	verificationEvidenceFixture = `{"id":"ev-1","source_type":"l1_sqlite","value":"row 1","retrieved_at":"2026-09-21T16:59:00Z","supports":true}`

	verificationReportBodyFixture = `{"status":"not_checked","claim_count":1,"verified_count":0,"weak_count":0,"unsupported_count":0,"conflict_count":0,"not_checked_count":1,"claims":[{"id":"claim-1","text":"RenCrow has memory layers","priority":"low","status":"not_checked","evidence":[` + verificationEvidenceFixture + `]}],"questions":[{"id":"vq_001","claim_id":"claim-1","query":"Verify the claim using independent evidence: RenCrow has memory layers"}],"evidence":[` + verificationEvidenceFixture + `],"skip_reason":""}`

	// verificationReportNoClaimsBodyFixture is a different body: an empty claim list is
	// content of its own, and the projection must normalize a nil list to the same bytes.
	verificationReportNoClaimsBodyFixture = `{"status":"not_checked","claim_count":0,"verified_count":0,"weak_count":0,"unsupported_count":0,"conflict_count":0,"not_checked_count":0,"claims":[],"questions":[],"evidence":[],"skip_reason":"no factual claims"}`

	// verificationReportEmptyBodyFixture is the body of a report that names no claim,
	// question or evidence and records no skip reason, so a test that builds a report by
	// hand can pin its digest from these bytes instead of the projection code.
	verificationReportEmptyBodyFixture = `{"status":"not_checked","claim_count":0,"verified_count":0,"weak_count":0,"unsupported_count":0,"conflict_count":0,"not_checked_count":0,"claims":[],"questions":[],"evidence":[],"skip_reason":""}`
)

// verificationReportPayload builds a canonical report JSON from a body projection, so a
// test can vary only the fields it names.
func verificationReportPayload(bodyFixture string, extra string) string {
	identity := `{"artifact_id":"art_00000000-0000-5000-8000-000000000001","artifact_kind":"report","task_id":"tsk_00000000-0000-5000-8000-000000000002","session_id":"session-1","route":"CHAT","trigger_level":"low","created_at":"2026-09-21T17:00:00Z",`
	return identity + extra + strings.TrimPrefix(bodyFixture, "{")
}

func verificationReportDigest(bodyFixture string) string {
	return modulecore.ContentHashOf([]byte(bodyFixture))
}

// verificationReportContentHash digests a report built in a test. The tests that pin one of
// the body fixtures above are what catch a body projection drifting from these bytes.
func verificationReportContentHash(t *testing.T, item VerificationReport) string {
	t.Helper()
	digest, err := ComputeVerificationReportContentHash(item)
	if err != nil {
		t.Fatalf("compute report content hash: %v", err)
	}
	return digest
}

// verificationFixtureReport decodes a canonical report carrying the digest of one of the
// body fixtures. Decoding plus Validate is the contract check itself: if the declared body
// bytes drifted from these fixture bytes, the digest would no longer match.
func verificationFixtureReport(t *testing.T, bodyFixture string) VerificationReport {
	t.Helper()
	payload := verificationReportPayload(bodyFixture, `"content_hash":"`+verificationReportDigest(bodyFixture)+`",`)
	var report VerificationReport
	if err := json.Unmarshal([]byte(payload), &report); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if err := report.Validate(); err != nil {
		t.Fatalf("a report carrying its matching digest must validate, got %v", err)
	}
	return report
}

// verificationReportWith returns a fixture report with one change applied to its own copy of
// every list, so a case cannot write into rows another case still reads.
func verificationReportWith(t *testing.T, bodyFixture string, change func(*VerificationReport)) VerificationReport {
	t.Helper()
	report := verificationFixtureReport(t, bodyFixture)
	claims := make([]Claim, len(report.Claims))
	copy(claims, report.Claims)
	for i := range claims {
		claims[i].Evidence = append([]EvidenceRef(nil), report.Claims[i].Evidence...)
	}
	report.Claims = claims
	report.Questions = append([]VerificationQuestion(nil), report.Questions...)
	report.Evidence = append([]EvidenceRef(nil), report.Evidence...)
	change(&report)
	return report
}

// TestVerificationReportContentHashJSONContract checks that a report decoded from the
// wire keeps its content digest and successor reference instead of dropping them.
func TestVerificationReportContentHashJSONContract(t *testing.T) {
	digest := verificationReportDigest(verificationReportBodyFixture)
	payload := verificationReportPayload(verificationReportBodyFixture, `"content_hash":"`+digest+`","superseded_by":"art_00000000-0000-5000-8000-00000000000b",`)
	var report VerificationReport
	if err := json.Unmarshal([]byte(payload), &report); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if err := report.Validate(); err != nil {
		t.Fatalf("a report carrying its matching digest must validate, got %v", err)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"content_hash":"` + digest + `"`, `"superseded_by":"art_00000000-0000-5000-8000-00000000000b"`} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("report JSON must preserve %s, got %s", want, encoded)
		}
	}
}

// TestValidateVerificationReportContentHashContract pins the refusals that protect the
// verification report body. The digest is taken over the body projection declared by the
// verification domain, and the successor reference is checked by the shared validator.
func TestValidateVerificationReportContentHashContract(t *testing.T) {
	matching := verificationReportDigest(verificationReportBodyFixture)
	other := verificationReportDigest(verificationReportNoClaimsBodyFixture)
	cases := []struct {
		name    string
		payload string
		want    string
	}{
		{name: "missing content hash", payload: verificationReportPayload(verificationReportBodyFixture, ""), want: "content_hash is required"},
		{name: "content hash without prefix", payload: verificationReportPayload(verificationReportBodyFixture, `"content_hash":"0e3f5c8d1b6a4972ae0c7f4d63b81a29c5e7d0f318a4b6c2d9e0f1a3b5c7d9e1",`), want: "must be a sha256:prefixed"},
		{name: "content hash of another body", payload: verificationReportPayload(verificationReportBodyFixture, `"content_hash":"`+other+`",`), want: "does not match content digest"},
		// The sha256: prefix stays lowercase so this case reaches the digest check and is
		// refused for the uppercase hex, not for a missing prefix.
		{name: "content hash uppercase digest", payload: verificationReportPayload(verificationReportBodyFixture, `"content_hash":"`+modulecore.ContentHashPrefix+strings.ToUpper(strings.TrimPrefix(matching, modulecore.ContentHashPrefix))+`",`), want: "lowercase"},
		{name: "supersedes itself", payload: verificationReportPayload(verificationReportBodyFixture, `"content_hash":"`+matching+`","superseded_by":"art_00000000-0000-5000-8000-000000000001",`), want: "must not reference the artifact itself"},
		{name: "superseded by opaque id", payload: verificationReportPayload(verificationReportBodyFixture, `"content_hash":"`+matching+`","superseded_by":"art_1",`), want: "superseded_by"},
		{name: "matching hash and canonical supersession", payload: verificationReportPayload(verificationReportBodyFixture, `"content_hash":"`+matching+`","superseded_by":"art_00000000-0000-7000-8000-00000000000d",`), want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var report VerificationReport
			if err := json.Unmarshal([]byte(tc.payload), &report); err != nil {
				t.Fatalf("decode payload: %v", err)
			}
			err := report.Validate()
			if tc.want == "" {
				if err != nil {
					t.Fatalf("expected the canonical payload to validate, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v, want %q", err, tc.want)
			}
		})
	}
}

// TestVerificationReportDigestCoversBodyNotIdentityMetadata pins what the digest is silent
// about, so a later remint of an ArtifactID or an appended supersession never rewrites the
// content identity of an already stored report.
func TestVerificationReportDigestCoversBodyNotIdentityMetadata(t *testing.T) {
	report := verificationFixtureReport(t, verificationReportBodyFixture)
	digest := verificationReportDigest(verificationReportBodyFixture)

	reminted := report
	reminted.ArtifactID = modulecore.NewArtifactID()
	reminted.TaskID = modulecore.NewTaskID()
	reminted.SessionID = "session-2"
	reminted.Route = "RENDER"
	reminted.TriggerLevel = TriggerHigh
	reminted.CreatedAt = report.CreatedAt.Add(-48 * time.Hour)
	if reminted.ArtifactID == report.ArtifactID {
		t.Fatal("a reminted artifact id must differ from the stored one")
	}
	if got := verificationReportContentHash(t, reminted); got != digest {
		t.Fatalf("identity metadata changed the digest: got %s want %s", got, digest)
	}

	// Two artifacts holding the same body share one digest, and the successor reference
	// itself never enters the bytes it describes.
	successor := reminted
	successor.ArtifactID = modulecore.NewArtifactID()
	successor.SupersededBy = report.ArtifactID
	if got := verificationReportContentHash(t, successor); got != digest {
		t.Fatalf("a successor reference changed the digest: got %s want %s", got, digest)
	}
	if err := successor.Validate(); err != nil {
		t.Fatalf("a report naming its successor must validate, got %v", err)
	}

	// error_kind and error are the diagnostic the pipeline writes onto the same report
	// after a store Save failure, so the report it returns keeps matching its own digest.
	annotated := report
	annotated.ErrorKind = ErrorPersistenceFailed
	annotated.Error = "verification report store Save: no space left on device"
	if got := verificationReportContentHash(t, annotated); got != digest {
		t.Fatalf("the Save failure diagnostic changed the digest: got %s want %s", got, digest)
	}
	if err := annotated.Validate(); err != nil {
		t.Fatalf("a report annotated after a Save failure must still match its own digest, got %v", err)
	}
}

// TestVerificationReportDigestChangesWithBodyContent refuses a digest that was carried over
// from a previous body: every value the report states as its verified content has to move
// the digest.
func TestVerificationReportDigestChangesWithBodyContent(t *testing.T) {
	digest := verificationReportDigest(verificationReportBodyFixture)
	cases := []struct {
		name   string
		change func(*VerificationReport)
	}{
		{name: "claim text", change: func(r *VerificationReport) { r.Claims[0].Text = "RenCrow has one memory layer" }},
		{name: "claim status", change: func(r *VerificationReport) { r.Claims[0].Status = StatusWeaklySupported }},
		{name: "nested evidence value", change: func(r *VerificationReport) { r.Claims[0].Evidence[0].Value = "row 2" }},
		{name: "question query", change: func(r *VerificationReport) { r.Questions[0].Query = "verify something else" }},
		{name: "top level evidence value", change: func(r *VerificationReport) { r.Evidence[0].Value = "row 2" }},
		{name: "claim count", change: func(r *VerificationReport) { r.ClaimCount = 2 }},
		{name: "verified count", change: func(r *VerificationReport) { r.VerifiedCount = 1 }},
		{name: "not checked count", change: func(r *VerificationReport) { r.NotCheckedCount = 0 }},
		{name: "skip reason", change: func(r *VerificationReport) { r.SkipReason = "annotated after the run" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			changed := verificationReportWith(t, verificationReportBodyFixture, tc.change)
			if changed.ContentHash != digest {
				t.Fatalf("the case replaced the hash it should keep, got %s", changed.ContentHash)
			}
			if got := verificationReportContentHash(t, changed); got == digest {
				t.Fatalf("%s must change the body digest %s", tc.name, digest)
			}
			if err := changed.Validate(); err == nil || !strings.Contains(err.Error(), "does not match content digest") {
				t.Fatalf("err=%v, want a content digest mismatch after %s", err, tc.name)
			}
		})
	}
}

// TestVerificationReportDigestTreatsNilAndEmptyListsAsSameContent fixes the missing-list
// contract for the top-level lists and for the evidence nested in a claim: one content keeps
// one digest whether the producer wrote nothing or nothing was recorded for that list.
func TestVerificationReportDigestTreatsNilAndEmptyListsAsSameContent(t *testing.T) {
	question := func() []VerificationQuestion {
		return []VerificationQuestion{{ID: "vq_001", ClaimID: "claim-1", Query: "verify answer"}}
	}
	build := func(claims []Claim, questions []VerificationQuestion, evidence []EvidenceRef) VerificationReport {
		return VerificationReport{
			ArtifactID: modulecore.NewArtifactID(), Kind: modulecore.ArtifactKindReport,
			TaskID: modulecore.NewTaskID(), SessionID: "session-1", Route: "CHAT",
			Status: StatusNotChecked, TriggerLevel: TriggerLow,
			Claims: claims, Questions: questions, Evidence: evidence,
			CreatedAt: time.Date(2026, 9, 21, 17, 0, 0, 0, time.UTC),
		}
	}

	nilLists := build(nil, nil, nil)
	emptyLists := build([]Claim{}, []VerificationQuestion{}, []EvidenceRef{})
	nilDigest, emptyDigest := verificationReportContentHash(t, nilLists), verificationReportContentHash(t, emptyLists)
	if nilDigest != emptyDigest {
		t.Fatalf("a missing top-level list and an empty one digested differently: %s vs %s", nilDigest, emptyDigest)
	}
	if want := verificationReportDigest(verificationReportEmptyBodyFixture); nilDigest != want {
		t.Fatalf("the nil lists must reach the pinned empty body bytes %s, got %s", want, nilDigest)
	}

	claimWith := func(evidence []EvidenceRef) []Claim {
		return []Claim{{ID: "claim-1", Text: "answer", Priority: TriggerLow, Status: StatusVerified, Evidence: evidence}}
	}
	nilNested := build(claimWith(nil), question(), nil)
	emptyNested := build(claimWith([]EvidenceRef{}), question(), []EvidenceRef{})
	if got, want := verificationReportContentHash(t, nilNested), verificationReportContentHash(t, emptyNested); got != want {
		t.Fatalf("a claim with no evidence digested differently by list kind: %s vs %s", got, want)
	}
	// The producer stamps the digest of the body it wrote, exactly as it does for a report
	// whose claim did record evidence, and that report validates.
	nilNested.ContentHash = verificationReportContentHash(t, nilNested)
	if err := nilNested.Validate(); err != nil {
		t.Fatalf("a report whose claim recorded no evidence must validate, got %v", err)
	}
}

// TestVerificationReportDigestChangesWithListOrder keeps a list's order inside the content:
// the same rows written in another order are another digest.
func TestVerificationReportDigestChangesWithListOrder(t *testing.T) {
	first := EvidenceRef{ID: "ev-1", SourceType: EvidenceL1SQLite, Value: "row 1", Supports: true}
	second := EvidenceRef{ID: "ev-2", SourceType: EvidenceVectorKB, Value: "row 2", Supports: true}

	forward := verificationReportWith(t, verificationReportBodyFixture, func(r *VerificationReport) {
		r.Evidence = []EvidenceRef{first, second}
	})
	reversed := verificationReportWith(t, verificationReportBodyFixture, func(r *VerificationReport) {
		r.Evidence = []EvidenceRef{second, first}
	})
	if got, want := verificationReportContentHash(t, forward), verificationReportContentHash(t, reversed); got == want {
		t.Fatalf("reordering the evidence list kept one digest %s", got)
	}

	nestedForward := verificationReportWith(t, verificationReportBodyFixture, func(r *VerificationReport) {
		r.Claims[0].Evidence = []EvidenceRef{first, second}
	})
	nestedReversed := verificationReportWith(t, verificationReportBodyFixture, func(r *VerificationReport) {
		r.Claims[0].Evidence = []EvidenceRef{second, first}
	})
	if got, want := verificationReportContentHash(t, nestedForward), verificationReportContentHash(t, nestedReversed); got == want {
		t.Fatalf("reordering the evidence nested in a claim kept one digest %s", got)
	}
}

// TestVerificationReportBodyBytesDoesNotWriteIntoTheInput keeps the projection honest: it
// normalizes a nil list for the digest without editing the report it was handed, which is
// the same report the store and the Viewer still hold.
func TestVerificationReportBodyBytesDoesNotWriteIntoTheInput(t *testing.T) {
	item := verificationFixtureReport(t, verificationReportBodyFixture)
	item.Claims[0].Evidence = nil
	item.Questions = nil
	item.Evidence = nil

	if _, err := VerificationReportBodyBytes(item); err != nil {
		t.Fatalf("encode body: %v", err)
	}
	if item.Claims[0].Evidence != nil {
		t.Fatalf("the projection wrote an empty list through into the caller's claim: %#v", item.Claims[0].Evidence)
	}
	if item.Questions != nil || item.Evidence != nil {
		t.Fatalf("the projection replaced a caller's list: questions=%#v evidence=%#v", item.Questions, item.Evidence)
	}

	withEmpty := item
	withEmpty.Claims = append([]Claim(nil), item.Claims...)
	withEmpty.Claims[0].Evidence = []EvidenceRef{}
	withEmpty.Questions = []VerificationQuestion{}
	withEmpty.Evidence = []EvidenceRef{}
	if got, want := verificationReportContentHash(t, item), verificationReportContentHash(t, withEmpty); got != want {
		t.Fatalf("nil and empty lists digested differently: %s vs %s", got, want)
	}
}

// TestVerificationReportBodyBytesRejectsUnencodableRetrievedAt covers the one field of the
// body that encoding/json can refuse: an evidence timestamp outside the encodable range.
// The error is returned to the caller instead of a panic, an empty digest, or a digest of
// bytes that were never persisted.
func TestVerificationReportBodyBytesRejectsUnencodableRetrievedAt(t *testing.T) {
	outOfRange := time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		change func(*VerificationReport)
	}{
		{name: "top level evidence", change: func(r *VerificationReport) { r.Evidence[0].RetrievedAt = outOfRange }},
		{name: "nested claim evidence", change: func(r *VerificationReport) { r.Claims[0].Evidence[0].RetrievedAt = outOfRange }},
	}
	storedDigest := verificationReportDigest(verificationReportBodyFixture)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			item := verificationReportWith(t, verificationReportBodyFixture, tc.change)
			got, err := ComputeVerificationReportContentHash(item)
			if err == nil {
				t.Fatalf("a retrieved_at outside the encodable range must return an error, got digest %q", got)
			}
			if got != "" {
				t.Fatalf("no digest may be returned when the body cannot be encoded, got %q", got)
			}
			if !strings.Contains(err.Error(), "encode verification report body") {
				t.Fatalf("err=%v, want it to name the report body it could not encode", err)
			}
			if err := item.Validate(); err == nil || !strings.Contains(err.Error(), "encode verification report body") {
				t.Fatalf("err=%v, want Validate to propagate the encoding error for the stored hash %s", err, storedDigest)
			}
		})
	}
}
