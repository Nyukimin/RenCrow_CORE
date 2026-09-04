package vectordb

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	"github.com/google/uuid"
	"github.com/qdrant/go-client/qdrant"
)

// SaveThreadSummary はThread要約をVectorDBに保存
func (v *VectorDBStore) SaveThreadSummary(ctx context.Context, summary *conversation.ThreadSummary) error {
	if summary == nil {
		return fmt.Errorf("thread summary is required")
	}
	pointID, err := canonicalThreadSummaryPointID(summary.ThreadID)
	if err != nil {
		return err
	}
	return v.SaveThreadSummaryWithPointID(ctx, summary, pointID)
}

// SaveThreadSummaryWithPointID preserves the explicit call boundary used by
// owner tooling, but the point identity is not caller-selectable: one Thread
// has exactly one Qdrant point, identified by its canonical Thread UUID.
func (v *VectorDBStore) SaveThreadSummaryWithPointID(ctx context.Context, summary *conversation.ThreadSummary, pointID string) error {
	point, err := threadSummaryPoint(summary, pointID)
	if err != nil {
		return err
	}

	// Upsert（Wait=trueで同期書き込み）
	waitTrue := true
	_, err = v.client.Upsert(ctx, &qdrant.UpsertPoints{
		CollectionName: v.collectionName,
		Points:         []*qdrant.PointStruct{point},
		Wait:           &waitTrue,
	})
	if err != nil {
		return fmt.Errorf("failed to upsert point to vectordb: %w", err)
	}

	return nil
}

func threadSummaryPoint(summary *conversation.ThreadSummary, pointID string) (*qdrant.PointStruct, error) {
	if err := validateThreadSummaryForSave(summary); err != nil {
		return nil, err
	}
	expectedPointID, err := canonicalThreadSummaryPointID(summary.ThreadID)
	if err != nil {
		return nil, err
	}
	if pointID != expectedPointID {
		return nil, fmt.Errorf("point ID must equal canonical Thread UUID %q", expectedPointID)
	}
	return &qdrant.PointStruct{
		Id: &qdrant.PointId{
			PointIdOptions: &qdrant.PointId_Uuid{Uuid: pointID},
		},
		Vectors: &qdrant.Vectors{
			VectorsOptions: &qdrant.Vectors_Vector{
				Vector: &qdrant.Vector{
					Data: summary.Embedding,
				},
			},
		},
		Payload: threadSummaryPayload(summary),
	}, nil
}

func canonicalThreadSummaryPointID(threadID modulecore.ThreadID) (string, error) {
	if err := threadID.Validate(); err != nil {
		return "", fmt.Errorf("thread_id: %w", err)
	}
	raw := strings.TrimPrefix(string(threadID), "thr_")
	parsed, err := uuid.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("thread_id does not contain a canonical UUID: %w", err)
	}
	return parsed.String(), nil
}

func threadSummaryPayload(summary *conversation.ThreadSummary) map[string]*qdrant.Value {
	payload := map[string]*qdrant.Value{
		"thread_id": {
			Kind: &qdrant.Value_StringValue{StringValue: string(summary.ThreadID)},
		},
		"thread_seq": {
			Kind: &qdrant.Value_IntegerValue{IntegerValue: int64(summary.ThreadSeq)},
		},
		"thread_kind": {
			Kind: &qdrant.Value_StringValue{StringValue: string(summary.ThreadKind)},
		},
		"session_id": {
			Kind: &qdrant.Value_StringValue{StringValue: summary.SessionID},
		},
		"ts_start": {
			Kind: &qdrant.Value_IntegerValue{IntegerValue: summary.StartTime.Unix()},
		},
		"ts_end": {
			Kind: &qdrant.Value_IntegerValue{IntegerValue: summary.EndTime.Unix()},
		},
		"domain": {
			Kind: &qdrant.Value_StringValue{StringValue: summary.Domain},
		},
		"summary": {
			Kind: &qdrant.Value_StringValue{StringValue: summary.Summary},
		},
		"is_novel": {
			Kind: &qdrant.Value_BoolValue{BoolValue: summary.IsNovel},
		},
	}

	// Keywords追加
	if len(summary.Keywords) > 0 {
		keywordsList := make([]*qdrant.Value, 0, len(summary.Keywords))
		for _, kw := range summary.Keywords {
			keywordsList = append(keywordsList, &qdrant.Value{
				Kind: &qdrant.Value_StringValue{StringValue: kw},
			})
		}
		payload["keywords"] = &qdrant.Value{
			Kind: &qdrant.Value_ListValue{
				ListValue: &qdrant.ListValue{Values: keywordsList},
			},
		}
	}
	return payload
}

// SearchSimilar はembeddingベクトル類似度検索
func (v *VectorDBStore) SearchSimilar(ctx context.Context, queryEmbedding []float32, topK int) ([]*conversation.ThreadSummary, error) {
	if len(queryEmbedding) == 0 {
		return nil, fmt.Errorf("queryEmbedding is empty")
	}

	limit := uint64(topK)
	// ベクトル検索（WithPayloadで要約情報も取得）
	searchResult, err := v.client.Query(ctx, &qdrant.QueryPoints{
		CollectionName: v.collectionName,
		Query: &qdrant.Query{
			Variant: &qdrant.Query_Nearest{
				Nearest: &qdrant.VectorInput{
					Variant: &qdrant.VectorInput_Dense{
						Dense: &qdrant.DenseVector{
							Data: queryEmbedding,
						},
					},
				},
			},
		},
		Limit:       &limit,
		WithPayload: &qdrant.WithPayloadSelector{SelectorOptions: &qdrant.WithPayloadSelector_Enable{Enable: true}},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to search similar: %w", err)
	}

	// 結果をThreadSummaryに変換
	summaries := make([]*conversation.ThreadSummary, 0, len(searchResult))
	for _, point := range searchResult {
		summary, err := pointToThreadSummary(point)
		if err != nil {
			// ログ出力してスキップ
			continue
		}
		summary.Score = point.Score
		summaries = append(summaries, summary)
	}

	return summaries, nil
}

// SearchByDomain はドメインでThread要約を検索
func (v *VectorDBStore) SearchByDomain(ctx context.Context, domain string, limit int) ([]*conversation.ThreadSummary, error) {
	lim := uint32(limit)
	// Scrollでドメインフィルタリング
	scrollResult, err := v.client.Scroll(ctx, &qdrant.ScrollPoints{
		CollectionName: v.collectionName,
		Filter: &qdrant.Filter{
			Must: []*qdrant.Condition{
				{
					ConditionOneOf: &qdrant.Condition_Field{
						Field: &qdrant.FieldCondition{
							Key: "domain",
							Match: &qdrant.Match{
								MatchValue: &qdrant.Match_Keyword{Keyword: domain},
							},
						},
					},
				},
			},
		},
		Limit:       &lim,
		WithPayload: &qdrant.WithPayloadSelector{SelectorOptions: &qdrant.WithPayloadSelector_Enable{Enable: true}},
		WithVectors: &qdrant.WithVectorsSelector{SelectorOptions: &qdrant.WithVectorsSelector_Enable{Enable: false}},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to search by domain: %w", err)
	}

	// 結果をThreadSummaryに変換
	summaries := make([]*conversation.ThreadSummary, 0, len(scrollResult))
	for _, point := range scrollResult {
		summary, err := retrievedPointToThreadSummary(point)
		if err != nil {
			continue
		}
		summaries = append(summaries, summary)
	}

	return summaries, nil
}

// IsNovelQuery はクエリが新規情報かを判定（類似度ベース）
func (v *VectorDBStore) IsNovelQuery(ctx context.Context, queryEmbedding []float32, threshold float32) (bool, float32, error) {
	if len(queryEmbedding) == 0 {
		return false, 0.0, fmt.Errorf("queryEmbedding is empty")
	}

	// 最類似検索（top 1）
	topSummaries, err := v.SearchSimilar(ctx, queryEmbedding, 1)
	if err != nil {
		return false, 0.0, err
	}

	// 結果なし → 新規情報
	if len(topSummaries) == 0 {
		return true, 0.0, nil
	}

	// SearchSimilar の実スコアを使用（Qdrant Cosine距離: 高いほど類似）
	similarity := topSummaries[0].Score
	isNovel := similarity < threshold

	return isNovel, similarity, nil
}

// pointToThreadSummary はQdrant ScoredPointをThreadSummaryに変換
func pointToThreadSummary(point *qdrant.ScoredPoint) (*conversation.ThreadSummary, error) {
	if point == nil {
		return nil, fmt.Errorf("point is nil")
	}
	payload := point.Payload
	if payload == nil {
		return nil, fmt.Errorf("payload is nil")
	}

	threadID, threadSeq, threadKind, err := threadSummaryIdentityFromPayload(payload)
	if err != nil {
		return nil, err
	}
	summary := &conversation.ThreadSummary{ThreadID: threadID, ThreadSeq: threadSeq, ThreadKind: threadKind}

	// session_id
	if v, ok := payload["session_id"]; ok {
		if strVal, ok := v.GetKind().(*qdrant.Value_StringValue); ok {
			summary.SessionID = strVal.StringValue
		}
	}

	// ts_start
	if v, ok := payload["ts_start"]; ok {
		if intVal, ok := v.GetKind().(*qdrant.Value_IntegerValue); ok {
			summary.StartTime = time.Unix(intVal.IntegerValue, 0)
		}
	}

	// ts_end
	if v, ok := payload["ts_end"]; ok {
		if intVal, ok := v.GetKind().(*qdrant.Value_IntegerValue); ok {
			summary.EndTime = time.Unix(intVal.IntegerValue, 0)
		}
	}

	// domain
	if v, ok := payload["domain"]; ok {
		if strVal, ok := v.GetKind().(*qdrant.Value_StringValue); ok {
			summary.Domain = strVal.StringValue
		}
	}

	// summary
	if v, ok := payload["summary"]; ok {
		if strVal, ok := v.GetKind().(*qdrant.Value_StringValue); ok {
			summary.Summary = strVal.StringValue
		}
	}

	// is_novel
	if v, ok := payload["is_novel"]; ok {
		if boolVal, ok := v.GetKind().(*qdrant.Value_BoolValue); ok {
			summary.IsNovel = boolVal.BoolValue
		}
	}

	// keywords
	if v, ok := payload["keywords"]; ok {
		if listVal, ok := v.GetKind().(*qdrant.Value_ListValue); ok {
			keywords := make([]string, 0, len(listVal.ListValue.Values))
			for _, kw := range listVal.ListValue.Values {
				if strVal, ok := kw.GetKind().(*qdrant.Value_StringValue); ok {
					keywords = append(keywords, strVal.StringValue)
				}
			}
			summary.Keywords = keywords
		}
	}

	return summary, nil
}

// retrievedPointToThreadSummary はQdrant RetrievedPointをThreadSummaryに変換
func retrievedPointToThreadSummary(point *qdrant.RetrievedPoint) (*conversation.ThreadSummary, error) {
	if point == nil {
		return nil, fmt.Errorf("point is nil")
	}
	payload := point.Payload
	if payload == nil {
		return nil, fmt.Errorf("payload is nil")
	}

	threadID, threadSeq, threadKind, err := threadSummaryIdentityFromPayload(payload)
	if err != nil {
		return nil, err
	}
	summary := &conversation.ThreadSummary{ThreadID: threadID, ThreadSeq: threadSeq, ThreadKind: threadKind}

	// session_id
	if v, ok := payload["session_id"]; ok {
		if strVal, ok := v.GetKind().(*qdrant.Value_StringValue); ok {
			summary.SessionID = strVal.StringValue
		}
	}

	// ts_start
	if v, ok := payload["ts_start"]; ok {
		if intVal, ok := v.GetKind().(*qdrant.Value_IntegerValue); ok {
			summary.StartTime = time.Unix(intVal.IntegerValue, 0)
		}
	}

	// ts_end
	if v, ok := payload["ts_end"]; ok {
		if intVal, ok := v.GetKind().(*qdrant.Value_IntegerValue); ok {
			summary.EndTime = time.Unix(intVal.IntegerValue, 0)
		}
	}

	// domain
	if v, ok := payload["domain"]; ok {
		if strVal, ok := v.GetKind().(*qdrant.Value_StringValue); ok {
			summary.Domain = strVal.StringValue
		}
	}

	// summary
	if v, ok := payload["summary"]; ok {
		if strVal, ok := v.GetKind().(*qdrant.Value_StringValue); ok {
			summary.Summary = strVal.StringValue
		}
	}

	// is_novel
	if v, ok := payload["is_novel"]; ok {
		if boolVal, ok := v.GetKind().(*qdrant.Value_BoolValue); ok {
			summary.IsNovel = boolVal.BoolValue
		}
	}

	// keywords
	if v, ok := payload["keywords"]; ok {
		if listVal, ok := v.GetKind().(*qdrant.Value_ListValue); ok {
			keywords := make([]string, 0, len(listVal.ListValue.Values))
			for _, kw := range listVal.ListValue.Values {
				if strVal, ok := kw.GetKind().(*qdrant.Value_StringValue); ok {
					keywords = append(keywords, strVal.StringValue)
				}
			}
			summary.Keywords = keywords
		}
	}

	return summary, nil
}

func validateThreadSummaryForSave(summary *conversation.ThreadSummary) error {
	if summary == nil {
		return fmt.Errorf("thread summary is required")
	}
	if err := summary.ThreadID.Validate(); err != nil {
		return fmt.Errorf("thread_id: %w", err)
	}
	if err := summary.ThreadSeq.Validate(); err != nil {
		return fmt.Errorf("thread_seq: %w", err)
	}
	if err := summary.ThreadKind.Validate(); err != nil {
		return fmt.Errorf("thread_kind: %w", err)
	}
	if err := modulecore.SessionID(summary.SessionID).Validate(); err != nil {
		return fmt.Errorf("session_id: %w", err)
	}
	if len(summary.Embedding) == 0 {
		return fmt.Errorf("embedding is required for VectorDB storage")
	}
	return nil
}

func threadSummaryIdentityFromPayload(payload map[string]*qdrant.Value) (modulecore.ThreadID, conversation.ThreadSeq, conversation.ThreadKind, error) {
	threadIDValue, ok := payload["thread_id"]
	if !ok || threadIDValue == nil {
		return "", 0, "", fmt.Errorf("thread_id payload is required")
	}
	threadIDString, ok := threadIDValue.GetKind().(*qdrant.Value_StringValue)
	if !ok {
		return "", 0, "", fmt.Errorf("thread_id payload must be a string")
	}
	threadID := modulecore.ThreadID(threadIDString.StringValue)
	if err := threadID.Validate(); err != nil {
		return "", 0, "", fmt.Errorf("thread_id payload: %w", err)
	}

	threadSeqValue, ok := payload["thread_seq"]
	if !ok || threadSeqValue == nil {
		return "", 0, "", fmt.Errorf("thread_seq payload is required")
	}
	threadSeqInteger, ok := threadSeqValue.GetKind().(*qdrant.Value_IntegerValue)
	if !ok {
		return "", 0, "", fmt.Errorf("thread_seq payload must be an integer")
	}
	threadSeq := modulecore.ThreadSeq(threadSeqInteger.IntegerValue)
	if err := threadSeq.Validate(); err != nil {
		return "", 0, "", fmt.Errorf("thread_seq payload: %w", err)
	}

	threadKindValue, ok := payload["thread_kind"]
	if !ok || threadKindValue == nil {
		return "", 0, "", fmt.Errorf("thread_kind payload is required")
	}
	threadKindString, ok := threadKindValue.GetKind().(*qdrant.Value_StringValue)
	if !ok {
		return "", 0, "", fmt.Errorf("thread_kind payload must be a string")
	}
	threadKind := modulecore.ThreadKind(threadKindString.StringValue)
	if err := threadKind.Validate(); err != nil {
		return "", 0, "", fmt.Errorf("thread_kind payload: %w", err)
	}
	return threadID, conversation.ThreadSeq(threadSeq), conversation.ThreadKind(threadKind), nil
}
