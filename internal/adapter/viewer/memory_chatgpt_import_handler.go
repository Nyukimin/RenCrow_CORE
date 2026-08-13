package viewer

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
)

type ChatGPTL3Importer interface {
	ImportChatGPTL3Records(context.Context, []l1sqlite.ChatGPTL3ImportRecord, bool) (l1sqlite.ChatGPTL3ImportResult, error)
	ConfirmChatGPTL3Candidates(context.Context, string, string, bool) (l1sqlite.ChatGPTL3ConfirmResult, error)
}

func HandleChatGPTL3Import(store ChatGPTL3Importer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireViewerMethod(w, r, http.MethodPost) {
			return
		}
		if !requireViewerStore(w, store == nil, "ChatGPT L3 importer unavailable") {
			return
		}
		var request struct {
			Apply   bool `json:"apply"`
			Records []struct {
				Format                string           `json:"format"`
				ExportID              string           `json:"export_id"`
				EvidenceID            string           `json:"evidence_id"`
				ConversationID        string           `json:"conversation_id"`
				ConversationTitle     string           `json:"conversation_title"`
				ConversationCreatedAt optionalJSONTime `json:"conversation_created_at"`
				ConversationUpdatedAt optionalJSONTime `json:"conversation_updated_at"`
				NodeID                string           `json:"node_id"`
				ParentNodeID          string           `json:"parent_node_id"`
				ChildNodeIDs          []string         `json:"child_node_ids"`
				OnCurrentBranch       bool             `json:"on_current_branch"`
				MessageID             string           `json:"message_id"`
				MessageCreatedAt      optionalJSONTime `json:"message_created_at"`
				Role                  string           `json:"role"`
				ContentType           string           `json:"content_type"`
				Text                  string           `json:"text"`
				Content               json.RawMessage  `json:"content"`
				Metadata              json.RawMessage  `json:"metadata"`
			} `json:"records"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&request); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		records := make([]l1sqlite.ChatGPTL3ImportRecord, 0, len(request.Records))
		for _, item := range request.Records {
			records = append(records, l1sqlite.ChatGPTL3ImportRecord{
				Format: item.Format, ExportID: item.ExportID, EvidenceID: item.EvidenceID,
				ConversationID: item.ConversationID, ConversationTitle: item.ConversationTitle,
				ConversationCreatedAt: item.ConversationCreatedAt.Time, ConversationUpdatedAt: item.ConversationUpdatedAt.Time,
				NodeID: item.NodeID, ParentNodeID: item.ParentNodeID, ChildNodeIDs: item.ChildNodeIDs,
				OnCurrentBranch: item.OnCurrentBranch, MessageID: item.MessageID, MessageCreatedAt: item.MessageCreatedAt.Time,
				Role: item.Role, ContentType: item.ContentType, Text: item.Text, Content: item.Content, Metadata: item.Metadata,
			})
		}
		result, err := store.ImportChatGPTL3Records(r.Context(), records, request.Apply)
		if err != nil {
			http.Error(w, "ChatGPT L3 import rejected: "+err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"status": "ok", "applied": request.Apply, "result": result})
	}
}

func HandleChatGPTL3Confirm(store ChatGPTL3Importer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireViewerMethod(w, r, http.MethodPost) {
			return
		}
		if !requireViewerStore(w, store == nil, "ChatGPT L3 importer unavailable") {
			return
		}
		var request struct {
			ExportID string `json:"export_id"`
			Reason   string `json:"reason"`
			Apply    bool   `json:"apply"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&request); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		result, err := store.ConfirmChatGPTL3Candidates(r.Context(), request.ExportID, request.Reason, request.Apply)
		if err != nil {
			http.Error(w, "ChatGPT L3 confirmation rejected: "+err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"status": "ok", "applied": request.Apply, "result": result})
	}
}

type optionalJSONTime struct{ time.Time }

func (value *optionalJSONTime) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if raw == "" {
		value.Time = time.Time{}
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return err
	}
	value.Time = parsed
	return nil
}
