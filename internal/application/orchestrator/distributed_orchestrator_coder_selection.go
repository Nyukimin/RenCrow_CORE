package orchestrator

import (
	"log"

	capdomain "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/capability"
	domainnode "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/node"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/routing"
	domaintransport "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/transport"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/transport"
)

type distributedCoderSelection struct {
	router        *transport.MessageRouter
	sshTransports map[string]domaintransport.Transport
	nodeSelector  *NodeSelector
	nodeCaps      map[string]domainnode.ResourceProfile
	coderCaps     []capdomain.CoderCapability
}

func newDistributedCoderSelection(
	router *transport.MessageRouter,
	sshTransports map[string]domaintransport.Transport,
	nodeSelector *NodeSelector,
	nodeCaps map[string]domainnode.ResourceProfile,
) *distributedCoderSelection {
	return &distributedCoderSelection{
		router:        router,
		sshTransports: sshTransports,
		nodeSelector:  nodeSelector,
		nodeCaps:      nodeCaps,
	}
}

func (s *distributedCoderSelection) SetNodeCapabilities(caps map[string]domainnode.ResourceProfile) {
	s.nodeCaps = caps
}

func (s *distributedCoderSelection) SetCoderCapabilities(caps []capdomain.CoderCapability) {
	s.coderCaps = append([]capdomain.CoderCapability(nil), caps...)
}

func (s *distributedCoderSelection) RouteToCoder(route routing.Route) string {
	if selected := s.routeByCapability(route); selected != "" {
		return selected
	}
	switch route {
	case routing.RouteCODE:
		if s.IsCoderConnected("coder1") {
			log.Printf("[DistributedOrch] coder selected route=%s target=coder1 mode=default", route)
			return "coder1"
		}
		log.Printf("[DistributedOrch] coder skip route=%s target=coder1 reason=unconnected", route)
		return ""
	case routing.RouteCODE1:
		return s.explicitCoder(route, "coder1")
	case routing.RouteCODE2:
		return s.explicitCoder(route, "coder2")
	case routing.RouteCODE3:
		return s.explicitCoder(route, "coder3")
	case routing.RouteCODE4:
		return s.explicitCoder(route, "coder4")
	default:
		return ""
	}
}

func (s *distributedCoderSelection) RouteToCoderForMessage(route routing.Route, userMessage string) string {
	return s.RouteToCoder(route)
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

func (s *distributedCoderSelection) explicitCoder(route routing.Route, coder string) string {
	if s.IsCoderConnected(coder) {
		log.Printf("[DistributedOrch] coder selected route=%s target=%s mode=explicit", route, coder)
		return coder
	}
	log.Printf("[DistributedOrch] coder skip route=%s target=%s reason=unconnected", route, coder)
	return ""
}

func (s *distributedCoderSelection) routeByCapability(route routing.Route) string {
	if len(s.coderCaps) == 0 {
		return ""
	}
	candidates := make([]capdomain.CoderCapability, 0, len(s.coderCaps))
	for _, cap := range s.coderCaps {
		connected := s.IsCoderConnected(cap.Name)
		if !connected {
			log.Printf("[DistributedOrch] coder skip route=%s target=%s reason=unconnected capability_quality=%d", route, cap.Name, cap.Quality)
		}
		cap.Available = cap.Available && connected
		candidates = append(candidates, cap)
	}
	selected, degraded, evidence, err := capdomain.SelectCoderWithEvidence(candidates, route)
	if err != nil {
		log.Printf("[DistributedOrch] coder capability selection failed route=%s err=%v", route, err)
		return ""
	}
	mode := "capability"
	if degraded != "" {
		mode = "capability_degraded"
	}
	log.Printf("[DistributedOrch] coder selected route=%s target=%s mode=%s selected_quality=%d degraded_route=%s required_quality=%d candidates=%d",
		route, selected, mode, evidence.SelectedQuality, degraded, evidence.RequiredQuality, len(evidence.Candidates))
	return selected
}
