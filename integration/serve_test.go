package integration

import (
	"encoding/json"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/juanfont/headscale/integration/hsic"
	"github.com/juanfont/headscale/integration/integrationutil"
	"github.com/juanfont/headscale/integration/tsic"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"tailscale.com/ipn"
)

type serveTestEnv struct {
	scenario   *Scenario
	serveNode  TailscaleClient
	clientNode TailscaleClient
	serveFQDN  string
}

func TestServeHTTPProxyStatusAndReset(t *testing.T) {
	IntegrationSkip(t)

	env := newServeTestEnv(
		t,
		"serve-http-proxy",
		nil,
		[]tsic.Option{
			tsic.WithPackages("python3"),
		},
		[]tsic.Option{
			tsic.WithPackages("curl"),
			tsic.WithDockerWorkdir("/"),
		},
	)

	_, stderr, err := env.serveNode.Execute([]string{
		"sh",
		"-c",
		"mkdir -p /tmp/serve-http && printf 'proxy-ok\\n' >/tmp/serve-http/index.html && python3 -m http.server 18080 --bind 127.0.0.1 --directory /tmp/serve-http >/tmp/serve-http.log 2>&1 &",
	})
	require.NoError(t, err, stderr)

	_, stderr, err = env.serveNode.Execute([]string{
		"tailscale", "serve", "--bg", "--http", "80", "http://127.0.0.1:18080",
	})
	require.NoError(t, err, stderr)

	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		cfg := readServeStatusWithCollect(c, env.serveNode)

		hostPort := ipn.HostPort(net.JoinHostPort(env.serveFQDN, "80"))
		assert.Contains(c, cfg.Web, hostPort)
		handler := cfg.Web[hostPort].Handlers["/"]
		if assert.NotNil(c, handler) {
			assert.Equal(c, "http://127.0.0.1:18080", handler.Proxy)
		}
	}, 30*time.Second, 500*time.Millisecond, "serve status should report the HTTP proxy handler")

	_, stderr, err = env.serveNode.Execute([]string{"tailscale", "serve", "reset"})
	require.NoError(t, err, stderr)

	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		cfg := readServeStatusWithCollect(c, env.serveNode)
		assert.Empty(c, cfg.Web)
		assert.Empty(c, cfg.TCP)
	}, 30*time.Second, 500*time.Millisecond, "serve reset should clear the serve config")

	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		_, err := env.clientNode.CurlFailFast(fmt.Sprintf("http://%s", env.serveFQDN))
		assert.Error(c, err)
	}, 30*time.Second, 500*time.Millisecond, "peer should stop reaching the served HTTP proxy endpoint after reset")
}

func TestServeTCPPeerReachability(t *testing.T) {
	IntegrationSkip(t)

	env := newServeTestEnv(
		t,
		"serve-tcp-peer",
		nil,
		[]tsic.Option{
			tsic.WithPackages("python3"),
		},
		[]tsic.Option{
			tsic.WithPackages("python3"),
			tsic.WithDockerWorkdir("/"),
		},
	)

	_, stderr, err := env.serveNode.Execute([]string{
		"sh",
		"-c",
		"python3 -c 'import socket; s=socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1); s.bind((\"127.0.0.1\", 18081)); s.listen(1); conn, _ = s.accept(); data = conn.recv(1024); conn.sendall(b\"tcp:\" + data); conn.close(); s.close()' >/tmp/serve-tcp.log 2>&1 &",
	})
	require.NoError(t, err, stderr)

	_, stderr, err = env.serveNode.Execute([]string{
		"tailscale", "serve", "--bg", "--tcp", "10080", "tcp://127.0.0.1:18081",
	})
	require.NoError(t, err, stderr)

	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		cfg := readServeStatusWithCollect(c, env.serveNode)

		if assert.Contains(c, cfg.TCP, uint16(10080)) {
			assert.Equal(c, "127.0.0.1:18081", cfg.TCP[10080].TCPForward)
		}
	}, 30*time.Second, 500*time.Millisecond, "serve status should report the TCP forwarder")

	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		stdout, stderr, err := env.clientNode.Execute([]string{
			"python3",
			"-c",
			fmt.Sprintf("import socket; s=socket.create_connection((%q, 10080), timeout=5); s.sendall(b'ping'); print(s.recv(1024).decode()); s.close()", env.serveFQDN),
		})
		assert.NoError(c, err, stderr)
		assert.Contains(c, stdout, "tcp:ping")
	}, 30*time.Second, 500*time.Millisecond, "peer should reach the served TCP endpoint")
}

func TestFunnelToggleStatus(t *testing.T) {
	IntegrationSkip(t)

	env := newServeTestEnv(
		t,
		"serve-funnel-toggle",
		[]hsic.Option{
			hsic.WithConfigEnv(map[string]string{
				"HEADSCALE_DNS_OVERRIDE_LOCAL_DNS":             "false",
				"HEADSCALE_SERVE_HTTPS_ENABLED":                "true",
				"HEADSCALE_SERVE_HTTPS_DNS_PROVIDER":           "rfc2136",
				"HEADSCALE_SERVE_HTTPS_DNS_RFC2136_NAMESERVER": "127.0.0.1:53",
				"HEADSCALE_SERVE_HTTPS_DNS_RFC2136_ZONE":       "headscale.net",
				"HEADSCALE_SERVE_FUNNEL_ENABLED":               "true",
				"HEADSCALE_SERVE_FUNNEL_ALLOW_PORTS":           "443",
			}),
		},
		[]tsic.Option{
			tsic.WithPackages("python3"),
		},
		nil,
	)

	_, stderr, err := env.serveNode.Execute([]string{
		"sh",
		"-c",
		"mkdir -p /tmp/serve-funnel && printf 'funnel-ok\\n' >/tmp/serve-funnel/index.html && python3 -m http.server 18082 --bind 127.0.0.1 --directory /tmp/serve-funnel >/tmp/serve-funnel.log 2>&1 &",
	})
	require.NoError(t, err, stderr)

	_, stderr, err = env.serveNode.Execute([]string{
		"tailscale", "funnel", "--bg", "http://127.0.0.1:18082",
	})
	require.NoError(t, err, stderr)

	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		cfg := readServeStatusWithCollect(c, env.serveNode)
		hostPort := ipn.HostPort(net.JoinHostPort(env.serveFQDN, "443"))
		assert.True(c, cfg.AllowFunnel[hostPort])
		if assert.Contains(c, cfg.TCP, uint16(443)) {
			assert.True(c, cfg.TCP[443].HTTPS)
		}
	}, 30*time.Second, 500*time.Millisecond, "funnel should be enabled for the configured port")

	_, stderr, err = env.serveNode.Execute([]string{
		"tailscale", "serve", "--bg", "http://127.0.0.1:18082",
	})
	require.NoError(t, err, stderr)

	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		cfg := readServeStatusWithCollect(c, env.serveNode)
		hostPort := ipn.HostPort(net.JoinHostPort(env.serveFQDN, "443"))
		assert.False(c, cfg.AllowFunnel[hostPort])
	}, 30*time.Second, 500*time.Millisecond, "funnel should be disabled after turning it off")
}

func TestServeServiceHostPeerReachability(t *testing.T) {
	IntegrationSkip(t)

	spec := ScenarioSpec{
		NodesPerUser: 0,
		Users:        []string{"user1"},
		MaxWait:      dockertestMaxWait(),
	}

	scenario, err := NewScenario(spec)
	require.NoError(t, err)
	t.Cleanup(func() {
		scenario.ShutdownAssertNoPanics(t)
	})

	err = scenario.CreateHeadscaleEnv(
		[]tsic.Option{},
		hsic.WithTestName("serve-service-host"),
		hsic.WithConfigEnv(map[string]string{
			"HEADSCALE_SERVE_SERVICE_COLLECT": "true",
		}),
	)
	requireNoErrHeadscaleEnv(t, err)

	headscale, err := scenario.Headscale()
	require.NoError(t, err)

	user, err := GetUserByName(headscale, "user1")
	require.NoError(t, err)

	serviceHostAuthKey, err := scenario.CreatePreAuthKeyWithTags(user.GetId(), true, false, []string{"tag:service"})
	require.NoError(t, err)
	clientAuthKey, err := scenario.CreatePreAuthKey(user.GetId(), true, false)
	require.NoError(t, err)

	serviceHost, err := scenario.CreateTailscaleNode(
		"head",
		tsic.WithNetwork(scenario.networks[scenario.testDefaultNetwork]),
		tsic.WithNetfilter("off"),
		tsic.WithPackages("python3"),
	)
	require.NoError(t, err)

	clientNode, err := scenario.CreateTailscaleNode(
		"head",
		tsic.WithNetwork(scenario.networks[scenario.testDefaultNetwork]),
		tsic.WithNetfilter("off"),
		tsic.WithPackages("curl"),
		tsic.WithDockerWorkdir("/"),
		tsic.WithAcceptRoutes(),
	)
	require.NoError(t, err)

	err = serviceHost.Login(headscale.GetEndpoint(), serviceHostAuthKey.GetKey())
	require.NoError(t, err)
	err = clientNode.Login(headscale.GetEndpoint(), clientAuthKey.GetKey())
	require.NoError(t, err)

	err = serviceHost.WaitForRunning(integrationutil.PeerSyncTimeout())
	require.NoError(t, err)
	err = clientNode.WaitForRunning(integrationutil.PeerSyncTimeout())
	require.NoError(t, err)
	err = scenario.WaitForTailscaleSync()
	require.NoError(t, err)

	_, stderr, err := serviceHost.Execute([]string{
		"sh",
		"-c",
		"mkdir -p /tmp/serve-service && printf 'service-ok\\n' >/tmp/serve-service/index.html && python3 -m http.server 18083 --bind 127.0.0.1 --directory /tmp/serve-service >/tmp/serve-service.log 2>&1 &",
	})
	require.NoError(t, err, stderr)

	_, stderr, err = serviceHost.Execute([]string{
		"tailscale", "serve", "--service=svc:web", "--bg", "--http", "80", "http://127.0.0.1:18083",
	})
	require.NoError(t, err, stderr)

	var serviceMagicSuffix string
	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		status, err := serviceHost.Status()
		assert.NoError(c, err)
		if !assert.NotNil(c, status.CurrentTailnet) {
			return
		}
		serviceMagicSuffix = status.CurrentTailnet.MagicDNSSuffix
	}, 30*time.Second, 500*time.Millisecond, "service host should have current tailnet status")

	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		cfg := readServeStatusWithCollect(c, serviceHost)
		service := cfg.Services["svc:web"]
		if assert.NotNil(c, service) {
			hostPort := ipn.HostPort(net.JoinHostPort("web."+serviceMagicSuffix, "80"))
			assert.Contains(c, service.Web, hostPort)
		}
	}, 30*time.Second, 500*time.Millisecond, "serve status should report the service-host config")

	var serviceURL string
	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		status, err := clientNode.Status()
		assert.NoError(c, err)
		if !assert.NotNil(c, status.CurrentTailnet) {
			return
		}
		serviceURL = fmt.Sprintf("http://web.%s", status.CurrentTailnet.MagicDNSSuffix)
	}, 30*time.Second, 500*time.Millisecond, "client should have current tailnet status")

	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		stdout, err := clientNode.CurlFailFast(serviceURL)
		assert.NoError(c, err)
		assert.Contains(c, stdout, "service-ok")
	}, 60*time.Second, 500*time.Millisecond, "peer should reach the served service-host endpoint")
}

func TestServeServiceHostAdvertiseAndDrain(t *testing.T) {
	IntegrationSkip(t)

	spec := ScenarioSpec{
		NodesPerUser: 0,
		Users:        []string{"user1"},
		MaxWait:      dockertestMaxWait(),
	}

	scenario, err := NewScenario(spec)
	require.NoError(t, err)
	t.Cleanup(func() {
		scenario.ShutdownAssertNoPanics(t)
	})

	err = scenario.CreateHeadscaleEnv(
		[]tsic.Option{},
		hsic.WithTestName("serve-service-host-advertise-drain"),
		hsic.WithConfigEnv(map[string]string{
			"HEADSCALE_SERVE_SERVICE_COLLECT": "true",
		}),
	)
	requireNoErrHeadscaleEnv(t, err)

	headscale, err := scenario.Headscale()
	require.NoError(t, err)

	user, err := GetUserByName(headscale, "user1")
	require.NoError(t, err)

	serviceHostAuthKey, err := scenario.CreatePreAuthKeyWithTags(user.GetId(), true, false, []string{"tag:service"})
	require.NoError(t, err)
	clientAuthKey, err := scenario.CreatePreAuthKey(user.GetId(), true, false)
	require.NoError(t, err)

	serviceHost, err := scenario.CreateTailscaleNode(
		"head",
		tsic.WithNetwork(scenario.networks[scenario.testDefaultNetwork]),
		tsic.WithNetfilter("off"),
		tsic.WithPackages("python3"),
	)
	require.NoError(t, err)

	clientNode, err := scenario.CreateTailscaleNode(
		"head",
		tsic.WithNetwork(scenario.networks[scenario.testDefaultNetwork]),
		tsic.WithNetfilter("off"),
		tsic.WithPackages("curl"),
		tsic.WithDockerWorkdir("/"),
		tsic.WithAcceptRoutes(),
	)
	require.NoError(t, err)

	err = serviceHost.Login(headscale.GetEndpoint(), serviceHostAuthKey.GetKey())
	require.NoError(t, err)
	err = clientNode.Login(headscale.GetEndpoint(), clientAuthKey.GetKey())
	require.NoError(t, err)

	err = serviceHost.WaitForRunning(integrationutil.PeerSyncTimeout())
	require.NoError(t, err)
	err = clientNode.WaitForRunning(integrationutil.PeerSyncTimeout())
	require.NoError(t, err)
	err = scenario.WaitForTailscaleSync()
	require.NoError(t, err)

	_, stderr, err := serviceHost.Execute([]string{
		"sh",
		"-c",
		"mkdir -p /tmp/serve-service && printf 'service-ok\\n' >/tmp/serve-service/index.html && python3 -m http.server 18084 --bind 127.0.0.1 --directory /tmp/serve-service >/tmp/serve-service.log 2>&1 &",
	})
	require.NoError(t, err, stderr)

	_, stderr, err = serviceHost.Execute([]string{
		"tailscale", "serve", "--service=svc:web", "--bg", "--http", "80", "http://127.0.0.1:18084",
	})
	require.NoError(t, err, stderr)

	var serviceURL string
	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		status, err := clientNode.Status()
		assert.NoError(c, err)
		if !assert.NotNil(c, status.CurrentTailnet) {
			return
		}
		serviceURL = fmt.Sprintf("http://web.%s", status.CurrentTailnet.MagicDNSSuffix)
	}, 30*time.Second, 500*time.Millisecond, "client should have current tailnet status")

	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		stdout, err := clientNode.CurlFailFast(serviceURL)
		assert.NoError(c, err)
		assert.Contains(c, stdout, "service-ok")
	}, 60*time.Second, 500*time.Millisecond, "peer should initially reach the advertised service-host endpoint")

	_, stderr, err = serviceHost.Execute([]string{"tailscale", "serve", "drain", "svc:web"})
	require.NoError(t, err, stderr)

	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		cfg := readServeStatusWithCollect(c, serviceHost)
		assert.NotNil(c, cfg.Services["svc:web"])
	}, 30*time.Second, 500*time.Millisecond, "drain should keep the local service config")

	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		_, err := clientNode.CurlFailFast(serviceURL)
		assert.Error(c, err)
	}, 60*time.Second, 500*time.Millisecond, "peer should stop reaching the drained service-host endpoint")

	_, stderr, err = serviceHost.Execute([]string{"tailscale", "serve", "advertise", "svc:web"})
	require.NoError(t, err, stderr)

	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		stdout, err := clientNode.CurlFailFast(serviceURL)
		assert.NoError(c, err)
		assert.Contains(c, stdout, "service-ok")
	}, 60*time.Second, 500*time.Millisecond, "peer should reach the service-host endpoint again after advertise")
}

func newServeTestEnv(
	t *testing.T,
	testName string,
	headscaleOpts []hsic.Option,
	serveNodeOpts []tsic.Option,
	clientNodeOpts []tsic.Option,
) *serveTestEnv {
	t.Helper()

	spec := ScenarioSpec{
		NodesPerUser: 0,
		Users:        []string{"user1"},
		MaxWait:      dockertestMaxWait(),
	}

	scenario, err := NewScenario(spec)
	require.NoError(t, err)
	t.Cleanup(func() {
		scenario.ShutdownAssertNoPanics(t)
	})

	opts := append([]hsic.Option{hsic.WithTestName(testName)}, headscaleOpts...)
	err = scenario.CreateHeadscaleEnv([]tsic.Option{}, opts...)
	requireNoErrHeadscaleEnv(t, err)

	headscale, err := scenario.Headscale()
	require.NoError(t, err)

	user, err := GetUserByName(headscale, "user1")
	require.NoError(t, err)

	authKey, err := scenario.CreatePreAuthKey(user.GetId(), true, false)
	require.NoError(t, err)

	serveNode, err := scenario.CreateTailscaleNode(
		"head",
		append([]tsic.Option{
			tsic.WithNetwork(scenario.networks[scenario.testDefaultNetwork]),
			tsic.WithNetfilter("off"),
		}, serveNodeOpts...)...,
	)
	require.NoError(t, err)

	clientNode, err := scenario.CreateTailscaleNode(
		"head",
		append([]tsic.Option{
			tsic.WithNetwork(scenario.networks[scenario.testDefaultNetwork]),
			tsic.WithNetfilter("off"),
		}, clientNodeOpts...)...,
	)
	require.NoError(t, err)

	err = serveNode.Login(headscale.GetEndpoint(), authKey.GetKey())
	require.NoError(t, err)

	err = clientNode.Login(headscale.GetEndpoint(), authKey.GetKey())
	require.NoError(t, err)

	err = serveNode.WaitForRunning(integrationutil.PeerSyncTimeout())
	require.NoError(t, err)

	err = clientNode.WaitForRunning(integrationutil.PeerSyncTimeout())
	require.NoError(t, err)

	userState := scenario.GetOrCreateUser("user1")
	userState.Clients[serveNode.Hostname()] = serveNode
	userState.Clients[clientNode.Hostname()] = clientNode

	err = scenario.WaitForTailscaleSync()
	require.NoError(t, err)

	serveFQDN, err := serveNode.FQDN()
	require.NoError(t, err)

	return &serveTestEnv{
		scenario:   scenario,
		serveNode:  serveNode,
		clientNode: clientNode,
		serveFQDN:  trimDotSuffix(serveFQDN),
	}
}

func readServeStatusWithCollect(c *assert.CollectT, node TailscaleClient) ipn.ServeConfig {
	c.Helper()

	stdout, stderr, err := node.Execute([]string{"tailscale", "serve", "status", "--json"})
	assert.NoError(c, err, stderr)

	var cfg ipn.ServeConfig
	err = json.Unmarshal([]byte(stdout), &cfg)
	assert.NoError(c, err)

	return cfg
}

func trimDotSuffix(name string) string {
	if len(name) > 0 && name[len(name)-1] == '.' {
		return name[:len(name)-1]
	}

	return name
}
