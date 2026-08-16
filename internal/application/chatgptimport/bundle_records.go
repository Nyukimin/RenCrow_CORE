package chatgptimport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"
)

var conversationFilePattern = regexp.MustCompile(`^conversations-\d{3}\.json$`)

type artifactRecord struct {
	Format                string          `json:"format"`
	ExportID              string          `json:"export_id"`
	EvidenceID            string          `json:"evidence_id"`
	ConversationID        string          `json:"conversation_id"`
	ConversationTitle     string          `json:"conversation_title"`
	ConversationCreatedAt string          `json:"conversation_created_at,omitempty"`
	ConversationUpdatedAt string          `json:"conversation_updated_at,omitempty"`
	NodeID                string          `json:"node_id"`
	ParentNodeID          string          `json:"parent_node_id,omitempty"`
	ChildNodeIDs          []string        `json:"child_node_ids,omitempty"`
	OnCurrentBranch       bool            `json:"on_current_branch"`
	MessageID             string          `json:"message_id"`
	MessageCreatedAt      string          `json:"message_created_at,omitempty"`
	Role                  string          `json:"role"`
	ContentType           string          `json:"content_type"`
	Text                  string          `json:"text"`
	Content               json.RawMessage `json:"content"`
	Metadata              json.RawMessage `json:"metadata,omitempty"`
}

var recordFields = []string{
	"format", "export_id", "evidence_id", "conversation_id", "conversation_title", "conversation_created_at",
	"conversation_updated_at", "node_id", "parent_node_id", "child_node_ids", "on_current_branch", "message_id",
	"message_created_at", "role", "content_type", "text", "content", "metadata",
}

var requiredRecordFields = []string{
	"format", "export_id", "evidence_id", "conversation_id", "conversation_title", "node_id", "on_current_branch",
	"message_id", "role", "content_type", "text", "content",
}

type recordCounts struct {
	Records           int
	UserMessages      int
	AssistantMessages int
	Conversations     map[string]struct{}
}

func decodeArtifactRecord(data []byte, exportID string) (artifactRecord, error) {
	if err := validateJSONObject(data, recordFields, requiredRecordFields); err != nil {
		return artifactRecord{}, err
	}
	var value artifactRecord
	if err := decodeStrictJSON(data, &value); err != nil {
		return artifactRecord{}, err
	}
	canonical, err := json.Marshal(value)
	if err != nil || !bytes.Equal(canonical, data) {
		return artifactRecord{}, errors.New("artifact record is not canonical JSON")
	}
	expectedEvidenceID := "chatgpt_export:" + value.ConversationID + ":" + value.MessageID
	if value.Format != RecordSchema || value.ExportID != exportID || value.EvidenceID != expectedEvidenceID {
		return artifactRecord{}, errors.New("artifact record format, export, or evidence is invalid")
	}
	if value.ConversationID == "" || value.NodeID == "" || value.MessageID == "" || len(value.Content) == 0 || !json.Valid(value.Content) || bytes.Equal(value.Content, []byte("null")) {
		return artifactRecord{}, errors.New("artifact record required value is invalid")
	}
	switch value.Role {
	case "user", "assistant", "system", "tool":
	default:
		return artifactRecord{}, errors.New("artifact record role is unsupported")
	}
	return value, nil
}

type exportConversation struct {
	ID         string                      `json:"id"`
	Title      string                      `json:"title"`
	CreateTime float64                     `json:"create_time"`
	UpdateTime float64                     `json:"update_time"`
	Current    string                      `json:"current_node"`
	Mapping    map[string]conversationNode `json:"mapping"`
}

type conversationNode struct {
	ID       string          `json:"id"`
	Parent   *string         `json:"parent"`
	Children []string        `json:"children"`
	Message  json.RawMessage `json:"message"`
}

type exportMessage struct {
	ID     string `json:"id"`
	Author struct {
		Role string `json:"role"`
	} `json:"author"`
	CreateTime float64         `json:"create_time"`
	Content    json.RawMessage `json:"content"`
	Metadata   json.RawMessage `json:"metadata"`
}

type exportContent struct {
	ContentType string            `json:"content_type"`
	Parts       []json.RawMessage `json:"parts"`
	Text        string            `json:"text"`
}

func parseConversationArray(ctx context.Context, reader io.Reader, callback func(exportConversation) error) error {
	decoder := json.NewDecoder(&contextReader{ctx: ctx, reader: reader})
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '[' {
		return errors.New("conversation file must contain a JSON array")
	}
	for decoder.More() {
		if err := ctx.Err(); err != nil {
			return err
		}
		var conversation exportConversation
		if err := decoder.Decode(&conversation); err != nil {
			return err
		}
		if conversation.ID == "" || conversation.Mapping == nil {
			return errors.New("conversation is missing an ID or mapping")
		}
		if err := callback(conversation); err != nil {
			return err
		}
	}
	token, err = decoder.Token()
	if err != nil {
		return err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != ']' {
		return errors.New("conversation file JSON array is not closed")
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("conversation file contains trailing JSON")
		}
		return err
	}
	return nil
}

func deriveConversationRecords(exportID string, conversation exportConversation, callback func(artifactRecord) error) error {
	branch := currentBranch(conversation)
	nodeIDs := make([]string, 0, len(conversation.Mapping))
	for nodeID := range conversation.Mapping {
		nodeIDs = append(nodeIDs, nodeID)
	}
	sort.Strings(nodeIDs)
	for _, nodeID := range nodeIDs {
		node := conversation.Mapping[nodeID]
		if len(node.Message) == 0 || bytes.Equal(node.Message, []byte("null")) {
			continue
		}
		record, err := recordFromNode(exportID, conversation, nodeID, node, branch)
		if err != nil {
			return err
		}
		if err := callback(record); err != nil {
			return err
		}
	}
	return nil
}

func recordFromNode(exportID string, conversation exportConversation, nodeID string, node conversationNode, branch map[string]bool) (artifactRecord, error) {
	if nodeID == "" {
		return artifactRecord{}, errors.New("conversation node ID is empty")
	}
	var message exportMessage
	if err := json.Unmarshal(node.Message, &message); err != nil {
		return artifactRecord{}, err
	}
	if message.ID == "" {
		message.ID = nodeID
	}
	switch message.Author.Role {
	case "user", "assistant", "system", "tool":
	default:
		return artifactRecord{}, errors.New("conversation message role is unsupported")
	}
	if len(message.Content) == 0 || !json.Valid(message.Content) || bytes.Equal(message.Content, []byte("null")) {
		return artifactRecord{}, errors.New("conversation message content is missing or invalid")
	}
	parent := ""
	if node.Parent != nil {
		parent = *node.Parent
	}
	text, contentType := contentText(message.Content)
	return artifactRecord{
		Format: RecordSchema, ExportID: exportID, EvidenceID: "chatgpt_export:" + conversation.ID + ":" + message.ID,
		ConversationID: conversation.ID, ConversationTitle: conversation.Title,
		ConversationCreatedAt: timestamp(conversation.CreateTime), ConversationUpdatedAt: timestamp(conversation.UpdateTime),
		NodeID: nodeID, ParentNodeID: parent, ChildNodeIDs: append([]string(nil), node.Children...), OnCurrentBranch: branch[nodeID],
		MessageID: message.ID, MessageCreatedAt: timestamp(message.CreateTime), Role: message.Author.Role,
		ContentType: contentType, Text: text, Content: message.Content, Metadata: message.Metadata,
	}, nil
}

func currentBranch(conversation exportConversation) map[string]bool {
	result := make(map[string]bool)
	for nodeID := conversation.Current; nodeID != "" && !result[nodeID]; {
		result[nodeID] = true
		node, exists := conversation.Mapping[nodeID]
		if !exists || node.Parent == nil {
			break
		}
		nodeID = *node.Parent
	}
	return result
}

func timestamp(value float64) string {
	if value <= 0 {
		return ""
	}
	seconds := int64(value)
	fraction := value - float64(seconds)
	return time.Unix(seconds, int64(fraction*1e9)).UTC().Format(time.RFC3339Nano)
}

func contentText(raw json.RawMessage) (string, string) {
	var content exportContent
	if len(raw) == 0 || json.Unmarshal(raw, &content) != nil {
		return "", ""
	}
	var texts []string
	if strings.TrimSpace(content.Text) != "" {
		texts = append(texts, content.Text)
	}
	for _, rawPart := range content.Parts {
		var text string
		if json.Unmarshal(rawPart, &text) == nil {
			texts = append(texts, text)
			continue
		}
		var object struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(rawPart, &object) == nil && strings.TrimSpace(object.Text) != "" {
			texts = append(texts, object.Text)
		}
	}
	return strings.TrimSpace(strings.Join(texts, "\n")), content.ContentType
}
