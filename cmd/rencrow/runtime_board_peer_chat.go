package main

import (
	"fmt"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
)

type boardCommunicationResponderWiring struct {
	Readback responseFirstReadbackWiring
	PeerChat boardPeerChatWiring
}

type boardPeerChatWiring map[string]agent.ReadbackAssigneeConfig

// boardPeerChatWiringFor reads exact authored bundles and dedicated CHAT-only
// Gateway providers for explicitly configured Kuro/BOARD-character responders.
// It deliberately ignores Heavy, Coder, and ordinary Readback providers.
func boardPeerChatWiringFor(cfg *config.Config, providers map[string]agent.ConversationGenerationProvider) boardPeerChatWiring {
	if cfg == nil || !cfg.BoardCommunication.Enabled || cfg.Prompts == nil || len(providers) == 0 {
		return nil
	}
	wiring := make(boardPeerChatWiring)
	for _, responder := range cfg.BoardCommunication.Responders {
		actorID := responder.ActorID
		if actorID != string(agent.BoardPeerChatActorKuro) && !domainconversation.IsBoardSpeechCharacter(actorID) {
			continue
		}
		provider := providers[actorID]
		prompt := strings.TrimSpace(cfg.Prompts.CharacterPrompts[actorID])
		if provider == nil || prompt == "" {
			continue
		}
		wiring[actorID] = agent.ReadbackAssigneeConfig{Provider: provider, SystemPrompt: prompt}
	}
	return wiring
}

// newBoardPeerChatRuntime selects the reviewed authored prompt and real
// conversation provider for one external-thread responder. Stable runtime
// context from the readback wiring is deliberately not forwarded.
func newBoardPeerChatRuntime(actor string, wiring boardCommunicationResponderWiring) (*agent.BoardPeerChatRuntime, error) {
	if !domainconversation.IsExternalPeerResponderActor(actor) {
		return nil, fmt.Errorf("unsupported BOARD peer chat actor %q", actor)
	}
	var configured agent.ReadbackAssigneeConfig
	var ok bool
	if actor == string(agent.BoardPeerChatActorKuro) || domainconversation.IsBoardSpeechCharacter(actor) {
		configured, ok = wiring.PeerChat[actor]
	} else {
		configured, ok = wiring.Readback.Assignees[actor]
	}
	if !ok {
		return nil, fmt.Errorf("BOARD peer chat provider and authored prompt for %s are not configured", actor)
	}
	if configured.Provider == nil {
		return nil, fmt.Errorf("BOARD peer chat provider for %s is unavailable", actor)
	}
	if strings.TrimSpace(configured.SystemPrompt) == "" {
		return nil, fmt.Errorf("authored system prompt for BOARD peer chat actor %s is unavailable", actor)
	}
	return agent.NewBoardPeerChatRuntime(agent.BoardPeerChatActor(actor), configured.SystemPrompt, configured.Provider)
}
