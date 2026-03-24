package state

import (
	"net/netip"
	"testing"
	"time"

	hsdb "github.com/juanfont/headscale/hscontrol/db"
	"github.com/juanfont/headscale/hscontrol/policy"
	"github.com/juanfont/headscale/hscontrol/types"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"tailscale.com/tailcfg"
)

func iap(addr string) *netip.Addr {
	a := netip.MustParseAddr(addr)
	return &a
}

func newServeServiceTestState(t *testing.T) *State {
	t.Helper()
	return newServeServiceTestStateForNode(t, &types.Node{
		ID:        1,
		Hostname:  "service-node",
		GivenName: "service-node",
		Tags:      []string{"tag:service"},
	})
}

func newServeServiceTestStateForNode(t *testing.T, node *types.Node) *State {
	t.Helper()

	prefix4 := netip.MustParsePrefix("100.64.0.0/24")
	prefix6 := netip.MustParsePrefix("fd7a:115c:a1e0::/120")
	ipAlloc, err := hsdb.NewIPAllocator(nil, &prefix4, &prefix6, types.IPAllocationStrategySequential)
	require.NoError(t, err)

	if node == nil {
		node = &types.Node{
			ID:        1,
			Hostname:  "service-node",
			GivenName: "service-node",
			Tags:      []string{"tag:service"},
		}
	}

	return &State{
		cfg: &types.Config{
			Serve: types.ServeConfig{
				Service: types.ServeServiceConfig{Collect: true},
			},
		},
		ipAlloc: ipAlloc,
		nodeStore: NewNodeStore(
			types.Nodes{node},
			func(nodes []types.NodeView) map[types.NodeID][]types.NodeView {
				return map[types.NodeID][]types.NodeView{}
			},
			1,
			10*time.Millisecond,
		),
	}
}

func TestUpdateServiceCollectionFromHostinfoMarksRefresh(t *testing.T) {
	t.Parallel()

	st := newServeServiceTestState(t)

	ch := st.UpdateServiceCollectionFromHostinfo(
		1,
		nil,
		&tailcfg.Hostinfo{
			ServicesHash: "hash-1",
		},
	)

	require.True(t, ch.IsEmpty())
	require.True(t, st.ServiceMetadataNeedsRefresh(1))
	require.Nil(t, st.ServiceIPMappings(1))
}

func TestEnsureServiceCollectionStateBootstrapsRefreshFromNodeHostinfo(t *testing.T) {
	t.Parallel()

	st := newServeServiceTestStateForNode(t, &types.Node{
		ID:        1,
		Hostname:  "service-node",
		GivenName: "service-node",
		Tags:      []string{"tag:service"},
		Hostinfo: &tailcfg.Hostinfo{
			ServicesHash: "hash-bootstrap",
		},
	})

	_ = st.ensureServiceCollectionState()
	require.True(t, st.ServiceMetadataNeedsRefresh(1))

	st.serviceCollection.mu.RLock()
	defer st.serviceCollection.mu.RUnlock()
	require.Equal(t, "hash-bootstrap", st.serviceCollection.nodes[1].servicesHash)
	require.True(t, st.serviceCollection.nodes[1].needsRefresh)
}

func TestSetVIPServicesAllocatesMappings(t *testing.T) {
	t.Parallel()

	st := newServeServiceTestState(t)
	st.UpdateServiceCollectionFromHostinfo(
		1,
		nil,
		&tailcfg.Hostinfo{
			ServicesHash: "hash-1",
		},
	)

	ch, err := st.SetVIPServices(1, &tailcfg.C2NVIPServicesResponse{
		ServicesHash: "hash-1",
		VIPServices: []*tailcfg.VIPService{
			{Name: "svc:web", Active: true},
		},
	})
	require.NoError(t, err)
	require.Equal(t, types.NodeID(1), ch.OriginNode)
	require.True(t, ch.IncludeDNS)
	require.False(t, st.ServiceMetadataNeedsRefresh(1))

	mappings := st.ServiceIPMappings(1)
	require.Len(t, mappings, 1)
	require.Len(t, mappings["svc:web"], 2)
	require.Equal(t, netip.MustParseAddr("100.64.0.1"), mappings["svc:web"][0])
	require.Equal(t, netip.MustParseAddr("fd7a:115c:a1e0::1"), mappings["svc:web"][1])
}

func TestUpdateServiceCollectionFromHostinfoClearsMappings(t *testing.T) {
	t.Parallel()

	st := newServeServiceTestState(t)
	_, err := st.SetVIPServices(1, &tailcfg.C2NVIPServicesResponse{
		ServicesHash: "hash-1",
		VIPServices: []*tailcfg.VIPService{
			{Name: "svc:web", Active: true},
		},
	})
	require.NoError(t, err)

	ch := st.UpdateServiceCollectionFromHostinfo(
		1,
		&tailcfg.Hostinfo{
			ServicesHash: "hash-1",
		},
		&tailcfg.Hostinfo{
			WireIngress: false,
		},
	)

	require.Equal(t, types.NodeID(1), ch.OriginNode)
	require.True(t, ch.IncludeDNS)
	require.Nil(t, st.ServiceIPMappings(1))
	require.False(t, st.ServiceMetadataNeedsRefresh(1))
}

func TestServiceRefreshLifecycle(t *testing.T) {
	t.Parallel()

	st := newServeServiceTestState(t)
	st.UpdateServiceCollectionFromHostinfo(
		1,
		nil,
		&tailcfg.Hostinfo{
			ServicesHash: "hash-1",
		},
	)

	require.True(t, st.BeginServiceRefresh(1))
	require.False(t, st.BeginServiceRefresh(1))

	st.MarkServiceRefreshFailed(1)
	require.True(t, st.BeginServiceRefresh(1))

	_, err := st.SetVIPServices(1, &tailcfg.C2NVIPServicesResponse{
		ServicesHash: "hash-1",
		VIPServices: []*tailcfg.VIPService{
			{Name: "svc:web", Active: true},
		},
	})
	require.NoError(t, err)
	require.False(t, st.BeginServiceRefresh(1))
}

func TestUpdateServiceCollectionFromHostinfoDoesNotRequireWireIngress(t *testing.T) {
	t.Parallel()

	st := newServeServiceTestState(t)

	ch := st.UpdateServiceCollectionFromHostinfo(
		1,
		nil,
		&tailcfg.Hostinfo{
			ServicesHash: "hash-1",
			WireIngress:  false,
		},
	)

	require.True(t, ch.IsEmpty())
	require.True(t, st.ServiceMetadataNeedsRefresh(1))
}

func TestServiceDNSRecords(t *testing.T) {
	t.Parallel()

	st := newServeServiceTestState(t)

	_, err := st.SetVIPServices(1, &tailcfg.C2NVIPServicesResponse{
		ServicesHash: "hash-1",
		VIPServices: []*tailcfg.VIPService{
			{Name: "svc:web", Active: true},
		},
	})
	require.NoError(t, err)

	records := st.ServiceDNSRecords("headscale.net")
	require.Equal(t, []tailcfg.DNSRecord{
		{Name: "web.headscale.net", Type: "A", Value: "100.64.0.1"},
		{Name: "web.headscale.net", Type: "AAAA", Value: "fd7a:115c:a1e0::1"},
	}, records)
}

func TestSetVIPServicesDoesNotPublishInactiveServices(t *testing.T) {
	t.Parallel()

	st := newServeServiceTestState(t)

	ch, err := st.SetVIPServices(1, &tailcfg.C2NVIPServicesResponse{
		ServicesHash: "hash-1",
		VIPServices: []*tailcfg.VIPService{
			{Name: "svc:web", Active: false},
		},
	})
	require.NoError(t, err)
	require.Equal(t, types.NodeID(1), ch.OriginNode)
	require.True(t, ch.IncludeDNS)
	require.Nil(t, st.ServiceIPMappings(1))
	require.Empty(t, st.ServiceDNSRecords("headscale.net"))

	st.serviceCollection.mu.RLock()
	defer st.serviceCollection.mu.RUnlock()
	require.Len(t, st.serviceCollection.nodes[1].services, 1)
	require.Equal(t, tailcfg.ServiceName("svc:web"), st.serviceCollection.nodes[1].services[0].Name)
	require.False(t, st.serviceCollection.nodes[1].services[0].Active)
}

func TestSetVIPServicesDoesNotPublishForUntaggedNodes(t *testing.T) {
	t.Parallel()

	st := newServeServiceTestStateForNode(t, &types.Node{
		ID:        1,
		Hostname:  "user-node",
		GivenName: "user-node",
	})

	ch, err := st.SetVIPServices(1, &tailcfg.C2NVIPServicesResponse{
		ServicesHash: "hash-1",
		VIPServices: []*tailcfg.VIPService{
			{Name: "svc:web", Active: true},
		},
	})
	require.NoError(t, err)
	require.Equal(t, types.NodeID(1), ch.OriginNode)
	require.True(t, ch.IncludeDNS)
	require.Nil(t, st.ServiceIPMappings(1))
	require.Empty(t, st.ServiceDNSRecords("headscale.net"))

	st.serviceCollection.mu.RLock()
	defer st.serviceCollection.mu.RUnlock()
	require.Len(t, st.serviceCollection.nodes[1].services, 1)
	require.Equal(t, tailcfg.ServiceName("svc:web"), st.serviceCollection.nodes[1].services[0].Name)
}

func TestSetVIPServicesDoesNotPublishWhenServiceNotApproved(t *testing.T) {
	t.Parallel()

	user := types.User{
		Model: gorm.Model{
			ID: 1,
		},
		Name: "user1",
	}
	node := &types.Node{
		ID:        1,
		Hostname:  "service-node",
		GivenName: "service-node",
		Tags:      []string{"tag:service"},
		IPv4:      iap("100.64.0.50"),
		User:      &user,
	}
	node.UserID = &user.ID

	st := newServeServiceTestStateForNode(t, node)

	polMan, err := policy.NewPolicyManager([]byte(`{
  "tagOwners": {
    "tag:service": ["user1@"]
  },
  "autoApprovers": {
    "services": {
      "svc:api": ["tag:service"]
    }
  }
}`), []types.User{user}, types.Nodes{node}.ViewSlice())
	require.NoError(t, err)
	st.polMan = polMan

	ch, err := st.SetVIPServices(1, &tailcfg.C2NVIPServicesResponse{
		ServicesHash: "hash-1",
		VIPServices: []*tailcfg.VIPService{
			{Name: "svc:web", Active: true},
		},
	})
	require.NoError(t, err)
	require.Equal(t, types.NodeID(1), ch.OriginNode)
	require.True(t, ch.IncludeDNS)
	require.Nil(t, st.ServiceIPMappings(1))
	require.Empty(t, st.ServiceDNSRecords("headscale.net"))
}

func TestSetVIPServicesPublishesWhenServiceApproved(t *testing.T) {
	t.Parallel()

	user := types.User{
		Model: gorm.Model{
			ID: 1,
		},
		Name: "user1",
	}
	node := &types.Node{
		ID:        1,
		Hostname:  "service-node",
		GivenName: "service-node",
		Tags:      []string{"tag:service"},
		IPv4:      iap("100.64.0.51"),
		User:      &user,
	}
	node.UserID = &user.ID

	st := newServeServiceTestStateForNode(t, node)

	polMan, err := policy.NewPolicyManager([]byte(`{
  "tagOwners": {
    "tag:service": ["user1@"]
  },
  "autoApprovers": {
    "services": {
      "svc:web": ["tag:service"]
    }
  }
}`), []types.User{user}, types.Nodes{node}.ViewSlice())
	require.NoError(t, err)
	st.polMan = polMan

	ch, err := st.SetVIPServices(1, &tailcfg.C2NVIPServicesResponse{
		ServicesHash: "hash-1",
		VIPServices: []*tailcfg.VIPService{
			{Name: "svc:web", Active: true},
		},
	})
	require.NoError(t, err)
	require.Equal(t, types.NodeID(1), ch.OriginNode)
	require.True(t, ch.IncludeDNS)
	mappings := st.ServiceIPMappings(1)
	require.Len(t, mappings, 1)
	require.Len(t, mappings["svc:web"], 2)
}

func TestReevaluateServiceHostApprovalsWithdrawsDeniedService(t *testing.T) {
	t.Parallel()

	user := types.User{
		Model: gorm.Model{ID: 1},
		Name:  "user1",
	}
	node := &types.Node{
		ID:        1,
		Hostname:  "service-node",
		GivenName: "service-node",
		Tags:      []string{"tag:service"},
		IPv4:      iap("100.64.0.52"),
		User:      &user,
	}
	node.UserID = &user.ID

	st := newServeServiceTestStateForNode(t, node)

	allowPolicy, err := policy.NewPolicyManager([]byte(`{
  "tagOwners": {
    "tag:service": ["user1@"]
  },
  "autoApprovers": {
    "services": {
      "svc:web": ["tag:service"]
    }
  }
}`), []types.User{user}, types.Nodes{node}.ViewSlice())
	require.NoError(t, err)
	st.polMan = allowPolicy

	_, err = st.SetVIPServices(1, &tailcfg.C2NVIPServicesResponse{
		ServicesHash: "hash-1",
		VIPServices: []*tailcfg.VIPService{
			{Name: "svc:web", Active: true},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, st.ServiceIPMappings(1))

	denyPolicy, err := policy.NewPolicyManager([]byte(`{
  "tagOwners": {
    "tag:service": ["user1@"]
  },
  "autoApprovers": {
    "services": {
      "svc:other": ["tag:service"]
    }
  }
}`), []types.User{user}, types.Nodes{node}.ViewSlice())
	require.NoError(t, err)
	st.polMan = denyPolicy

	ch := st.ReevaluateServiceHostApprovals()
	require.True(t, ch.IncludeDNS)
	require.Nil(t, st.ServiceIPMappings(1))
	require.Empty(t, st.ServiceDNSRecords("headscale.net"))
}
