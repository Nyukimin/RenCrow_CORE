package conversation

import (
	"context"
	"fmt"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/conversation"
	"github.com/google/uuid"
	"github.com/qdrant/go-client/qdrant"
)

// SaveThreadSummary はThread要約をVectorDBに保存
func (v *VectorDBStore) SaveThreadSummary(ctx context.Context, summary *conversation.ThreadSummary) error {
	if len(summary.Embedding) == 0 {
		return fmt.Errorf("embedding is required for VectorDB storage")
	}

	// Qdrant Point作成
	pointID := uuid.New().String()
	point := &qdrant.PointStruct{
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
		Payload: map[string]*qdrant.Value{
			"thread_id": {
				Kind: &qdrant.Value_IntegerValue{IntegerValue: summary.ThreadID},
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
		point.Payload["keywords"] = &qdrant.Value{
			Kind: &qdrant.Value_ListValue{
				ListValue: &qdrant.ListValue{Values: keywordsList},
			},
		}
	}

	// Upsert（Wait=trueで同期書き込み）
	waitTrue := true
	_, err := v.client.Upsert(ctx, &qdrant.UpsertPoints{
		CollectionName: v.collectionName,
		Points:         []*qdrant.PointStruct{point},
		Wait:           &waitTrue,
	})
	if err != nil {
		return fmt.Errorf("failed to upsert point to vectordb: %w", err)
	}

	return nil
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
	payload := point.Payload
	if payload == nil {
		return nil, fmt.Errorf("payload is nil")
	}

	summary := &conversation.ThreadSummary{}

	// thread_id
	if v, ok := payload["thread_id"]; ok {
		if intVal, ok := v.GetKind().(*qdrant.Value_IntegerValue); ok {
			summary.ThreadID = intVal.IntegerValue
		}
	}

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
	payload := point.Payload
	if payload == nil {
		return nil, fmt.Errorf("payload is nil")
	}

	summary := &conversation.ThreadSummary{}

	// thread_id
	if v, ok := payload["thread_id"]; ok {
		if intVal, ok := v.GetKind().(*qdrant.Value_IntegerValue); ok {
			summary.ThreadID = intVal.IntegerValue
		}
	}

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
