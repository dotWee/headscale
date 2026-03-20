package hscontrol

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"github.com/juanfont/headscale/hscontrol/types"
	"github.com/miekg/dns"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"tailscale.com/control/controlclient"
	"tailscale.com/health"
	"tailscale.com/net/netmon"
	"tailscale.com/net/tsdial"
	"tailscale.com/tailcfg"
	"tailscale.com/types/dnstype"
	"tailscale.com/types/key"
	"tailscale.com/types/persist"
	"tailscale.com/util/eventbus"
)

type mockServeDNSManager struct {
	name  string
	value string
	err   error
}

func (m *mockServeDNSManager) SetDNS(_ context.Context, name, value string) error {
	m.name = name
	m.value = value
	return m.err
}

func newServeTestApp(t *testing.T) (*Headscale, *httptest.Server) {
	t.Helper()

	tmpDir := t.TempDir()
	prefixV4 := netip.MustParsePrefix("100.64.0.0/10")
	prefixV6 := netip.MustParsePrefix("fd7a:115c:a1e0::/48")

	cfg := &types.Config{
		ServerURL:                      "http://localhost:0",
		NoisePrivateKeyPath:            tmpDir + "/noise_private.key",
		EphemeralNodeInactivityTimeout: 30 * time.Second,
		PrefixV4:                       &prefixV4,
		PrefixV6:                       &prefixV6,
		IPAllocation:                   types.IPAllocationStrategySequential,
		BaseDomain:                     "example.com",
		Serve: types.ServeConfig{
			HTTPS: types.ServeHTTPSConfig{
				Enabled: false,
			},
		},
		Database: types.DatabaseConfig{
			Type: "sqlite3",
			Sqlite: types.SqliteConfig{
				Path: tmpDir + "/headscale_test.db",
			},
		},
		Policy: types.PolicyConfig{Mode: types.PolicyModeDB},
		Tuning: types.Tuning{
			BatchChangeDelay:               50 * time.Millisecond,
			NodeMapSessionBufferedChanSize: 30,
			BatcherWorkers:                 1,
		},
		TailcfgDNSConfig: &tailcfg.DNSConfig{
			Routes:  map[string][]*dnstype.Resolver{},
			Domains: []string{"example.com"},
			Proxied: true,
		},
	}

	app, err := NewHeadscale(cfg)
	require.NoError(t, err)
	app.StartBatcherForTest(t)
	app.StartEphemeralGCForTest(t)
	app.GetState().SetDERPMap(&tailcfg.DERPMap{
		Regions: map[int]*tailcfg.DERPRegion{
			900: {
				RegionID:   900,
				RegionCode: "test",
				RegionName: "Test Region",
				Nodes: []*tailcfg.DERPNode{{
					Name:     "test0",
					RegionID: 900,
					HostName: "127.0.0.1",
					IPv4:     "127.0.0.1",
					DERPPort: -1,
				}},
			},
		},
	})

	ts := httptest.NewServer(app.HTTPHandler())
	t.Cleanup(ts.Close)
	app.SetServerURLForTest(t, ts.URL)

	return app, ts
}

func registerServeTestNode(t *testing.T, app *Headscale, serverURL, hostname string) types.NodeView {
	t.Helper()

	user, _, err := app.state.CreateUser(types.User{Name: "serve-user"})
	require.NoError(t, err)

	uid := types.UserID(user.ID)
	pak, err := app.state.CreatePreAuthKey(&uid, true, false, nil, nil)
	require.NoError(t, err)

	bus := eventbus.New()
	tracker := health.NewTracker(bus)
	dialer := tsdial.NewDialer(netmon.NewStatic())
	dialer.SetBus(bus)
	t.Cleanup(func() {
		dialer.Close()
		bus.Close()
	})

	machineKey := key.NewMachine()
	direct, err := controlclient.NewDirect(controlclient.Options{
		Persist:              persist.Persist{},
		GetMachinePrivateKey: func() (key.MachinePrivate, error) { return machineKey, nil },
		ServerURL:            serverURL,
		AuthKey:              pak.Key,
		Hostinfo: &tailcfg.Hostinfo{
			BackendLogID: "serve-test-" + hostname,
			Hostname:     hostname,
		},
		DiscoPublicKey: key.NewDisco().Public(),
		Logf:           t.Logf,
		HealthTracker:  tracker,
		Dialer:         dialer,
		Bus:            bus,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = direct.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	loginURL, err := direct.TryLogin(ctx, controlclient.LoginDefault)
	require.NoError(t, err)
	require.Empty(t, loginURL)

	nodeKey, ok := direct.GetPersist().PublicNodeKeyOK()
	require.True(t, ok)

	require.Eventually(t, func() bool {
		_, ok := app.state.GetNodeByNodeKey(nodeKey)
		return ok
	}, 5*time.Second, 100*time.Millisecond)

	node, ok := app.state.GetNodeByNodeKey(nodeKey)
	require.True(t, ok)

	return node
}

func TestServeFeatureResponse(t *testing.T) {
	t.Parallel()

	cfg := &types.Config{}

	resp, err := serveFeatureResponse(cfg, serveFeatureName)
	require.NoError(t, err)
	assert.False(t, resp.Complete)

	cfg.Serve.HTTPS.Enabled = true
	resp, err = serveFeatureResponse(cfg, serveFeatureName)
	require.NoError(t, err)
	assert.True(t, resp.Complete)

	resp, err = serveFeatureResponse(cfg, funnelFeatureName)
	require.NoError(t, err)
	assert.False(t, resp.Complete)
	assert.Contains(t, resp.Text, "disabled")

	cfg.Serve.Funnel.Enabled = true
	cfg.Serve.Funnel.AllowPorts = []string{"443", "8443"}
	resp, err = serveFeatureResponse(cfg, funnelFeatureName)
	require.NoError(t, err)
	assert.True(t, resp.Complete)
}

func TestValidateServeDNSRequest(t *testing.T) {
	t.Parallel()

	user := &types.User{Name: "test"}
	node := &types.Node{
		ID:        1,
		Hostname:  "serve-node",
		GivenName: "serve-node",
		UserID:    &user.ID,
		User:      user,
		NodeKey:   key.NewNode().Public(),
	}
	cfg := &types.Config{
		BaseDomain: "example.com",
		Serve: types.ServeConfig{
			HTTPS: types.ServeHTTPSConfig{Enabled: true},
		},
	}

	req := tailcfg.SetDNSRequest{
		NodeKey: node.NodeKey,
		Name:    "_acme-challenge.serve-node.example.com",
		Type:    "TXT",
		Value:   "challenge",
	}

	require.NoError(t, validateServeDNSRequest(cfg, node.View(), node.NodeKey, req))

	req.Name = "_acme-challenge.other.example.com"
	require.ErrorIs(t, validateServeDNSRequest(cfg, node.View(), node.NodeKey, req), errInvalidServeDNSName)
}

func TestValidateServeDNSRequestDedicatedServeDomain(t *testing.T) {
	t.Parallel()

	user := &types.User{Name: "test"}
	node := &types.Node{
		ID:        1,
		Hostname:  "serve-node",
		GivenName: "serve-node",
		UserID:    &user.ID,
		User:      user,
		NodeKey:   key.NewNode().Public(),
	}
	cfg := &types.Config{
		BaseDomain: "tailnet.example.com",
		Serve: types.ServeConfig{
			Domain: "serve.example.com",
			HTTPS:  types.ServeHTTPSConfig{Enabled: true},
		},
	}

	req := tailcfg.SetDNSRequest{
		NodeKey: node.NodeKey,
		Name:    "_acme-challenge.serve-node.serve.example.com",
		Type:    "TXT",
		Value:   "challenge",
	}

	require.NoError(t, validateServeDNSRequest(cfg, node.View(), node.NodeKey, req))
}

func TestQueryFeatureHandler(t *testing.T) {
	t.Parallel()

	app, ts := newServeTestApp(t)
	app.cfg.Serve.HTTPS.Enabled = true
	node := registerServeTestNode(t, app, ts.URL, "serve-node")
	ns := &noiseServer{
		headscale: app,
		nodeKey:   node.NodeKey(),
	}

	body, err := json.Marshal(&tailcfg.QueryFeatureRequest{
		Feature: serveFeatureName,
		NodeKey: node.NodeKey(),
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/machine/feature/query", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	ns.QueryFeatureHandler(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var resp tailcfg.QueryFeatureResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.True(t, resp.Complete)
}

func TestSetDNSHandler(t *testing.T) {
	t.Parallel()

	app, ts := newServeTestApp(t)
	app.cfg.Serve.HTTPS.Enabled = true
	node := registerServeTestNode(t, app, ts.URL, "serve-node")
	mockDNS := &mockServeDNSManager{}
	app.serveDNS = mockDNS

	ns := &noiseServer{
		headscale: app,
		nodeKey:   node.NodeKey(),
	}

	body, err := json.Marshal(&tailcfg.SetDNSRequest{
		NodeKey: node.NodeKey(),
		Name:    "_acme-challenge.serve-node.example.com",
		Type:    "TXT",
		Value:   "challenge",
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/machine/set-dns", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	ns.SetDNSHandler(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "_acme-challenge.serve-node.example.com", mockDNS.name)
	assert.Equal(t, "challenge", mockDNS.value)
}

func TestRFC2136DNSManager(t *testing.T) {
	t.Parallel()

	updates := make(chan *dns.Msg, 1)
	handler := dns.HandlerFunc(func(w dns.ResponseWriter, r *dns.Msg) {
		updates <- r.Copy()
		resp := new(dns.Msg)
		resp.SetReply(r)
		require.NoError(t, w.WriteMsg(resp))
	})

	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	defer pc.Close()

	srv := &dns.Server{
		PacketConn: pc,
		Handler:    handler,
		MsgAcceptFunc: func(dns.Header) dns.MsgAcceptAction {
			return dns.MsgAccept
		},
	}
	go func() {
		_ = srv.ActivateAndServe()
	}()
	t.Cleanup(func() { _ = srv.Shutdown() })

	manager := &rfc2136DNSManager{
		nameserver: pc.LocalAddr().String(),
		zone:       "example.com.",
		ttl:        120,
		network:    "udp",
		timeout: timeouts{
			request: time.Second,
		},
	}

	require.NoError(t, manager.SetDNS(context.Background(), "_acme-challenge.node.example.com", "token"))

	select {
	case msg := <-updates:
		require.Len(t, msg.Ns, 2)
		remove, ok := msg.Ns[0].(*dns.TXT)
		require.True(t, ok)
		assert.Equal(t, "_acme-challenge.node.example.com.", remove.Hdr.Name)
		insert, ok := msg.Ns[1].(*dns.TXT)
		require.True(t, ok)
		assert.Equal(t, []string{"token"}, insert.Txt)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for RFC2136 update")
	}
}

func TestRFC2136DNSManagerRcodeError(t *testing.T) {
	t.Parallel()

	handler := dns.HandlerFunc(func(w dns.ResponseWriter, r *dns.Msg) {
		resp := new(dns.Msg)
		resp.SetReply(r)
		resp.Rcode = dns.RcodeRefused
		require.NoError(t, w.WriteMsg(resp))
	})

	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	defer pc.Close()

	srv := &dns.Server{
		PacketConn: pc,
		Handler:    handler,
		MsgAcceptFunc: func(dns.Header) dns.MsgAcceptAction {
			return dns.MsgAccept
		},
	}
	go func() {
		_ = srv.ActivateAndServe()
	}()
	t.Cleanup(func() { _ = srv.Shutdown() })

	manager := &rfc2136DNSManager{
		nameserver: pc.LocalAddr().String(),
		zone:       "example.com.",
		ttl:        120,
		network:    "udp",
		timeout: timeouts{
			request: time.Second,
		},
	}

	err = manager.SetDNS(context.Background(), "_acme-challenge.node.example.com", "token")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "_acme-challenge.node.example.com.")
	assert.Contains(t, err.Error(), "REFUSED")
}
