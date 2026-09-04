package vectordb

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/threadmigration"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	"github.com/google/uuid"
	"github.com/qdrant/go-client/qdrant"
)

type canonicalThreadStageFakeClient struct {
	exists         bool
	collection     *qdrant.CreateCollection
	indexes        []*qdrant.CreateFieldIndexCollection
	upserts        []*qdrant.UpsertPoints
	points         []*qdrant.RetrievedPoint
	count          uint64
	collectionErr  error
	createErr      error
	indexErr       error
	upsertErr      error
	countErr       error
	scrollErr      error
	closeErr       error
	collectionCall int
	deletes        int
	aliases        int
	countRequest   *qdrant.CountPoints
	scrolls        []*qdrant.ScrollPoints
}

func (f *canonicalThreadStageFakeClient) CollectionExists(_ context.Context, _ string) (bool, error) {
	f.collectionCall++
	return f.exists, f.collectionErr
}

func (f *canonicalThreadStageFakeClient) CreateCollection(_ context.Context, request *qdrant.CreateCollection) error {
	f.collection = request
	return f.createErr
}

func (f *canonicalThreadStageFakeClient) CreateFieldIndex(_ context.Context, request *qdrant.CreateFieldIndexCollection) (*qdrant.UpdateResult, error) {
	f.indexes = append(f.indexes, request)
	return nil, f.indexErr
}

func (f *canonicalThreadStageFakeClient) Upsert(_ context.Context, request *qdrant.UpsertPoints) (*qdrant.UpdateResult, error) {
	f.upserts = append(f.upserts, request)
	if f.upsertErr != nil {
		return nil, f.upsertErr
	}
	for _, point := range request.Points {
		vector := point.GetVectors().GetVector().GetDense()
		f.points = append(f.points, &qdrant.RetrievedPoint{
			Id:      point.Id,
			Payload: point.Payload,
			Vectors: &qdrant.VectorsOutput{VectorsOptions: &qdrant.VectorsOutput_Vector{Vector: &qdrant.VectorOutput{Vector: &qdrant.VectorOutput_Dense{Dense: &qdrant.DenseVector{Data: append([]float32(nil), vector.GetData()...)}}}}},
		})
	}
	return nil, nil
}

func (f *canonicalThreadStageFakeClient) Count(_ context.Context, request *qdrant.CountPoints) (uint64, error) {
	f.countRequest = request
	if f.countErr != nil {
		return 0, f.countErr
	}
	if f.count != 0 {
		return f.count, nil
	}
	return uint64(len(f.points)), nil
}

func (f *canonicalThreadStageFakeClient) ScrollAndOffset(_ context.Context, request *qdrant.ScrollPoints) ([]*qdrant.RetrievedPoint, *qdrant.PointId, error) {
	f.scrolls = append(f.scrolls, request)
	if f.scrollErr != nil {
		return nil, nil, f.scrollErr
	}
	if request.GetOffset() != nil {
		return nil, nil, nil
	}
	return f.points, nil, nil
}

func (f *canonicalThreadStageFakeClient) Close() error { return f.closeErr }

func (f *canonicalThreadStageFakeClient) DeleteCollection(context.Context, string) error {
	f.deletes++
	return nil
}

func (f *canonicalThreadStageFakeClient) CreateAlias(context.Context, string, string) error {
	f.aliases++
	return nil
}

func TestStageCanonicalThreadPointsFreshStagesValidatedPoints(t *testing.T) {
	prepared := canonicalThreadStagePrepared(t, 3)
	fake := &canonicalThreadStageFakeClient{}
	oldFactory := newCanonicalThreadStageClient
	var gotConfig *qdrant.Config
	newCanonicalThreadStageClient = func(config *qdrant.Config) (canonicalThreadStageClient, error) {
		copy := *config
		gotConfig = &copy
		return fake, nil
	}
	t.Cleanup(func() { newCanonicalThreadStageClient = oldFactory })

	receipt, err := StageCanonicalThreadPointsFresh(context.Background(), "127.0.0.1:6334", "fresh_threads", prepared)
	if err != nil {
		t.Fatalf("StageCanonicalThreadPointsFresh() error = %v", err)
	}
	if receipt.Status != CanonicalThreadStageStatusStagedNotActive {
		t.Fatalf("receipt status = %q", receipt.Status)
	}
	if receipt.PreparedCount != len(prepared.Points) || receipt.StagedCount != len(prepared.Points) || receipt.VectorDimension != prepared.Receipt.VectorDimension {
		t.Fatalf("receipt counts = %+v", receipt)
	}
	if receipt.PreparedOutputSHA256 != prepared.Receipt.OutputSHA256 || receipt.ReadbackOutputSHA256 != prepared.Receipt.OutputSHA256 || receipt.MappingSHA256 != prepared.Plan.MappingSHA256 {
		t.Fatalf("receipt hashes = %+v", receipt)
	}
	if gotConfig == nil || gotConfig.Host != "127.0.0.1" || gotConfig.Port != 6334 || !gotConfig.SkipCompatibilityCheck {
		t.Fatalf("client config = %#v", gotConfig)
	}
	if err := receipt.Validate(); err != nil {
		t.Fatalf("receipt.Validate() error = %v", err)
	}
	if fake.collection == nil || fake.collection.GetCollectionName() != "fresh_threads" {
		t.Fatalf("collection request = %#v", fake.collection)
	}
	params := fake.collection.GetVectorsConfig().GetParams()
	if params == nil || params.GetSize() != uint64(prepared.Receipt.VectorDimension) || params.GetDistance() != qdrant.Distance_Cosine {
		t.Fatalf("vector params = %#v", params)
	}
	if len(fake.indexes) != 2 || fake.indexes[0].GetFieldName() != "session_id" || fake.indexes[1].GetFieldName() != "domain" {
		t.Fatalf("indexes = %#v", fake.indexes)
	}
	if len(fake.upserts) == 0 || len(fake.upserts) > len(prepared.Points) {
		t.Fatalf("upsert batches = %d", len(fake.upserts))
	}
	for _, request := range fake.upserts {
		if !request.GetWait() || len(request.GetPoints()) > canonicalThreadStageMaxBatchPoints {
			t.Fatalf("upsert request = %#v", request)
		}
	}
	if fake.countRequest == nil || !fake.countRequest.GetExact() || fake.countRequest.GetCollectionName() != "fresh_threads" {
		t.Fatalf("count request = %#v", fake.countRequest)
	}
	if len(fake.scrolls) != 1 || fake.scrolls[0].GetCollectionName() != "fresh_threads" || !fake.scrolls[0].GetWithPayload().GetEnable() || !fake.scrolls[0].GetWithVectors().GetEnable() {
		t.Fatalf("scroll requests = %#v", fake.scrolls)
	}
	if fake.deletes != 0 || fake.aliases != 0 {
		t.Fatalf("source cleanup/alias operations were attempted: deletes=%d aliases=%d", fake.deletes, fake.aliases)
	}
}

func TestStageCanonicalThreadPointsFreshRejectsExistingCollectionBeforeMutation(t *testing.T) {
	prepared := canonicalThreadStagePrepared(t, 1)
	fake := &canonicalThreadStageFakeClient{exists: true}
	oldFactory := newCanonicalThreadStageClient
	newCanonicalThreadStageClient = func(*qdrant.Config) (canonicalThreadStageClient, error) { return fake, nil }
	t.Cleanup(func() { newCanonicalThreadStageClient = oldFactory })

	receipt, err := StageCanonicalThreadPointsFresh(context.Background(), "127.0.0.1:6334", "fresh_threads", prepared)
	if err == nil || receipt.Status != CanonicalThreadStageStatusBlocked {
		t.Fatalf("result = %+v, %v; want blocked", receipt, err)
	}
	if receipt.ErrorCode != canonicalThreadStageErrorTargetNotFresh || fake.collection != nil || len(fake.indexes) != 0 || len(fake.upserts) != 0 {
		t.Fatalf("receipt/client mutation = %+v/%#v", receipt, fake)
	}
	if err := receipt.Validate(); err != nil {
		t.Fatalf("blocked receipt.Validate() error = %v", err)
	}
	if fake.deletes != 0 || fake.aliases != 0 {
		t.Fatalf("source cleanup/alias operations were attempted: deletes=%d aliases=%d", fake.deletes, fake.aliases)
	}
}

func TestStageCanonicalThreadPointsFreshRejectsInvalidPreparationWithoutOpeningClient(t *testing.T) {
	factoryCalls := 0
	oldFactory := newCanonicalThreadStageClient
	newCanonicalThreadStageClient = func(*qdrant.Config) (canonicalThreadStageClient, error) {
		factoryCalls++
		return nil, errors.New("must not open")
	}
	t.Cleanup(func() { newCanonicalThreadStageClient = oldFactory })

	receipt, err := StageCanonicalThreadPointsFresh(context.Background(), "127.0.0.1:6334", "fresh_threads", threadmigration.QdrantPreparationResult{})
	if err == nil || receipt.Status != CanonicalThreadStageStatusBlocked || receipt.ErrorCode != canonicalThreadStageErrorPrepared {
		t.Fatalf("result = %+v, %v; want invalid_prepared", receipt, err)
	}
	if factoryCalls != 0 {
		t.Fatalf("factory calls = %d, want 0", factoryCalls)
	}
}

func TestStageCanonicalThreadPointsFreshRejectsReadbackMismatchWithoutDeleting(t *testing.T) {
	prepared := canonicalThreadStagePrepared(t, 1)
	fake := &canonicalThreadStageFakeClient{}
	oldFactory := newCanonicalThreadStageClient
	newCanonicalThreadStageClient = func(*qdrant.Config) (canonicalThreadStageClient, error) { return fake, nil }
	t.Cleanup(func() { newCanonicalThreadStageClient = oldFactory })

	fake.scrollErr = errors.New("readback failed")
	receipt, err := StageCanonicalThreadPointsFresh(context.Background(), "127.0.0.1:6334", "fresh_threads", prepared)
	if err == nil || receipt.Status != CanonicalThreadStageStatusBlocked || receipt.ErrorCode != canonicalThreadStageErrorReadback {
		t.Fatalf("result = %+v, %v; want blocked scroll", receipt, err)
	}
	if err := receipt.Validate(); err != nil {
		t.Fatalf("blocked receipt.Validate() error = %v", err)
	}
}

func TestCanonicalThreadStageJSONValuePreservesTypesAndRejectsUnsafeJSON(t *testing.T) {
	value, err := canonicalThreadStageJSONValue(json.RawMessage(`{"i":9223372036854775807,"f":1.25,"b":true,"s":"ok","n":null,"l":[1,false]}`))
	if err != nil {
		t.Fatalf("canonicalThreadStageJSONValue() error = %v", err)
	}
	object := value.GetStructValue().GetFields()
	if _, ok := object["i"].GetKind().(*qdrant.Value_IntegerValue); !ok {
		t.Fatalf("integer value = %#v", object["i"])
	}
	if _, ok := object["f"].GetKind().(*qdrant.Value_DoubleValue); !ok {
		t.Fatalf("double value = %#v", object["f"])
	}
	cases := []string{
		`{"x":1,"x":2}`,
		`1 2`,
		string([]byte{0xff}),
	}
	for _, raw := range cases {
		if _, err := canonicalThreadStageJSONValue(json.RawMessage(raw)); err == nil {
			t.Fatalf("canonicalThreadStageJSONValue(%q) unexpectedly succeeded", raw)
		}
	}
	deep := []byte("null")
	for i := 0; i < canonicalThreadStageMaxJSONDepth+2; i++ {
		deep = append(append([]byte{'['}, deep...), ']')
	}
	if _, err := canonicalThreadStageJSONValue(deep); err == nil {
		t.Fatal("canonicalThreadStageJSONValue() accepted excessive depth")
	}
}

func canonicalThreadStagePrepared(t *testing.T, count int) threadmigration.QdrantPreparationResult {
	t.Helper()
	plan, err := threadmigration.BuildPlan([]threadmigration.LegacyThreadFact{{
		Surface: "vectordb_stage_test", RecordKey: "thread-7", SessionID: "legacy-stage-session", LegacyThreadID: 7,
		KindHint: string(modulecore.ThreadKindUserConversation),
	}})
	if err != nil {
		t.Fatal(err)
	}
	points := make([]threadmigration.QdrantPointSnapshot, 0, count)
	for i := 0; i < count; i++ {
		id := uuid.MustParse("00000000-0000-4000-8000-00000000000" + string(rune('1'+i)))
		points = append(points, threadmigration.QdrantPointSnapshot{
			PointID: id.String(), Vector: []float32{1, -2.5, 0.25}, Payload: map[string]json.RawMessage{
				"session_id": json.RawMessage(`"legacy-stage-session"`),
				"thread_id":  json.RawMessage(`7`),
				"body":       json.RawMessage(`{"message":"same"}`),
				"domain":     json.RawMessage(`"conversation"`),
			},
		})
	}
	prepared, err := threadmigration.PrepareQdrantPoints(threadmigration.QdrantPreparationInput{Phase: threadmigration.QdrantPreparationPhase, Plan: plan, Points: points})
	if err != nil {
		t.Fatal(err)
	}
	return prepared
}
