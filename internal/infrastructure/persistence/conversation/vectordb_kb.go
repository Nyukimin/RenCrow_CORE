package conversation

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/conversation"
	"github.com/google/uuid"
	"github.com/qdrant/go-client/qdrant"
)

// getKBCollectionName はドメインごとのKBコレクション名を返す
func (v *VectorDBStore) getKBCollectionName(domain string) string {
	return fmt.Sprintf("kb_%s", domain)
}

// initKBCollection はKBコレクションを初期化
func (v *VectorDBStore) initKBCollection(ctx context.Context, domain string, vectorSize uint64) error {
	collectionName := v.getKBCollectionName(domain)
	if vectorSize == 0 {
		return fmt.Errorf("kb collection vector size is required")
	}

	// コレクション存在確認
	exists, err := v.client.CollectionExists(ctx, collectionName)
	if err != nil {
		return fmt.Errorf("failed to check kb collection existence: %w", err)
	}

	if exists {
		return nil
	}

	// コレクション作成。KBはproviderごとにembedding次元が異なるため、保存対象の次元に合わせる。
	err = v.client.CreateCollection(ctx, &qdrant.CreateCollection{
		CollectionName: collectionName,
		VectorsConfig: qdrant.NewVectorsConfig(&qdrant.VectorParams{
			Size:     vectorSize,
			Distance: qdrant.Distance_Cosine,
		}),
	})
	if err != nil {
		return fmt.Errorf("failed to create kb collection: %w", err)
	}

	// Payloadインデックス作成（source、created_at）
	_, err = v.client.CreateFieldIndex(ctx, &qdrant.CreateFieldIndexCollection{
		CollectionName: collectionName,
		FieldName:      "source",
		FieldType:      qdrant.FieldType_FieldTypeKeyword.Enum(),
	})
	if err != nil {
		return fmt.Errorf("failed to create source index: %w", err)
	}

	return nil
}

// SaveKB はKnowledge BaseにDocumentを保存
func (v *VectorDBStore) SaveKB(ctx context.Context, doc *conversation.Document) error {
	if err := validateKBDocumentForSave(doc); err != nil {
		return err
	}

	// KBコレクション初期化
	if err := v.initKBCollection(ctx, doc.Domain, uint64(len(doc.Embedding))); err != nil {
		return err
	}

	collectionName := v.getKBCollectionName(doc.Domain)

	// Qdrant Point作成
	point := &qdrant.PointStruct{
		Id: &qdrant.PointId{
			PointIdOptions: &qdrant.PointId_Uuid{Uuid: uuid.NewSHA1(uuid.NameSpaceURL, []byte(doc.ID)).String()},
		},
		Vectors: &qdrant.Vectors{
			VectorsOptions: &qdrant.Vectors_Vector{
				Vector: &qdrant.Vector{
					Data: doc.Embedding,
				},
			},
		},
		Payload: map[string]*qdrant.Value{
			"id": {
				Kind: &qdrant.Value_StringValue{StringValue: doc.ID},
			},
			"content": {
				Kind: &qdrant.Value_StringValue{StringValue: doc.Content},
			},
			"source": {
				Kind: &qdrant.Value_StringValue{StringValue: doc.Source},
			},
			"created_at": {
				Kind: &qdrant.Value_IntegerValue{IntegerValue: doc.CreatedAt.Unix()},
			},
			"updated_at": {
				Kind: &qdrant.Value_IntegerValue{IntegerValue: doc.UpdatedAt.Unix()},
			},
		},
	}

	// Meta情報をPayloadに追加
	for key, value := range doc.Meta {
		if strVal, ok := value.(string); ok {
			point.Payload[key] = &qdrant.Value{
				Kind: &qdrant.Value_StringValue{StringValue: strVal},
			}
		}
	}

	// Upsert（Wait=trueで同期書き込み）
	waitTrue := true
	_, err := v.client.Upsert(ctx, &qdrant.UpsertPoints{
		CollectionName: collectionName,
		Points:         []*qdrant.PointStruct{point},
		Wait:           &waitTrue,
	})
	if err != nil {
		return fmt.Errorf("failed to upsert kb document: %w", err)
	}

	return nil
}

// SearchKB はKnowledge BaseからDocumentを検索
func (v *VectorDBStore) SearchKB(ctx context.Context, domain string, queryEmbedding []float32, topK int) ([]*conversation.Document, error) {
	if len(queryEmbedding) == 0 {
		return nil, fmt.Errorf("queryEmbedding is empty")
	}

	collectionName := v.getKBCollectionName(domain)

	// コレクション存在確認
	exists, err := v.client.CollectionExists(ctx, collectionName)
	if err != nil {
		return nil, fmt.Errorf("failed to check kb collection existence: %w", err)
	}
	if !exists {
		// コレクションが存在しない場合は空の結果を返す
		return []*conversation.Document{}, nil
	}

	limit := uint64(topK)
	// ベクトル検索
	searchResult, err := v.client.Query(ctx, &qdrant.QueryPoints{
		CollectionName: collectionName,
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
		return nil, fmt.Errorf("failed to search kb: %w", err)
	}

	// 結果をDocumentに変換
	docs := make([]*conversation.Document, 0, len(searchResult))
	for _, point := range searchResult {
		doc, err := pointToDocument(point, domain)
		if err != nil {
			// ログ出力してスキップ
			continue
		}
		doc.Score = point.Score
		docs = append(docs, doc)
	}

	return docs, nil
}

// pointToDocument はQdrant ScoredPointをDocumentに変換
func pointToDocument(point *qdrant.ScoredPoint, domain string) (*conversation.Document, error) {
	payload := point.Payload
	if payload == nil {
		return nil, fmt.Errorf("payload is nil")
	}

	doc := &conversation.Document{
		ID:     qdrantDocumentID(point.Id, payload),
		Domain: domain,
		Meta:   make(map[string]interface{}),
	}

	// content
	if v, ok := payload["content"]; ok {
		if strVal, ok := v.GetKind().(*qdrant.Value_StringValue); ok {
			doc.Content = strVal.StringValue
		}
	}

	// source
	if v, ok := payload["source"]; ok {
		if strVal, ok := v.GetKind().(*qdrant.Value_StringValue); ok {
			doc.Source = strVal.StringValue
		}
	}

	// created_at
	if v, ok := payload["created_at"]; ok {
		if intVal, ok := v.GetKind().(*qdrant.Value_IntegerValue); ok {
			doc.CreatedAt = time.Unix(intVal.IntegerValue, 0)
		}
	}

	// updated_at
	if v, ok := payload["updated_at"]; ok {
		if intVal, ok := v.GetKind().(*qdrant.Value_IntegerValue); ok {
			doc.UpdatedAt = time.Unix(intVal.IntegerValue, 0)
		}
	}

	// その他のメタ情報
	for key, value := range payload {
		if key == "id" || key == "content" || key == "source" || key == "created_at" || key == "updated_at" {
			continue
		}
		if strVal, ok := value.GetKind().(*qdrant.Value_StringValue); ok {
			doc.Meta[key] = strVal.StringValue
		}
	}

	if err := validateKBDocumentForRead(doc); err != nil {
		return nil, err
	}
	return doc, nil
}

// ListKBDocuments はKBコレクション内の全ドキュメントを取得（ページング対応）
func (v *VectorDBStore) ListKBDocuments(ctx context.Context, domain string, limit int) ([]*conversation.Document, error) {
	collectionName := v.getKBCollectionName(domain)

	// コレクション存在確認
	exists, err := v.client.CollectionExists(ctx, collectionName)
	if err != nil {
		return nil, fmt.Errorf("failed to check kb collection existence: %w", err)
	}
	if !exists {
		return []*conversation.Document{}, nil
	}

	lim := uint32(limit)

	// Scroll でドキュメント取得（フィルタなし）
	scrollResult, err := v.client.Scroll(ctx, &qdrant.ScrollPoints{
		CollectionName: collectionName,
		Limit:          &lim,
		WithPayload:    &qdrant.WithPayloadSelector{SelectorOptions: &qdrant.WithPayloadSelector_Enable{Enable: true}},
		WithVectors:    &qdrant.WithVectorsSelector{SelectorOptions: &qdrant.WithVectorsSelector_Enable{Enable: false}},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list kb documents: %w", err)
	}

	// 結果をDocumentに変換
	docs := make([]*conversation.Document, 0, len(scrollResult))
	for _, point := range scrollResult {
		doc, err := retrievedPointToDocument(point, domain)
		if err != nil {
			continue
		}
		docs = append(docs, doc)
	}

	return docs, nil
}

// retrievedPointToDocument はQdrant RetrievedPointをDocumentに変換
func retrievedPointToDocument(point *qdrant.RetrievedPoint, domain string) (*conversation.Document, error) {
	payload := point.Payload
	if payload == nil {
		return nil, fmt.Errorf("payload is nil")
	}

	doc := &conversation.Document{
		ID:     qdrantDocumentID(point.Id, payload),
		Domain: domain,
		Meta:   make(map[string]interface{}),
	}

	// content
	if v, ok := payload["content"]; ok {
		if strVal, ok := v.GetKind().(*qdrant.Value_StringValue); ok {
			doc.Content = strVal.StringValue
		}
	}

	// source
	if v, ok := payload["source"]; ok {
		if strVal, ok := v.GetKind().(*qdrant.Value_StringValue); ok {
			doc.Source = strVal.StringValue
		}
	}

	// created_at
	if v, ok := payload["created_at"]; ok {
		if intVal, ok := v.GetKind().(*qdrant.Value_IntegerValue); ok {
			doc.CreatedAt = time.Unix(intVal.IntegerValue, 0)
		}
	}

	// updated_at
	if v, ok := payload["updated_at"]; ok {
		if intVal, ok := v.GetKind().(*qdrant.Value_IntegerValue); ok {
			doc.UpdatedAt = time.Unix(intVal.IntegerValue, 0)
		}
	}

	// その他のメタ情報
	for key, value := range payload {
		if key == "id" || key == "content" || key == "source" || key == "created_at" || key == "updated_at" {
			continue
		}
		if strVal, ok := value.GetKind().(*qdrant.Value_StringValue); ok {
			doc.Meta[key] = strVal.StringValue
		}
	}

	if err := validateKBDocumentForRead(doc); err != nil {
		return nil, err
	}
	return doc, nil
}

func qdrantDocumentID(id *qdrant.PointId, payload map[string]*qdrant.Value) string {
	if v, ok := payload["id"]; ok {
		if strVal, ok := v.GetKind().(*qdrant.Value_StringValue); ok {
			return strVal.StringValue
		}
	}
	if id != nil {
		if uuidID, ok := id.GetPointIdOptions().(*qdrant.PointId_Uuid); ok {
			return uuidID.Uuid
		}
	}
	return ""
}

func validateKBDocumentForSave(doc *conversation.Document) error {
	if doc == nil {
		return fmt.Errorf("kb document is required")
	}
	if err := validateKBDocumentForRead(doc); err != nil {
		return err
	}
	if len(doc.Embedding) == 0 {
		return fmt.Errorf("kb document embedding is required")
	}
	return nil
}

func validateKBDocumentForRead(doc *conversation.Document) error {
	if doc == nil {
		return fmt.Errorf("kb document is required")
	}
	if strings.TrimSpace(doc.ID) == "" {
		return fmt.Errorf("kb document id is required")
	}
	if strings.TrimSpace(doc.Domain) == "" {
		return fmt.Errorf("kb document domain is required")
	}
	if strings.TrimSpace(doc.Content) == "" {
		return fmt.Errorf("kb document content is required")
	}
	if doc.CreatedAt.IsZero() {
		return fmt.Errorf("kb document created_at is required")
	}
	if doc.UpdatedAt.IsZero() {
		return fmt.Errorf("kb document updated_at is required")
	}
	return nil
}

// GetKBCollections は存在するKBコレクション一覧を取得
// NOTE: Qdrant Go client のListCollections APIが不安定なため、
// 既知のドメインリストから存在確認する簡易実装
func (v *VectorDBStore) GetKBCollections(ctx context.Context) ([]string, error) {
	// 既知のドメインリスト（実運用では設定から読み込むべき）
	knownDomains := []string{"general", "programming", "movie", "anime", "tech", "history"}

	existingDomains := make([]string, 0)
	for _, domain := range knownDomains {
		collectionName := v.getKBCollectionName(domain)
		exists, err := v.client.CollectionExists(ctx, collectionName)
		if err != nil {
			continue
		}
		if exists {
			existingDomains = append(existingDomains, domain)
		}
	}

	return existingDomains, nil
}

// GetKBStats はKBコレクションの統計情報を取得
func (v *VectorDBStore) GetKBStats(ctx context.Context, domain string) (*KBStats, error) {
	collectionName := v.getKBCollectionName(domain)

	// コレクション存在確認
	exists, err := v.client.CollectionExists(ctx, collectionName)
	if err != nil {
		return nil, fmt.Errorf("failed to check collection existence: %w", err)
	}
	if !exists {
		return &KBStats{Domain: domain, DocumentCount: 0, VectorSize: 768}, nil
	}

	// ドキュメント数をカウント（Scroll で取得して数える簡易実装）
	limit := uint32(1000)
	scrollResult, err := v.client.Scroll(ctx, &qdrant.ScrollPoints{
		CollectionName: collectionName,
		Limit:          &limit,
		WithPayload:    &qdrant.WithPayloadSelector{SelectorOptions: &qdrant.WithPayloadSelector_Enable{Enable: false}},
		WithVectors:    &qdrant.WithVectorsSelector{SelectorOptions: &qdrant.WithVectorsSelector_Enable{Enable: false}},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to count documents: %w", err)
	}

	return &KBStats{
		Domain:        domain,
		DocumentCount: len(scrollResult),
		VectorSize:    768, // 固定値（設定から読み込むべき）
	}, nil
}

// KBStats はKBコレクションの統計情報
type KBStats struct {
	Domain        string
	DocumentCount int
	VectorSize    int
}

// DeleteOldKBDocuments は指定日時より古いKBドキュメントを削除
func (v *VectorDBStore) DeleteOldKBDocuments(ctx context.Context, domain string, before time.Time) (int, error) {
	collectionName := v.getKBCollectionName(domain)

	// コレクション存在確認
	exists, err := v.client.CollectionExists(ctx, collectionName)
	if err != nil {
		return 0, fmt.Errorf("failed to check collection existence: %w", err)
	}
	if !exists {
		return 0, nil
	}

	// created_at < before のドキュメントを削除
	beforeUnix := before.Unix()
	beforeFloat := float64(beforeUnix)
	_, err = v.client.Delete(ctx, &qdrant.DeletePoints{
		CollectionName: collectionName,
		Points: &qdrant.PointsSelector{
			PointsSelectorOneOf: &qdrant.PointsSelector_Filter{
				Filter: &qdrant.Filter{
					Must: []*qdrant.Condition{
						{
							ConditionOneOf: &qdrant.Condition_Field{
								Field: &qdrant.FieldCondition{
									Key: "created_at",
									Range: &qdrant.Range{
										Lt: &beforeFloat,
									},
								},
							},
						},
					},
				},
			},
		},
	})
	if err != nil {
		return 0, fmt.Errorf("failed to delete old documents: %w", err)
	}

	// 削除数は正確には取得できないため、簡易実装として0を返す
	// 実運用では削除前後でカウントして差分を返すべき
	return 0, nil
}
