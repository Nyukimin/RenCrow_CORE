package orchestrator

import (
	"fmt"
	"log"

	capdomain "github.com/Nyukimin/RenCrow_CORE/internal/domain/capability"
	domainnode "github.com/Nyukimin/RenCrow_CORE/internal/domain/node"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/transport"
)

type distributedCoderSelection struct {
	router        *transport.MessageRouter
	sshTransports map[string]domaintransport.Transport
	nodeSelector  *NodeSelector
	nodeCaps      map[string]domainnode.ResourceProfile
	coderCaps     []capdomain.CoderCapability
	coderStatus   *CoderStatus
}

func newDistributedCoderSelection(
	router *transport.MessageRouter,
	sshTransports map[string]domaintransport.Transport,
	nodeSelector *NodeSelector,
	nodeCaps map[string]domainnode.ResourceProfile,
	coderStatuses ...*CoderStatus,
) *distributedCoderSelection {
	var coderStatus *CoderStatus
	if len(coderStatuses) > 0 {
		coderStatus = coderStatuses[0]
	}
	if coderStatus == nil {
		coderStatus = NewCoderStatus()
	}
	return &distributedCoderSelection{
		router:        router,
		sshTransports: sshTransports,
		nodeSelector:  nodeSelector,
		nodeCaps:      nodeCaps,
		coderStatus:   coderStatus,
	}
}

func (s *distributedCoderSelection) SetNodeCapabilities(caps map[string]domainnode.ResourceProfile) {
	s.nodeCaps = caps
}

func (s *distributedCoderSelection) SetCoderCapabilities(caps []capdomain.CoderCapability) {
	if caps == nil {
		s.coderCaps = nil
		return
	}
	s.coderCaps = make([]capdomain.CoderCapability, len(caps))
	copy(s.coderCaps, caps)
}

func (s *distributedCoderSelection) IsCoderConnected(agent string) bool {
	if _, ok := s.sshTransports[agent]; ok {
		return true
	}
	if s.router == nil {
		return false
	}
	_, ok := s.router.GetAgent(agent)
	return ok
}

// AcquireForRoute atomically acquires the requested or round-robin eligible coder.
// The returned release function owns the lease for the full distributed task.
func (s *distributedCoderSelection) AcquireForRoute(route routing.Route, _ string) (string, func(), error) {
	if coder, _, explicit := explicitCodeRouteTarget(route); explicit {
		if available, known := s.coderAvailability(coder); s.coderCaps != nil && (!known || !available) {
			log.Printf("[DistributedOrch] coder skip route=%s target=%s reason=unavailable", route, coder)
			return "", nil, fmt.Errorf("%s is unavailable", coder)
		}
		if !s.IsCoderConnected(coder) {
			log.Printf("[DistributedOrch] coder skip route=%s target=%s reason=unconnected", route, coder)
			return "", nil, fmt.Errorf("%s is unavailable", coder)
		}
		if !s.coderStatus.Acquire(coder) {
			log.Printf("[DistributedOrch] coder skip route=%s target=%s reason=busy", route, coder)
			return "", nil, fmt.Errorf("%s is busy", coder)
		}
		log.Printf("[DistributedOrch] coder selected route=%s target=%s mode=explicit", route, coder)
		return coder, func() { s.coderStatus.Release(coder) }, nil
	}
	if route != routing.RouteCODE {
		return "", nil, fmt.Errorf("no coder mapped for route %s", route)
	}

	candidates := s.readyConnectedCoders()
	if len(candidates) == 0 {
		log.Printf("[DistributedOrch] coder skip route=%s reason=no_ready_connected_candidates", route)
		return "", nil, fmt.Errorf("no enabled, probe-ready connected coder is available")
	}
	coder, ok := s.coderStatus.AcquireNext(candidates)
	if !ok {
		log.Printf("[DistributedOrch] coder skip route=%s reason=all_ready_candidates_busy candidates=%d", route, len(candidates))
		return "", nil, fmt.Errorf("all ready coders are busy")
	}
	log.Printf("[DistributedOrch] coder selected route=%s target=%s mode=round_robin candidates=%d", route, coder, len(candidates))
	return coder, func() { s.coderStatus.Release(coder) }, nil
}

func (s *distributedCoderSelection) readyConnectedCoders() []string {
	available := make(map[string]bool, len(s.coderCaps))
	for _, cap := range s.coderCaps {
		name := normalizeCoderStatusName(cap.Name)
		if name != "" && cap.Available {
			available[name] = true
		}
	}
	candidates := make([]string, 0, len(coderStatusSlotOrder))
	for _, name := range coderStatusSlotOrder {
		if !available[name] || !s.IsCoderConnected(name) {
			continue
		}
		candidates = append(candidates, name)
	}
	return candidates
}

func (s *distributedCoderSelection) coderAvailability(name string) (available, known bool) {
	for _, cap := range s.coderCaps {
		if normalizeCoderStatusName(cap.Name) == name {
			return cap.Available, true
		}
	}
	return false, false
}
