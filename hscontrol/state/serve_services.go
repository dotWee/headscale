package state

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"sync"

	"github.com/juanfont/headscale/hscontrol/types"
	"github.com/juanfont/headscale/hscontrol/types/change"
	"tailscale.com/tailcfg"
)

var errServiceVIPAllocatorUnavailable = errors.New("service VIP allocator unavailable")

type nodeVIPServiceState struct {
	servicesHash string
	services     []*tailcfg.VIPService
	needsRefresh bool
	refreshing   bool
}

type serviceCollectionState struct {
	mu            sync.RWMutex
	nodes         map[types.NodeID]nodeVIPServiceState
	serviceIPs    map[tailcfg.ServiceName][]netip.Addr
	serviceOwners map[tailcfg.ServiceName]map[types.NodeID]struct{}
}

func (s *State) ensureServiceCollectionState() *serviceCollectionState {
	if s.serviceCollection != nil {
		return s.serviceCollection
	}

	s.serviceCollection = &serviceCollectionState{
		nodes:         make(map[types.NodeID]nodeVIPServiceState),
		serviceIPs:    make(map[tailcfg.ServiceName][]netip.Addr),
		serviceOwners: make(map[tailcfg.ServiceName]map[types.NodeID]struct{}),
	}

	return s.serviceCollection
}

func (s *State) shouldCollectServices() bool {
	return s.cfg != nil && s.cfg.Serve.Service.Collect
}

func (s *State) ServiceIPMappings(id types.NodeID) tailcfg.ServiceIPMappings {
	if !s.shouldCollectServices() || s.serviceCollection == nil {
		return nil
	}

	s.serviceCollection.mu.RLock()
	defer s.serviceCollection.mu.RUnlock()

	nodeState, ok := s.serviceCollection.nodes[id]
	if !ok || nodeState.needsRefresh || len(nodeState.services) == 0 {
		return nil
	}

	mappings := make(tailcfg.ServiceIPMappings)
	for _, svc := range nodeState.services {
		if svc == nil {
			continue
		}

		ips := s.serviceCollection.serviceIPs[svc.Name]
		if len(ips) == 0 {
			continue
		}

		mappings[svc.Name] = slices.Clone(ips)
	}

	if len(mappings) == 0 {
		return nil
	}

	return mappings
}

func (s *State) ServiceMetadataNeedsRefresh(id types.NodeID) bool {
	if !s.shouldCollectServices() || s.serviceCollection == nil {
		return false
	}

	s.serviceCollection.mu.RLock()
	defer s.serviceCollection.mu.RUnlock()

	nodeState, ok := s.serviceCollection.nodes[id]
	return ok && nodeState.needsRefresh
}

func (s *State) BeginServiceRefresh(id types.NodeID) bool {
	if !s.shouldCollectServices() || s.serviceCollection == nil {
		return false
	}

	s.serviceCollection.mu.Lock()
	defer s.serviceCollection.mu.Unlock()

	nodeState, ok := s.serviceCollection.nodes[id]
	if !ok || !nodeState.needsRefresh || nodeState.refreshing {
		return false
	}

	nodeState.refreshing = true
	s.serviceCollection.nodes[id] = nodeState

	return true
}

func (s *State) MarkServiceRefreshFailed(id types.NodeID) {
	if !s.shouldCollectServices() || s.serviceCollection == nil {
		return
	}

	s.serviceCollection.mu.Lock()
	defer s.serviceCollection.mu.Unlock()

	nodeState, ok := s.serviceCollection.nodes[id]
	if !ok {
		return
	}

	nodeState.refreshing = false
	s.serviceCollection.nodes[id] = nodeState
}

func (s *State) UpdateServiceCollectionFromHostinfo(
	id types.NodeID,
	oldHI, newHI *tailcfg.Hostinfo,
) change.Change {
	if !s.shouldCollectServices() {
		return change.Change{}
	}

	st := s.ensureServiceCollectionState()
	st.mu.Lock()
	defer st.mu.Unlock()

	oldHash, oldIngress := serviceSignals(oldHI)
	newHash, newIngress := serviceSignals(newHI)

	if !newIngress || newHash == "" {
		changed, releasedIPs := st.clearNodeLocked(id)
		if len(releasedIPs) > 0 && s.ipAlloc != nil {
			s.ipAlloc.FreeIPs(releasedIPs)
		}
		if changed {
			return change.NodeAdded(id)
		}

		return change.Change{}
	}

	nodeState := st.nodes[id]
	hasCached := nodeState.servicesHash == newHash && !nodeState.needsRefresh && len(nodeState.services) > 0
	if oldHash != newHash || oldIngress != newIngress || !hasCached {
		changed, releasedIPs := st.clearNodeLocked(id)
		if len(releasedIPs) > 0 && s.ipAlloc != nil {
			s.ipAlloc.FreeIPs(releasedIPs)
		}
		st.nodes[id] = nodeVIPServiceState{
			servicesHash: newHash,
			needsRefresh: true,
			refreshing:   false,
		}
		if changed {
			return change.NodeAdded(id)
		}
	}

	return change.Change{}
}

func (s *State) SetVIPServices(
	id types.NodeID,
	resp *tailcfg.C2NVIPServicesResponse,
) (change.Change, error) {
	if !s.shouldCollectServices() {
		return change.Change{}, nil
	}
	if resp == nil {
		return change.Change{}, nil
	}

	st := s.ensureServiceCollectionState()
	st.mu.Lock()
	defer st.mu.Unlock()

	oldState, hadOldState := st.nodes[id]
	changed, releasedIPs := st.clearNodeLocked(id)
	if len(releasedIPs) > 0 && s.ipAlloc != nil {
		s.ipAlloc.FreeIPs(releasedIPs)
	}

	clonedServices := make([]*tailcfg.VIPService, 0, len(resp.VIPServices))
	for _, svc := range resp.VIPServices {
		if svc == nil {
			continue
		}
		if err := svc.Name.Validate(); err != nil {
			return change.Change{}, fmt.Errorf("invalid VIP service name %q: %w", svc.Name, err)
		}

		clonedServices = append(clonedServices, svc.Clone())

		if _, ok := st.serviceIPs[svc.Name]; !ok {
			ips, err := s.allocateVIPServiceIPsLocked()
			if err != nil {
				return change.Change{}, err
			}
			st.serviceIPs[svc.Name] = ips
		}

		if st.serviceOwners[svc.Name] == nil {
			st.serviceOwners[svc.Name] = make(map[types.NodeID]struct{})
		}
		st.serviceOwners[svc.Name][id] = struct{}{}
	}

	st.nodes[id] = nodeVIPServiceState{
		servicesHash: resp.ServicesHash,
		services:     clonedServices,
		needsRefresh: false,
		refreshing:   false,
	}

	if !changed {
		changed = !hadOldState ||
			oldState.servicesHash != resp.ServicesHash ||
			oldState.needsRefresh ||
			!equalVIPServices(oldState.services, clonedServices)
	}

	if changed || len(clonedServices) > 0 {
		return change.NodeAdded(id), nil
	}

	return change.Change{}, nil
}

func (s *State) allocateVIPServiceIPsLocked() ([]netip.Addr, error) {
	if s.ipAlloc == nil {
		return nil, errServiceVIPAllocatorUnavailable
	}

	v4, v6, err := s.ipAlloc.Next()
	if err != nil {
		return nil, fmt.Errorf("allocating service VIPs: %w", err)
	}

	ips := make([]netip.Addr, 0, 2)
	if v4 != nil {
		ips = append(ips, *v4)
	}
	if v6 != nil {
		ips = append(ips, *v6)
	}

	return ips, nil
}

func (st *serviceCollectionState) clearNodeLocked(id types.NodeID) (bool, []netip.Addr) {
	nodeState, ok := st.nodes[id]
	if !ok {
		return false, nil
	}

	changed := len(nodeState.services) > 0
	var released []netip.Addr
	for _, svc := range nodeState.services {
		if svc == nil {
			continue
		}

		owners := st.serviceOwners[svc.Name]
		delete(owners, id)
		if len(owners) == 0 {
			delete(st.serviceOwners, svc.Name)
			released = append(released, st.serviceIPs[svc.Name]...)
			delete(st.serviceIPs, svc.Name)
		}
	}

	delete(st.nodes, id)

	return changed, released
}

func serviceSignals(hi *tailcfg.Hostinfo) (servicesHash string, wireIngress bool) {
	if hi == nil {
		return "", false
	}

	return hi.ServicesHash, hi.WireIngress
}

func equalVIPServices(a, b []*tailcfg.VIPService) bool {
	if len(a) != len(b) {
		return false
	}

	for i := range a {
		switch {
		case a[i] == nil && b[i] == nil:
			continue
		case a[i] == nil || b[i] == nil:
			return false
		case a[i].Name != b[i].Name:
			return false
		case a[i].Active != b[i].Active:
			return false
		case !slices.Equal(a[i].Ports, b[i].Ports):
			return false
		}
	}

	return true
}
