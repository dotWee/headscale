package state

import (
	"net/netip"
	"testing"

	hsdb "github.com/juanfont/headscale/hscontrol/db"
	"github.com/juanfont/headscale/hscontrol/types"
	"github.com/stretchr/testify/require"
	"tailscale.com/tailcfg"
)

func newServeServiceTestState(t *testing.T) *State {
	t.Helper()

	prefix4 := netip.MustParsePrefix("100.64.0.0/24")
	prefix6 := netip.MustParsePrefix("fd7a:115c:a1e0::/120")
	ipAlloc, err := hsdb.NewIPAllocator(nil, &prefix4, &prefix6, types.IPAllocationStrategySequential)
	require.NoError(t, err)

	return &State{
		cfg: &types.Config{
			Serve: types.ServeConfig{
				Service: types.ServeServiceConfig{Collect: true},
			},
		},
		ipAlloc: ipAlloc,
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
