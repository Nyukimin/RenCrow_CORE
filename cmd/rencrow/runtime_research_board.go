package main

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/boardresearch"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	appresearch "github.com/Nyukimin/RenCrow_CORE/internal/application/research"
	c "github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/contract"
	"github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/engine"
)

// Resolve an existing connection generation for each operation. Research never
// creates a client, discovers a principal, or restarts the BOARD lifecycle.
type runtimeResearchBoardOwner struct {
	destinations map[string]appresearch.BoardDestination
	resolve      func(appresearch.BoardDestination) (engine.OwnerPort, error)
}

var _ engine.OwnerPort = (*runtimeResearchBoardOwner)(nil)
var _ engine.ReconcilePort = (*runtimeResearchBoardOwner)(nil)

func connectResearchBoardOwner(cfg *config.Config, deps *Dependencies, e *engine.Engine) error {
	if cfg == nil || e == nil || len(cfg.Research.BoardDestinations) == 0 {
		return nil
	}
	registered := make(map[string]appresearch.BoardDestination, len(cfg.Research.BoardDestinations))
	for _, entry := range cfg.Research.BoardDestinations {
		destination := appresearch.BoardDestination{
			Ref: entry.Ref, OwnerID: entry.OwnerID, PrincipalID: entry.PrincipalID,
			ChannelID: entry.ChannelID, ThreadID: entry.ThreadID, NewThread: entry.NewThread,
			Recipients: append([]string(nil), entry.Recipients...), DataScope: entry.DataScope, Title: entry.Title,
		}
		sort.Strings(destination.Recipients)
		if destination.Validate() != nil || destination.DataScope != "user" {
			return fmt.Errorf("invalid authenticated Research BOARD destination")
		}
		if _, found := registered[destination.Ref]; found {
			return fmt.Errorf("duplicate Research BOARD destination ref")
		}
		registered[destination.Ref] = destination
	}
	owner := &runtimeResearchBoardOwner{destinations: registered}
	owner.resolve = func(destination appresearch.BoardDestination) (engine.OwnerPort, error) {
		if deps == nil {
			return nil, appresearch.ErrBoardOwnerUnavailable
		}
		journal, ok := deps.canonicalEventStore.(appresearch.BoardPublicationJournal)
		if !ok {
			return nil, appresearch.ErrBoardOwnerUnavailable
		}
		boot := researchExistingBoardBoot(deps, destination.PrincipalID)
		if boot == nil || boot.settings.OwnerID != destination.OwnerID || !researchBoardMember(boot.settings.AllowedChannelIDs, destination.ChannelID) {
			return nil, appresearch.ErrBoardOwnerUnavailable
		}
		for _, recipient := range destination.Recipients {
			if !researchBoardMember(boot.inboxScope.AllowedPeerIDs, recipient) {
				return nil, appresearch.ErrBoardOwnerUnavailable
			}
		}
		boot.mu.RLock()
		client, pin := boot.client, boot.pin
		boot.mu.RUnlock()
		return boardresearch.NewOwner(client, journal, pin, []appresearch.BoardDestination{destination}, func() time.Time { return time.Now().UTC() })
	}
	e.Ports.Owner = appresearch.NewOwnerRouter(e.Ports.Owner, owner)
	return nil
}

func researchExistingBoardBoot(deps *Dependencies, principal string) *boardCommunicationBoot {
	if deps == nil {
		return nil
	}
	if boot := deps.boardCommunicationRootBoot; boot != nil && boot.settings.ExpectedPrincipalID == principal {
		return boot
	}
	for _, boot := range deps.boardCommunicationActorBoots {
		if boot != nil && boot.settings.ExpectedPrincipalID == principal {
			return boot
		}
	}
	return nil
}

func researchBoardMember(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func (o *runtimeResearchBoardOwner) owner(env c.Envelope, ref string) (engine.OwnerPort, error) {
	if o == nil || o.resolve == nil || env.DataScope != "user" || env.OwnerID == "" {
		return nil, appresearch.ErrBoardOwnerUnavailable
	}
	destination, found := o.destinations[ref]
	if !found || destination.OwnerID != env.OwnerID || destination.DataScope != env.DataScope {
		return nil, appresearch.ErrBoardOwnerUnavailable
	}
	return o.resolve(destination)
}

func (o *runtimeResearchBoardOwner) Commit(ctx context.Context, request engine.CommitRequest) (c.Receipt, error) {
	owner, err := o.owner(request.Envelope, request.Target.DestinationRef)
	if err != nil {
		return c.Receipt{}, err
	}
	return owner.Commit(ctx, request)
}

func (o *runtimeResearchBoardOwner) Verify(ctx context.Context, env c.Envelope, receipt c.Receipt) (c.Receipt, error) {
	owner, err := o.owner(env, receipt.DestinationRef)
	if err != nil {
		return c.Receipt{}, err
	}
	return owner.Verify(ctx, env, receipt)
}

func (o *runtimeResearchBoardOwner) Reconcile(ctx context.Context, request engine.CommitRequest) (engine.ReconcileResult, error) {
	owner, err := o.owner(request.Envelope, request.Target.DestinationRef)
	if err != nil {
		return engine.ReconcileResult{}, err
	}
	reconciler, ok := owner.(engine.ReconcilePort)
	if !ok {
		return engine.ReconcileResult{}, appresearch.ErrBoardOwnerUnavailable
	}
	return reconciler.Reconcile(ctx, request)
}
