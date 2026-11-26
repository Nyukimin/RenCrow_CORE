package main

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/agentroutine"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
)

// ReadBoardConversationHistory projects confirmed routine facts, whose copies
// are stored for each participant. It never reads user chats or worker prompts.
func (r *agentRoutineRuntime) ReadBoardConversationHistory(ctx context.Context, date string) ([]viewer.BoardConversation, error) {
	if r == nil || r.store == nil {
		return nil, errors.New("routine history unavailable")
	}
	items := make([]viewer.BoardConversation, 0)
	byID := make(map[string]viewer.BoardConversation)
	for _, actor := range agentHeartbeatActorIDs {
		facts, err := r.store.ListFacts(ctx, actor, date)
		if err != nil {
			return nil, err
		}
		for _, fact := range facts {
			if !fact.Confirmed || (fact.Kind != agentroutine.ActivityMorningChat && fact.Kind != agentroutine.ActivityConversation) || fact.Source != agentRoutineModuleID {
				continue
			}
			if fact.SourceID == "" || fact.At.IsZero() || fact.At.In(time.FixedZone("JST", 9*60*60)).Format("2006-01-02") != date {
				return nil, errors.New("invalid conversation identity or timestamp")
			}
			var turns []viewer.BoardConversationTurn
			if json.Unmarshal([]byte(fact.Reference), &turns) != nil || len(turns) < 2 {
				return nil, errors.New("invalid conversation transcript")
			}
			participants := make(map[string]bool)
			for _, turn := range turns {
				if !superagent.IsPrivateChatQueueActorID(turn.ActorID) || strings.TrimSpace(turn.Text) == "" {
					return nil, errors.New("invalid conversation speaker or text")
				}
				participants[turn.ActorID] = true
			}
			if len(participants) < 2 || !participants[actor] {
				return nil, errors.New("invalid conversation participants")
			}
			item := viewer.BoardConversation{ID: fact.SourceID, At: fact.At, Turns: turns}
			if prior, ok := byID[item.ID]; ok {
				if !prior.At.Equal(item.At) || !reflect.DeepEqual(prior.Turns, item.Turns) {
					return nil, errors.New("conflicting conversation copies")
				}
				continue
			}
			byID[item.ID] = item
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].At.Equal(items[j].At) {
			return items[i].ID < items[j].ID
		}
		return items[i].At.After(items[j].At)
	})
	return items, nil
}
