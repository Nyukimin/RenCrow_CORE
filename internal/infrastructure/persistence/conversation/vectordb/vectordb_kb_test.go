package vectordb

import (
	"context"
	"strings"
	"testing"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/qdrant/go-client/qdrant"
)

func TestVectorDBStore_SaveKBRejectsMalformedDocument(t *testing.T) {
	ctx := context.Background()
	store := &VectorDBStore{}
	now := time.Date(2026, 5, 20, 9, 10, 0, 0, time.UTC)
	valid := func() *domconv.Document {
		return &domconv.Document{
			ID:        "doc-1",
			Domain:    "movie",
			Content:   "knowledge content",
			Source:    "https://example.com/doc/1",
			Embedding: []float32{0.1, 0.2, 0.3},
			Meta:      map[string]interface{}{},
			CreatedAt: now,
			UpdatedAt: now,
		}
	}
	tests := []struct {
		name   string
		doc    *domconv.Document
		mutate func(*domconv.Document)
		want   string
	}{
		{name: "nil", doc: nil, want: "kb document is required"},
		{name: "missing id", mutate: func(doc *domconv.Document) {
			doc.ID = ""
		}, want: "id is required"},
		{name: "missing domain", mutate: func(doc *domconv.Document) {
			doc.Domain = ""
		}, want: "domain is required"},
		{name: "missing content", mutate: func(doc *domconv.Document) {
			doc.Content = "   "
		}, want: "content is required"},
		{name: "missing created at", mutate: func(doc *domconv.Document) {
			doc.CreatedAt = time.Time{}
		}, want: "created_at is required"},
		{name: "missing updated at", mutate: func(doc *domconv.Document) {
			doc.UpdatedAt = time.Time{}
		}, want: "updated_at is required"},
		{name: "missing embedding", mutate: func(doc *domconv.Document) {
			doc.Embedding = nil
		}, want: "embedding is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := tt.doc
			if doc == nil && tt.mutate != nil {
				doc = valid()
			}
			if tt.mutate != nil {
				tt.mutate(doc)
			}
			err := store.SaveKB(ctx, doc)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("SaveKB() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestVectorDBStore_PointToDocumentRejectsMalformedPayload(t *testing.T) {
	now := time.Date(2026, 5, 20, 9, 10, 0, 0, time.UTC).Unix()
	validPayload := func() map[string]*qdrant.Value {
		return map[string]*qdrant.Value{
			"content": {
				Kind: &qdrant.Value_StringValue{StringValue: "knowledge content"},
			},
			"source": {
				Kind: &qdrant.Value_StringValue{StringValue: "https://example.com/doc/1"},
			},
			"created_at": {
				Kind: &qdrant.Value_IntegerValue{IntegerValue: now},
			},
			"updated_at": {
				Kind: &qdrant.Value_IntegerValue{IntegerValue: now},
			},
		}
	}
	tests := []struct {
		name   string
		mutate func(map[string]*qdrant.Value)
		want   string
	}{
		{name: "missing content", mutate: func(payload map[string]*qdrant.Value) {
			delete(payload, "content")
		}, want: "content is required"},
		{name: "missing created at", mutate: func(payload map[string]*qdrant.Value) {
			delete(payload, "created_at")
		}, want: "created_at is required"},
		{name: "missing updated at", mutate: func(payload map[string]*qdrant.Value) {
			delete(payload, "updated_at")
		}, want: "updated_at is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := validPayload()
			tt.mutate(payload)
			_, err := retrievedPointToDocument(&qdrant.RetrievedPoint{
				Id: &qdrant.PointId{
					PointIdOptions: &qdrant.PointId_Uuid{Uuid: "doc-1"},
				},
				Payload: payload,
			}, "movie")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("retrievedPointToDocument() error = %v, want %q", err, tt.want)
			}
		})
	}
}
