package integration

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"github.com/juanfont/headscale/integration/hsic"
	"github.com/juanfont/headscale/integration/integrationutil"
	"github.com/juanfont/headscale/integration/tsic"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	policyv2 "github.com/juanfont/headscale/hscontrol/policy/v2"
	"tailscale.com/ipn"
)

type serveTestEnv struct {
	scenario   *Scenario
	serveNode  TailscaleClient
	clientNode TailscaleClient
	serveFQDN  string
}

// waitForLocalTCPPort waits until host:port accepts a connection inside node.
// Call this after starting a background listener so tailscale serve does not
// race a backend that has not bound yet.
func waitForLocalTCPPort(t *testing.T, node TailscaleClient, host string, port int, msg string) {
	t.Helper()

	py := fmt.Sprintf(
		"import socket\n"+
			"s=socket.socket()\n"+
			"s.settimeout(3)\n"+
			"try:\n"+
			"    s.connect((%q, %d))\n"+
			"finally:\n"+
			"    s.close()",
		host, port,
	)

	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		_, stderr, err := node.Execute([]string{"python3", "-c", py})
		assert.NoError(c, err, stderr)
	}, 30*time.Second, 200*time.Millisecond, msg)
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

func TestServeHTTPSE2EPebble(t *testing.T) {
	IntegrationSkip(t)

	if os.Getenv("HEADSCALE_INTEGRATION_PEBBLE_E2E") == "" {
		t.Skip("set HEADSCALE_INTEGRATION_PEBBLE_E2E=1 with RFC2136/ACME test infra to run Pebble-backed HTTPS serve e2e")
	}
	nameserver := os.Getenv("HEADSCALE_INTEGRATION_PEBBLE_DNS_NAMESERVER")
	zone := os.Getenv("HEADSCALE_INTEGRATION_PEBBLE_DNS_ZONE")
	if nameserver == "" || zone == "" {
		t.Skip("set HEADSCALE_INTEGRATION_PEBBLE_DNS_NAMESERVER and HEADSCALE_INTEGRATION_PEBBLE_DNS_ZONE for Pebble-backed HTTPS serve e2e")
	}

	env := newServeTestEnv(
		t,
		"serve-https-pebble-e2e",
		[]hsic.Option{
			hsic.WithConfigEnv(map[string]string{
				"HEADSCALE_DNS_OVERRIDE_LOCAL_DNS":             "false",
				"HEADSCALE_SERVE_HTTPS_ENABLED":                "true",
				"HEADSCALE_SERVE_HTTPS_DNS_PROVIDER":           "rfc2136",
				"HEADSCALE_SERVE_HTTPS_DNS_RFC2136_NAMESERVER": nameserver,
				"HEADSCALE_SERVE_HTTPS_DNS_RFC2136_ZONE":       zone,
			}),
		},
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
		"mkdir -p /tmp/serve-https && printf 'https-ok\\n' >/tmp/serve-https/index.html && python3 -m http.server 18110 --bind 127.0.0.1 --directory /tmp/serve-https >/tmp/serve-https.log 2>&1 &",
	})
	require.NoError(t, err, stderr)

	_, stderr, err = env.serveNode.Execute([]string{
		"tailscale", "serve", "--bg", "https", "/=http://127.0.0.1:18110",
	})
	require.NoError(t, err, stderr)

	httpsURL := fmt.Sprintf("https://%s", env.serveFQDN)
	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		stdout, err := env.clientNode.CurlFailFast(httpsURL)
		assert.NoError(c, err)
		assert.Contains(c, stdout, "https-ok")
	}, 120*time.Second, 2*time.Second, "peer should reach HTTPS serve endpoint once certificate issuance completes")
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

func TestServeNodeScopedTLSTerminatedTCP(t *testing.T) {
	IntegrationSkip(t)

	env := newServeTestEnv(
		t,
		"serve-node-tls-terminated-tcp",
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
		"mkdir -p /tmp/serve-node-tls && printf 'node-tls-ok\\n' >/tmp/serve-node-tls/index.html && python3 -m http.server 18130 --bind 127.0.0.1 --directory /tmp/serve-node-tls >/tmp/serve-node-tls.log 2>&1 &",
	})
	require.NoError(t, err, stderr)

	waitForLocalTCPPort(t, env.serveNode, "127.0.0.1", 18130, "TLS-terminated TCP backend HTTP server should listen")

	_, stderr, err = env.serveNode.Execute([]string{
		"tailscale", "serve", "--bg", "--tls-terminated-tcp", "9444", "tcp://127.0.0.1:18130",
	})
	require.NoError(t, err, stderr)

	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		cfg := readServeStatusWithCollect(c, env.serveNode)
		if tcpSvc, ok := cfg.TCP[9444]; assert.True(c, ok) {
			assert.Equal(c, "127.0.0.1:18130", tcpSvc.TCPForward)
			assert.NotEmpty(c, tcpSvc.TerminateTLS)
		}
	}, 60*time.Second, 500*time.Millisecond, "node-scoped serve status should expose TLS-terminated TCP before curl checks")

	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		stdout, stderr, err := env.clientNode.Execute([]string{
			"curl",
			"--silent",
			"--show-error",
			"--insecure",
			"--max-time",
			"5",
			fmt.Sprintf("https://%s:9444", env.serveFQDN),
		})
		assert.NoError(c, err, stderr)
		assert.Contains(c, stdout, "node-tls-ok")
	}, 120*time.Second, 500*time.Millisecond, "peer should reach node-scoped TLS-terminated TCP endpoint")
}

func TestServeNodeScopedMultiPort(t *testing.T) {
	IntegrationSkip(t)

	env := newServeTestEnv(
		t,
		"serve-node-multi-port",
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
		"mkdir -p /tmp/serve-node-multi && printf 'node-multi-http\\n' >/tmp/serve-node-multi/index.html && python3 -m http.server 18131 --bind 127.0.0.1 --directory /tmp/serve-node-multi >/tmp/serve-node-multi-http.log 2>&1 & python3 -c 'import socket; s=socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1); s.bind((\"127.0.0.1\", 18132)); s.listen(1); conn, _ = s.accept(); data = conn.recv(1024); conn.sendall(b\"node-multi:\" + data); conn.close(); s.close()' >/tmp/serve-node-multi-tcp.log 2>&1 &",
	})
	require.NoError(t, err, stderr)

	_, stderr, err = env.serveNode.Execute([]string{
		"tailscale", "serve", "--bg", "--http", "80", "http://127.0.0.1:18131",
	})
	require.NoError(t, err, stderr)
	_, stderr, err = env.serveNode.Execute([]string{
		"tailscale", "serve", "--bg", "--tcp", "10082", "tcp://127.0.0.1:18132",
	})
	require.NoError(t, err, stderr)

	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		cfg := readServeStatusWithCollect(c, env.serveNode)
		hostPort := ipn.HostPort(net.JoinHostPort(env.serveFQDN, "80"))
		assert.Contains(c, cfg.Web, hostPort)
		assert.Contains(c, cfg.TCP, uint16(10082))
	}, 30*time.Second, 500*time.Millisecond, "node-scoped serve status should contain HTTP and TCP ports")

	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		stdout, err := env.clientNode.CurlFailFast(fmt.Sprintf("http://%s", env.serveFQDN))
		assert.NoError(c, err)
		assert.Contains(c, stdout, "node-multi-http")
	}, 60*time.Second, 500*time.Millisecond, "peer should reach node-scoped HTTP endpoint")

	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		stdout, stderr, err := env.clientNode.Execute([]string{
			"python3",
			"-c",
			fmt.Sprintf("import socket; s=socket.create_connection((%q, 10082), timeout=5); s.sendall(b'ping'); print(s.recv(1024).decode()); s.close()", env.serveFQDN),
		})
		assert.NoError(c, err, stderr)
		assert.Contains(c, stdout, "node-multi:ping")
	}, 60*time.Second, 500*time.Millisecond, "peer should reach node-scoped TCP endpoint")
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

func TestFunnelPolicyEnableDenyByTag(t *testing.T) {
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

	acl := &policyv2.Policy{
		TagOwners: policyv2.TagOwners{
			"tag:funnel": policyv2.Owners{new(policyv2.Username("user1@"))},
			"tag:other":  policyv2.Owners{new(policyv2.Username("user1@"))},
		},
		NodeAttrs: []policyv2.NodeAttr{
			{
				Target: policyv2.Aliases{new(policyv2.Tag("tag:funnel"))},
				Attr:   []string{"funnel"},
			},
		},
	}

	err = scenario.CreateHeadscaleEnv(
		[]tsic.Option{},
		hsic.WithTestName("serve-funnel-policy-enable-deny"),
		hsic.WithACLPolicy(acl),
		hsic.WithConfigEnv(map[string]string{
			"HEADSCALE_DNS_OVERRIDE_LOCAL_DNS":             "false",
			"HEADSCALE_SERVE_HTTPS_ENABLED":                "true",
			"HEADSCALE_SERVE_HTTPS_DNS_PROVIDER":           "rfc2136",
			"HEADSCALE_SERVE_HTTPS_DNS_RFC2136_NAMESERVER": "127.0.0.1:53",
			"HEADSCALE_SERVE_HTTPS_DNS_RFC2136_ZONE":       "headscale.net",
			"HEADSCALE_SERVE_FUNNEL_ENABLED":               "true",
			"HEADSCALE_SERVE_FUNNEL_ALLOW_PORTS":           "443",
		}),
	)
	requireNoErrHeadscaleEnv(t, err)

	headscale, err := scenario.Headscale()
	require.NoError(t, err)
	user, err := GetUserByName(headscale, "user1")
	require.NoError(t, err)

	allowedKey, err := scenario.CreatePreAuthKeyWithTags(user.GetId(), true, false, []string{"tag:funnel"})
	require.NoError(t, err)
	deniedKey, err := scenario.CreatePreAuthKeyWithTags(user.GetId(), true, false, []string{"tag:other"})
	require.NoError(t, err)

	allowedNode, err := scenario.CreateTailscaleNode(
		"head",
		tsic.WithNetwork(scenario.networks[scenario.testDefaultNetwork]),
		tsic.WithNetfilter("off"),
		tsic.WithPackages("python3"),
	)
	require.NoError(t, err)
	deniedNode, err := scenario.CreateTailscaleNode(
		"head",
		tsic.WithNetwork(scenario.networks[scenario.testDefaultNetwork]),
		tsic.WithNetfilter("off"),
		tsic.WithPackages("python3"),
	)
	require.NoError(t, err)

	err = allowedNode.Login(headscale.GetEndpoint(), allowedKey.GetKey())
	require.NoError(t, err)
	err = deniedNode.Login(headscale.GetEndpoint(), deniedKey.GetKey())
	require.NoError(t, err)
	err = allowedNode.WaitForRunning(integrationutil.PeerSyncTimeout())
	require.NoError(t, err)
	err = deniedNode.WaitForRunning(integrationutil.PeerSyncTimeout())
	require.NoError(t, err)

	userState := scenario.GetOrCreateUser("user1")
	userState.Clients[allowedNode.Hostname()] = allowedNode
	userState.Clients[deniedNode.Hostname()] = deniedNode
	err = scenario.WaitForTailscaleSync()
	require.NoError(t, err)

	_, stderr, err := allowedNode.Execute([]string{
		"sh",
		"-c",
		"mkdir -p /tmp/funnel-allow && printf 'funnel-allow\\n' >/tmp/funnel-allow/index.html && python3 -m http.server 18100 --bind 127.0.0.1 --directory /tmp/funnel-allow >/tmp/funnel-allow.log 2>&1 &",
	})
	require.NoError(t, err, stderr)
	_, stderr, err = deniedNode.Execute([]string{
		"sh",
		"-c",
		"mkdir -p /tmp/funnel-deny && printf 'funnel-deny\\n' >/tmp/funnel-deny/index.html && python3 -m http.server 18101 --bind 127.0.0.1 --directory /tmp/funnel-deny >/tmp/funnel-deny.log 2>&1 &",
	})
	require.NoError(t, err, stderr)

	_, stderr, err = allowedNode.Execute([]string{
		"tailscale", "funnel", "--bg", "http://127.0.0.1:18100",
	})
	require.NoError(t, err, stderr)

	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		cfg := readServeStatusWithCollect(c, allowedNode)
		allowedFQDN := trimDotSuffix(allowedNode.MustFQDN())
		hostPort := ipn.HostPort(net.JoinHostPort(allowedFQDN, "443"))
		assert.True(c, cfg.AllowFunnel[hostPort])
	}, 30*time.Second, 500*time.Millisecond, "funnel should be enabled for allowed node")

	_, stderr, err = deniedNode.Execute([]string{
		"tailscale", "funnel", "--bg", "http://127.0.0.1:18101",
	})
	// Clients can differ in behavior (hard error vs local config only), so enforce by status.
	if err != nil {
		assert.NotEmpty(t, stderr)
	}

	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		cfg := readServeStatusWithCollect(c, deniedNode)
		deniedFQDN := trimDotSuffix(deniedNode.MustFQDN())
		hostPort := ipn.HostPort(net.JoinHostPort(deniedFQDN, "443"))
		assert.False(c, cfg.AllowFunnel[hostPort])
	}, 30*time.Second, 500*time.Millisecond, "funnel should remain disabled for denied node")
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

func TestServeServiceHostGetAndSetConfig(t *testing.T) {
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
		hsic.WithTestName("serve-service-host-config"),
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
		"mkdir -p /tmp/serve-service-web /tmp/serve-service-api && printf 'service-web\\n' >/tmp/serve-service-web/index.html && printf 'service-api\\n' >/tmp/serve-service-api/index.html && python3 -m http.server 18085 --bind 127.0.0.1 --directory /tmp/serve-service-web >/tmp/serve-service-web.log 2>&1 & python3 -m http.server 18086 --bind 127.0.0.1 --directory /tmp/serve-service-api >/tmp/serve-service-api.log 2>&1 &",
	})
	require.NoError(t, err, stderr)

	_, stderr, err = serviceHost.Execute([]string{
		"tailscale", "serve", "--service=svc:web", "--bg", "--http", "80", "http://127.0.0.1:18085",
	})
	require.NoError(t, err, stderr)
	_, stderr, err = serviceHost.Execute([]string{
		"tailscale", "serve", "--service=svc:api", "--bg", "--http", "80", "http://127.0.0.1:18086",
	})
	require.NoError(t, err, stderr)

	var webURL, apiURL string
	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		status, err := clientNode.Status()
		assert.NoError(c, err)
		if !assert.NotNil(c, status.CurrentTailnet) {
			return
		}
		webURL = fmt.Sprintf("http://web.%s", status.CurrentTailnet.MagicDNSSuffix)
		apiURL = fmt.Sprintf("http://api.%s", status.CurrentTailnet.MagicDNSSuffix)
	}, 30*time.Second, 500*time.Millisecond, "client should have current tailnet status")

	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		stdout, err := clientNode.CurlFailFast(webURL)
		assert.NoError(c, err)
		assert.Contains(c, stdout, "service-web")
	}, 60*time.Second, 500*time.Millisecond, "peer should initially reach the web service")
	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		stdout, err := clientNode.CurlFailFast(apiURL)
		assert.NoError(c, err)
		assert.Contains(c, stdout, "service-api")
	}, 60*time.Second, 500*time.Millisecond, "peer should initially reach the api service")

	_, stderr, err = serviceHost.Execute([]string{
		"sh", "-c", "tailscale serve get-config --service=svc:web > /tmp/web-service.json",
	})
	require.NoError(t, err, stderr)

	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		stdout, stderr, err := serviceHost.Execute([]string{"cat", "/tmp/web-service.json"})
		assert.NoError(c, err, stderr)
		assert.Contains(c, stdout, "\"tcp:80\"")
		assert.Contains(c, stdout, "18085")
		assert.NotContains(c, stdout, "18086")
	}, 30*time.Second, 500*time.Millisecond, "service config export should contain only the requested service")

	_, stderr, err = serviceHost.Execute([]string{"tailscale", "serve", "clear", "svc:web"})
	require.NoError(t, err, stderr)

	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		cfg := readServeStatusWithCollect(c, serviceHost)
		assert.Nil(c, cfg.Services["svc:web"])
		assert.NotNil(c, cfg.Services["svc:api"])
	}, 30*time.Second, 500*time.Millisecond, "clear should remove only the selected service")
	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		_, err := clientNode.CurlFailFast(webURL)
		assert.Error(c, err)
	}, 60*time.Second, 500*time.Millisecond, "peer should stop reaching the cleared web service")
	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		stdout, err := clientNode.CurlFailFast(apiURL)
		assert.NoError(c, err)
		assert.Contains(c, stdout, "service-api")
	}, 60*time.Second, 500*time.Millisecond, "peer should still reach the other service")

	_, stderr, err = serviceHost.Execute([]string{
		"tailscale", "serve", "set-config", "--service=svc:web", "/tmp/web-service.json",
	})
	require.NoError(t, err, stderr)

	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		cfg := readServeStatusWithCollect(c, serviceHost)
		assert.NotNil(c, cfg.Services["svc:web"])
		assert.NotNil(c, cfg.Services["svc:api"])
	}, 30*time.Second, 500*time.Millisecond, "service set-config should restore the selected service")
	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		stdout, err := clientNode.CurlFailFast(webURL)
		assert.NoError(c, err)
		assert.Contains(c, stdout, "service-web")
	}, 60*time.Second, 500*time.Millisecond, "peer should reach the restored web service")

	_, stderr, err = serviceHost.Execute([]string{
		"sh", "-c", "tailscale serve get-config --all > /tmp/all-services.json",
	})
	require.NoError(t, err, stderr)

	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		stdout, stderr, err := serviceHost.Execute([]string{"cat", "/tmp/all-services.json"})
		assert.NoError(c, err, stderr)
		assert.Contains(c, stdout, "svc:web")
		assert.Contains(c, stdout, "svc:api")
	}, 30*time.Second, 500*time.Millisecond, "all-services config export should contain both services")

	_, stderr, err = serviceHost.Execute([]string{"tailscale", "serve", "clear", "svc:web"})
	require.NoError(t, err, stderr)
	_, stderr, err = serviceHost.Execute([]string{"tailscale", "serve", "clear", "svc:api"})
	require.NoError(t, err, stderr)

	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		cfg := readServeStatusWithCollect(c, serviceHost)
		assert.Empty(c, cfg.Services)
	}, 30*time.Second, 500*time.Millisecond, "clear should remove all service configs")
	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		_, err := clientNode.CurlFailFast(webURL)
		assert.Error(c, err)
	}, 60*time.Second, 500*time.Millisecond, "peer should stop reaching the cleared web service after all clear")
	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		_, err := clientNode.CurlFailFast(apiURL)
		assert.Error(c, err)
	}, 60*time.Second, 500*time.Millisecond, "peer should stop reaching the cleared api service after all clear")

	_, stderr, err = serviceHost.Execute([]string{
		"tailscale", "serve", "set-config", "--all", "/tmp/all-services.json",
	})
	require.NoError(t, err, stderr)

	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		cfg := readServeStatusWithCollect(c, serviceHost)
		assert.NotNil(c, cfg.Services["svc:web"])
		assert.NotNil(c, cfg.Services["svc:api"])
	}, 30*time.Second, 500*time.Millisecond, "all-services set-config should restore both services")
	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		stdout, err := clientNode.CurlFailFast(webURL)
		assert.NoError(c, err)
		assert.Contains(c, stdout, "service-web")
	}, 60*time.Second, 500*time.Millisecond, "peer should reach the restored web service after all set-config")
	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		stdout, err := clientNode.CurlFailFast(apiURL)
		assert.NoError(c, err)
		assert.Contains(c, stdout, "service-api")
	}, 60*time.Second, 500*time.Millisecond, "peer should reach the restored api service after all set-config")
}

func TestServeServiceHostRejectsUntaggedNode(t *testing.T) {
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
		hsic.WithTestName("serve-service-host-untagged"),
		hsic.WithConfigEnv(map[string]string{
			"HEADSCALE_SERVE_SERVICE_COLLECT": "true",
		}),
	)
	requireNoErrHeadscaleEnv(t, err)

	headscale, err := scenario.Headscale()
	require.NoError(t, err)

	user, err := GetUserByName(headscale, "user1")
	require.NoError(t, err)

	authKey, err := scenario.CreatePreAuthKey(user.GetId(), true, false)
	require.NoError(t, err)

	serviceHost, err := scenario.CreateTailscaleNode(
		"head",
		tsic.WithNetwork(scenario.networks[scenario.testDefaultNetwork]),
		tsic.WithNetfilter("off"),
		tsic.WithPackages("python3"),
	)
	require.NoError(t, err)

	err = serviceHost.Login(headscale.GetEndpoint(), authKey.GetKey())
	require.NoError(t, err)

	err = serviceHost.WaitForRunning(integrationutil.PeerSyncTimeout())
	require.NoError(t, err)
	err = scenario.WaitForTailscaleSync()
	require.NoError(t, err)

	_, stderr, err := serviceHost.Execute([]string{
		"sh",
		"-c",
		"mkdir -p /tmp/serve-service && printf 'service-ok\\n' >/tmp/serve-service/index.html && python3 -m http.server 18087 --bind 127.0.0.1 --directory /tmp/serve-service >/tmp/serve-service.log 2>&1 &",
	})
	require.NoError(t, err, stderr)

	_, stderr, err = serviceHost.Execute([]string{
		"tailscale", "serve", "--service=svc:web", "--bg", "--http", "80", "http://127.0.0.1:18087",
	})
	require.Error(t, err)
	require.Contains(t, stderr, "service hosts must be tagged nodes")
}

func TestServeServiceHostTCPForwarding(t *testing.T) {
	IntegrationSkip(t)

	scenario, serviceHost, clientNode := newServiceHostPair(
		t,
		"serve-service-host-tcp-forwarding",
		nil,
		[]tsic.Option{tsic.WithPackages("python3")},
		[]tsic.Option{tsic.WithPackages("python3"), tsic.WithDockerWorkdir("/")},
	)
	_ = scenario

	_, stderr, err := serviceHost.Execute([]string{
		"sh",
		"-c",
		"python3 -c 'import socket; s=socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1); s.bind((\"127.0.0.1\", 18088)); s.listen(1); conn, _ = s.accept(); data = conn.recv(1024); conn.sendall(b\"tcp:\" + data); conn.close(); s.close()' >/tmp/serve-service-tcp.log 2>&1 &",
	})
	require.NoError(t, err, stderr)

	_, stderr, err = serviceHost.Execute([]string{
		"tailscale", "serve", "--service=svc:tcp", "--bg", "--tcp", "10080", "tcp://127.0.0.1:18088",
	})
	require.NoError(t, err, stderr)

	var tcpHost string
	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		status, err := clientNode.Status()
		assert.NoError(c, err)
		if !assert.NotNil(c, status.CurrentTailnet) {
			return
		}
		tcpHost = fmt.Sprintf("tcp.%s", status.CurrentTailnet.MagicDNSSuffix)
	}, 30*time.Second, 500*time.Millisecond, "client should have current tailnet status")

	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		stdout, stderr, err := clientNode.Execute([]string{
			"python3",
			"-c",
			fmt.Sprintf("import socket; s=socket.create_connection((%q, 10080), timeout=5); s.sendall(b'ping'); print(s.recv(1024).decode()); s.close()", tcpHost),
		})
		assert.NoError(c, err, stderr)
		assert.Contains(c, stdout, "tcp:ping")
	}, 60*time.Second, 500*time.Millisecond, "peer should reach the served service-host TCP endpoint")
}

func TestServeServiceHostTLSTerminatedTCP(t *testing.T) {
	IntegrationSkip(t)

	scenario, serviceHost, clientNode := newServiceHostPair(
		t,
		"serve-service-host-tls-terminated-tcp",
		nil,
		[]tsic.Option{tsic.WithPackages("python3")},
		[]tsic.Option{tsic.WithPackages("curl"), tsic.WithDockerWorkdir("/")},
	)
	_ = scenario

	_, stderr, err := serviceHost.Execute([]string{
		"sh",
		"-c",
		"mkdir -p /tmp/serve-service-tls && printf 'tls-tcp-ok\\n' >/tmp/serve-service-tls/index.html && python3 -m http.server 18089 --bind 127.0.0.1 --directory /tmp/serve-service-tls >/tmp/serve-service-tls.log 2>&1 &",
	})
	require.NoError(t, err, stderr)

	waitForLocalTCPPort(t, serviceHost, "127.0.0.1", 18089, "TLS-terminated TCP backend HTTP server should listen")

	_, stderr, err = serviceHost.Execute([]string{
		"tailscale", "serve", "--service=svc:tls", "--bg", "--tls-terminated-tcp", "9443", "tcp://127.0.0.1:18089",
	})
	require.NoError(t, err, stderr)

	var tlsURL string
	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		status, err := clientNode.Status()
		assert.NoError(c, err)
		if !assert.NotNil(c, status.CurrentTailnet) {
			return
		}
		tlsURL = fmt.Sprintf("https://tls.%s:9443", status.CurrentTailnet.MagicDNSSuffix)
	}, 30*time.Second, 500*time.Millisecond, "client should have current tailnet status")

	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		cfg := readServeStatusWithCollect(c, serviceHost)
		svc := cfg.Services["svc:tls"]
		if !assert.NotNil(c, svc) {
			return
		}
		if tcpSvc, ok := svc.TCP[9443]; assert.True(c, ok) {
			assert.Equal(c, "127.0.0.1:18089", tcpSvc.TCPForward)
			assert.NotEmpty(c, tcpSvc.TerminateTLS)
		}
	}, 60*time.Second, 500*time.Millisecond, "service status should expose TLS-terminated TCP before curl checks")

	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		stdout, stderr, err := clientNode.Execute([]string{
			"curl",
			"--silent",
			"--show-error",
			"--insecure",
			"--max-time",
			"5",
			tlsURL,
		})
		assert.NoError(c, err, stderr)
		assert.Contains(c, stdout, "tls-tcp-ok")
	}, 120*time.Second, 500*time.Millisecond, "peer should reach the served TLS-terminated TCP endpoint")
}

func TestServeServiceHostMultiPortAndReconnect(t *testing.T) {
	IntegrationSkip(t)

	const serviceMultiBackends = "mkdir -p /tmp/serve-service-multi && printf 'multi-http\\n' >/tmp/serve-service-multi/index.html && python3 -m http.server 18090 --bind 127.0.0.1 --directory /tmp/serve-service-multi >/tmp/serve-service-multi-http.log 2>&1 & python3 -c 'import socket; s=socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1); s.bind((\"127.0.0.1\", 18091)); s.listen(1); conn, _ = s.accept(); data = conn.recv(1024); conn.sendall(b\"multi:\" + data); conn.close(); s.close()' >/tmp/serve-service-multi-tcp.log 2>&1 &"

	scenario, serviceHost, clientNode := newServiceHostPair(
		t,
		"serve-service-host-multi-port-reconnect",
		nil,
		[]tsic.Option{tsic.WithPackages("python3")},
		[]tsic.Option{tsic.WithPackages("python3"), tsic.WithDockerWorkdir("/")},
	)

	startServiceMultiBackends := func() {
		t.Helper()

		_, stderr, err := serviceHost.Execute([]string{"sh", "-c", serviceMultiBackends})
		require.NoError(t, err, stderr)
		waitForLocalTCPPort(t, serviceHost, "127.0.0.1", 18090, "multi-port HTTP backend should listen")
		waitForLocalTCPPort(t, serviceHost, "127.0.0.1", 18091, "multi-port TCP backend should listen")
	}

	advertiseServiceMulti := func() {
		t.Helper()

		var stderr string
		var err error

		_, stderr, err = serviceHost.Execute([]string{
			"tailscale", "serve", "--service=svc:multi", "--bg", "--http", "80", "http://127.0.0.1:18090",
		})
		require.NoError(t, err, stderr)
		_, stderr, err = serviceHost.Execute([]string{
			"tailscale", "serve", "--service=svc:multi", "--bg", "--tcp", "10081", "tcp://127.0.0.1:18091",
		})
		require.NoError(t, err, stderr)
	}

	startServiceMultiBackends()
	advertiseServiceMulti()

	var multiHost string
	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		status, err := clientNode.Status()
		assert.NoError(c, err)
		if !assert.NotNil(c, status.CurrentTailnet) {
			return
		}
		multiHost = fmt.Sprintf("multi.%s", status.CurrentTailnet.MagicDNSSuffix)
	}, 30*time.Second, 500*time.Millisecond, "client should have current tailnet status")

	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		cfg := readServeStatusWithCollect(c, serviceHost)
		svc := cfg.Services["svc:multi"]
		if !assert.NotNil(c, svc) {
			return
		}
		httpHostPort := ipn.HostPort(net.JoinHostPort(multiHost, "80"))
		assert.Contains(c, svc.Web, httpHostPort)
		assert.Contains(c, svc.TCP, uint16(10081))
	}, 30*time.Second, 500*time.Millisecond, "service should expose both HTTP and TCP ports")

	httpURL := fmt.Sprintf("http://%s", multiHost)
	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		stdout, err := clientNode.Curl(httpURL)
		assert.NoError(c, err)
		assert.Contains(c, stdout, "multi-http")
	}, 60*time.Second, 500*time.Millisecond, "peer should reach the multi-port HTTP endpoint")
	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		stdout, stderr, err := clientNode.Execute([]string{
			"python3",
			"-c",
			fmt.Sprintf("import socket; s=socket.create_connection((%q, 10081), timeout=5); s.sendall(b'ping'); print(s.recv(1024).decode()); s.close()", multiHost),
		})
		assert.NoError(c, err, stderr)
		assert.Contains(c, stdout, "multi:ping")
	}, 60*time.Second, 500*time.Millisecond, "peer should reach the multi-port TCP endpoint")

	require.NoError(t, serviceHost.Restart())
	require.NoError(t, serviceHost.WaitForRunning(integrationutil.PeerSyncTimeout()))
	require.NoError(t, scenario.WaitForTailscaleSync())

	// Container restart kills background Python listeners; re-start backends and
	// re-advertise serve config so peers can reach the service again.
	startServiceMultiBackends()
	advertiseServiceMulti()

	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		cfg := readServeStatusWithCollect(c, serviceHost)
		svc := cfg.Services["svc:multi"]
		if !assert.NotNil(c, svc) {
			return
		}
		httpHostPort := ipn.HostPort(net.JoinHostPort(multiHost, "80"))
		assert.Contains(c, svc.Web, httpHostPort)
		assert.Contains(c, svc.TCP, uint16(10081))
	}, 90*time.Second, 500*time.Millisecond, "service should be re-advertised after service-host reconnect")

	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		stdout, err := clientNode.Curl(httpURL)
		assert.NoError(c, err)
		assert.Contains(c, stdout, "multi-http")
	}, 120*time.Second, 500*time.Millisecond, "service should remain reachable after service-host reconnect")
}

func TestServeServiceHostPolicyPreventsVIPLeak(t *testing.T) {
	IntegrationSkip(t)

	acl := &policyv2.Policy{
		TagOwners: policyv2.TagOwners{
			"tag:service": policyv2.Owners{new(policyv2.Username("user1@"))},
		},
		AutoApprovers: policyv2.AutoApproverPolicy{
			Services: map[string]policyv2.AutoApprovers{
				"svc:approved": {new(policyv2.Tag("tag:service"))},
			},
		},
	}

	scenario, serviceHost, clientNode := newServiceHostPair(
		t,
		"serve-service-host-policy-no-vip-leak",
		[]hsic.Option{hsic.WithACLPolicy(acl)},
		[]tsic.Option{tsic.WithPackages("python3")},
		[]tsic.Option{tsic.WithPackages("curl"), tsic.WithDockerWorkdir("/")},
	)
	_ = scenario

	_, stderr, err := serviceHost.Execute([]string{
		"sh",
		"-c",
		"mkdir -p /tmp/serve-service-approved /tmp/serve-service-denied && printf 'approved\\n' >/tmp/serve-service-approved/index.html && printf 'denied\\n' >/tmp/serve-service-denied/index.html && python3 -m http.server 18092 --bind 127.0.0.1 --directory /tmp/serve-service-approved >/tmp/serve-service-approved.log 2>&1 & python3 -m http.server 18093 --bind 127.0.0.1 --directory /tmp/serve-service-denied >/tmp/serve-service-denied.log 2>&1 &",
	})
	require.NoError(t, err, stderr)

	_, stderr, err = serviceHost.Execute([]string{
		"tailscale", "serve", "--service=svc:approved", "--bg", "--http", "80", "http://127.0.0.1:18092",
	})
	require.NoError(t, err, stderr)
	_, stderr, err = serviceHost.Execute([]string{
		"tailscale", "serve", "--service=svc:denied", "--bg", "--http", "80", "http://127.0.0.1:18093",
	})
	require.NoError(t, err, stderr)

	var approvedURL, deniedURL string
	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		status, err := clientNode.Status()
		assert.NoError(c, err)
		if !assert.NotNil(c, status.CurrentTailnet) {
			return
		}
		approvedURL = fmt.Sprintf("http://approved.%s", status.CurrentTailnet.MagicDNSSuffix)
		deniedURL = fmt.Sprintf("http://denied.%s", status.CurrentTailnet.MagicDNSSuffix)
	}, 30*time.Second, 500*time.Millisecond, "client should have current tailnet status")

	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		stdout, err := clientNode.CurlFailFast(approvedURL)
		assert.NoError(c, err)
		assert.Contains(c, stdout, "approved")
	}, 60*time.Second, 500*time.Millisecond, "approved service should be reachable")

	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		_, err := clientNode.CurlFailFast(deniedURL)
		assert.Error(c, err)
	}, 60*time.Second, 500*time.Millisecond, "unapproved service should not leak VIP reachability")
}

func TestServeServiceHostPolicyRevocationWithdrawsVIP(t *testing.T) {
	IntegrationSkip(t)

	allowACL := &policyv2.Policy{
		TagOwners: policyv2.TagOwners{
			"tag:service": policyv2.Owners{new(policyv2.Username("user1@"))},
		},
		AutoApprovers: policyv2.AutoApproverPolicy{
			Services: map[string]policyv2.AutoApprovers{
				"svc:web": {new(policyv2.Tag("tag:service"))},
			},
		},
	}
	scenario, serviceHost, clientNode := newServiceHostPair(
		t,
		"serve-service-host-policy-revocation",
		[]hsic.Option{hsic.WithACLPolicy(allowACL)},
		[]tsic.Option{tsic.WithPackages("python3")},
		[]tsic.Option{tsic.WithPackages("curl"), tsic.WithDockerWorkdir("/")},
	)

	_, stderr, err := serviceHost.Execute([]string{
		"sh",
		"-c",
		"mkdir -p /tmp/serve-service-revoke && printf 'revoke-ok\\n' >/tmp/serve-service-revoke/index.html && python3 -m http.server 18120 --bind 127.0.0.1 --directory /tmp/serve-service-revoke >/tmp/serve-service-revoke.log 2>&1 &",
	})
	require.NoError(t, err, stderr)

	_, stderr, err = serviceHost.Execute([]string{
		"tailscale", "serve", "--service=svc:web", "--bg", "--http", "80", "http://127.0.0.1:18120",
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
		assert.Contains(c, stdout, "revoke-ok")
	}, 60*time.Second, 500*time.Millisecond, "service should be reachable before policy revocation")

	headscale, err := scenario.Headscale()
	require.NoError(t, err)
	denyACL := &policyv2.Policy{
		TagOwners: policyv2.TagOwners{
			"tag:service": policyv2.Owners{new(policyv2.Username("user1@"))},
		},
		AutoApprovers: policyv2.AutoApproverPolicy{
			Services: map[string]policyv2.AutoApprovers{
				"svc:other": {new(policyv2.Tag("tag:service"))},
			},
		},
	}
	require.NoError(t, headscale.SetPolicy(denyACL))

	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		_, err := clientNode.CurlFailFast(serviceURL)
		assert.Error(c, err)
	}, 60*time.Second, 500*time.Millisecond, "service should be withdrawn after policy revocation")
}

func TestServeServiceHostPolicyGrantPublishesVIP(t *testing.T) {
	IntegrationSkip(t)

	denyACL := &policyv2.Policy{
		TagOwners: policyv2.TagOwners{
			"tag:service": policyv2.Owners{new(policyv2.Username("user1@"))},
		},
		AutoApprovers: policyv2.AutoApproverPolicy{
			Services: map[string]policyv2.AutoApprovers{
				"svc:other": {new(policyv2.Tag("tag:service"))},
			},
		},
	}
	scenario, serviceHost, clientNode := newServiceHostPair(
		t,
		"serve-service-host-policy-grant",
		[]hsic.Option{hsic.WithACLPolicy(denyACL)},
		[]tsic.Option{tsic.WithPackages("python3")},
		[]tsic.Option{tsic.WithPackages("curl"), tsic.WithDockerWorkdir("/")},
	)

	_, stderr, err := serviceHost.Execute([]string{
		"sh",
		"-c",
		"mkdir -p /tmp/serve-service-grant && printf 'grant-ok\\n' >/tmp/serve-service-grant/index.html && python3 -m http.server 18121 --bind 127.0.0.1 --directory /tmp/serve-service-grant >/tmp/serve-service-grant.log 2>&1 &",
	})
	require.NoError(t, err, stderr)

	_, stderr, err = serviceHost.Execute([]string{
		"tailscale", "serve", "--service=svc:web", "--bg", "--http", "80", "http://127.0.0.1:18121",
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
		_, err := clientNode.CurlFailFast(serviceURL)
		assert.Error(c, err)
	}, 60*time.Second, 500*time.Millisecond, "service should remain unreachable while policy denies publication")

	headscale, err := scenario.Headscale()
	require.NoError(t, err)

	allowACL := &policyv2.Policy{
		TagOwners: policyv2.TagOwners{
			"tag:service": policyv2.Owners{new(policyv2.Username("user1@"))},
		},
		AutoApprovers: policyv2.AutoApproverPolicy{
			Services: map[string]policyv2.AutoApprovers{
				"svc:web": {new(policyv2.Tag("tag:service"))},
			},
		},
	}
	require.NoError(t, headscale.SetPolicy(allowACL))

	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		stdout, err := clientNode.CurlFailFast(serviceURL)
		assert.NoError(c, err)
		assert.Contains(c, stdout, "grant-ok")
	}, 60*time.Second, 500*time.Millisecond, "service should become reachable after policy grant without reconfiguration")
}

func newServiceHostPair(
	t *testing.T,
	testName string,
	headscaleOpts []hsic.Option,
	serviceHostOpts []tsic.Option,
	clientNodeOpts []tsic.Option,
) (*Scenario, TailscaleClient, TailscaleClient) {
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

	opts := append(
		[]hsic.Option{
			hsic.WithTestName(testName),
			hsic.WithConfigEnv(map[string]string{
				"HEADSCALE_SERVE_SERVICE_COLLECT": "true",
			}),
		},
		headscaleOpts...,
	)
	err = scenario.CreateHeadscaleEnv([]tsic.Option{}, opts...)
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
		append([]tsic.Option{
			tsic.WithNetwork(scenario.networks[scenario.testDefaultNetwork]),
			tsic.WithNetfilter("off"),
		}, serviceHostOpts...)...,
	)
	require.NoError(t, err)

	clientNode, err := scenario.CreateTailscaleNode(
		"head",
		append([]tsic.Option{
			tsic.WithNetwork(scenario.networks[scenario.testDefaultNetwork]),
			tsic.WithNetfilter("off"),
			tsic.WithAcceptRoutes(),
		}, clientNodeOpts...)...,
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

	return scenario, serviceHost, clientNode
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
