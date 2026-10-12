package agentprofile

import (
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
)

// 応答の先頭ラベルとして除去する Agent 名（conversation.AgentLabelNames）は、
// CORE の Agent identity の正本である Catalog と一致しなければならない。
// 新しい Agent を Catalog に足したら、ラベル除去の対象も更新が必要になる。
func TestCatalogAgentIDsMatchSpeakerLabelNames(t *testing.T) {
	catalogIDs := map[string]bool{}
	for _, profile := range NewStaticCatalog().List() {
		catalogIDs[profile.ID] = true
	}
	labelNames := map[string]bool{}
	for _, name := range conversation.AgentLabelNames() {
		labelNames[name] = true
	}
	for id := range catalogIDs {
		if !labelNames[id] {
			t.Errorf("Agent %q is in the Catalog but not in conversation.AgentLabelNames()", id)
		}
	}
	for name := range labelNames {
		if !catalogIDs[name] {
			t.Errorf("label name %q is not an Agent in the Catalog", name)
		}
	}
}
