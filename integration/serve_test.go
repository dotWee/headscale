package integration

import (
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/juanfont/headscale/integration/hsic"
	"github.com/juanfont/headscale/integration/tsic"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"tailscale.com/tailcfg"
)

// TestServeCapabilitiesEnabled verifies that when serve is enabled
// (the default), nodes receive the CapabilityHTTPS capability in
// their CapMap, allowing `tailscale serve` to function.
func TestServeCapabilitiesEnabled(t *testing.T) {
	IntegrationSkip(t)

	spec := ScenarioSpec{
		NodesPerUser: 1,
		Users:        []string{"serve-user"},
	}

	scenario, err := NewScenario(spec)

	require.NoError(t, err)
	defer scenario.ShutdownAssertNoPanics(t)

	// Serve is enabled by default, no need to override.
	headscale, err := scenario.Headscale(
		hsic.WithTestName("serveenabled"),
	)
	requireNoErrHeadscaleEnv(t, err)

	user, err := scenario.CreateUser("serve-user")
	require.NoError(t, err)

	err = scenario.CreateTailscaleNodesInUser(
		"serve-user",
		"all",
		spec.NodesPerUser,
		tsic.WithNetwork(scenario.networks[scenario.testDefaultNetwork]),
	)
	require.NoError(t, err)

	key, err := scenario.CreatePreAuthKey(user.GetId(), true, false)
	require.NoError(t, err)

	err = scenario.RunTailscaleUp(
		"serve-user",
		headscale.GetEndpoint(),
		key.GetKey(),
	)
	require.NoError(t, err)

	err = scenario.WaitForTailscaleSync()
	requireNoErrSync(t, err)

	allClients, err := scenario.ListTailscaleClients()
	requireNoErrListClients(t, err)
	require.Len(t, allClients, 1)

	client := allClients[0]

	// Verify the client received the HTTPS capability.
	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		status, err := client.Status()
		assert.NoError(c, err)

		assert.True(c,
			status.Self.HasCap(tailcfg.CapabilityHTTPS),
			"node should have CapabilityHTTPS when serve is enabled",
		)
	}, 10*time.Second, 500*time.Millisecond,
		"node should receive CapabilityHTTPS capability",
	)
}

// TestServeCertDomains verifies that when serve is enabled, the node's
// CertDomains field is populated with its FQDN, enabling HTTPS cert
// provisioning. This is a prerequisite for `tailscale serve` HTTPS mode.
func TestServeCertDomains(t *testing.T) {
	IntegrationSkip(t)

	spec := ScenarioSpec{
		NodesPerUser: 1,
		Users:        []string{"certdom-user"},
	}

	scenario, err := NewScenario(spec)

	require.NoError(t, err)
	defer scenario.ShutdownAssertNoPanics(t)

	headscale, err := scenario.Headscale(
		hsic.WithTestName("certdomains"),
	)
	requireNoErrHeadscaleEnv(t, err)

	user, err := scenario.CreateUser("certdom-user")
	require.NoError(t, err)

	err = scenario.CreateTailscaleNodesInUser(
		"certdom-user",
		"all",
		spec.NodesPerUser,
		tsic.WithNetwork(scenario.networks[scenario.testDefaultNetwork]),
	)
	require.NoError(t, err)

	key, err := scenario.CreatePreAuthKey(user.GetId(), true, false)
	require.NoError(t, err)

	err = scenario.RunTailscaleUp(
		"certdom-user",
		headscale.GetEndpoint(),
		key.GetKey(),
	)
	require.NoError(t, err)

	err = scenario.WaitForTailscaleSync()
	requireNoErrSync(t, err)

	allClients, err := scenario.ListTailscaleClients()
	requireNoErrListClients(t, err)
	require.Len(t, allClients, 1)

	client := allClients[0]

	// Verify the client's CertDomains contains its FQDN.
	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		status, err := client.Status()
		assert.NoError(c, err)

		assert.NotEmpty(c, status.CertDomains,
			"CertDomains should be populated when serve is enabled",
		)

		if len(status.CertDomains) > 0 {
			// CertDomains should contain the node's FQDN without trailing dot.
			fqdn := strings.TrimSuffix(client.MustFQDN(), ".")
			assert.Contains(c, status.CertDomains, fqdn,
				"CertDomains should contain the node's FQDN",
			)
		}
	}, 10*time.Second, 500*time.Millisecond,
		"node should receive CertDomains in its DNS config",
	)
}

// TestServeCapabilitiesDisabled verifies that when serve is explicitly
// disabled, nodes do NOT receive the CapabilityHTTPS capability.
func TestServeCapabilitiesDisabled(t *testing.T) {
	IntegrationSkip(t)

	spec := ScenarioSpec{
		NodesPerUser: 1,
		Users:        []string{"serve-disabled"},
	}

	scenario, err := NewScenario(spec)

	require.NoError(t, err)
	defer scenario.ShutdownAssertNoPanics(t)

	headscale, err := scenario.Headscale(
		hsic.WithTestName("servedisabled"),
		hsic.WithConfigEnv(map[string]string{
			"HEADSCALE_SERVE_ENABLED": "false",
		}),
	)
	requireNoErrHeadscaleEnv(t, err)

	user, err := scenario.CreateUser("serve-disabled")
	require.NoError(t, err)

	err = scenario.CreateTailscaleNodesInUser(
		"serve-disabled",
		"all",
		spec.NodesPerUser,
		tsic.WithNetwork(scenario.networks[scenario.testDefaultNetwork]),
	)
	require.NoError(t, err)

	key, err := scenario.CreatePreAuthKey(user.GetId(), true, false)
	require.NoError(t, err)

	err = scenario.RunTailscaleUp(
		"serve-disabled",
		headscale.GetEndpoint(),
		key.GetKey(),
	)
	require.NoError(t, err)

	err = scenario.WaitForTailscaleSync()
	requireNoErrSync(t, err)

	allClients, err := scenario.ListTailscaleClients()
	requireNoErrListClients(t, err)
	require.Len(t, allClients, 1)

	client := allClients[0]

	// Verify the client did NOT receive the HTTPS capability.
	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		status, err := client.Status()
		assert.NoError(c, err)

		assert.False(c,
			status.Self.HasCap(tailcfg.CapabilityHTTPS),
			"node should NOT have CapabilityHTTPS when serve is disabled",
		)
	}, 10*time.Second, 500*time.Millisecond,
		"node should not receive CapabilityHTTPS capability",
	)
}

// TestFunnelCapabilities verifies that when funnel is enabled,
// nodes receive NodeAttrFunnel and CapabilityFunnelPorts capabilities.
func TestFunnelCapabilities(t *testing.T) {
	IntegrationSkip(t)

	spec := ScenarioSpec{
		NodesPerUser: 1,
		Users:        []string{"funnel-user"},
	}

	scenario, err := NewScenario(spec)

	require.NoError(t, err)
	defer scenario.ShutdownAssertNoPanics(t)

	headscale, err := scenario.Headscale(
		hsic.WithTestName("funnelcaps"),
		hsic.WithConfigEnv(map[string]string{
			"HEADSCALE_SERVE_ENABLED":  "true",
			"HEADSCALE_FUNNEL_ENABLED": "true",
		}),
	)
	requireNoErrHeadscaleEnv(t, err)

	user, err := scenario.CreateUser("funnel-user")
	require.NoError(t, err)

	err = scenario.CreateTailscaleNodesInUser(
		"funnel-user",
		"all",
		spec.NodesPerUser,
		tsic.WithNetwork(scenario.networks[scenario.testDefaultNetwork]),
	)
	require.NoError(t, err)

	key, err := scenario.CreatePreAuthKey(user.GetId(), true, false)
	require.NoError(t, err)

	err = scenario.RunTailscaleUp(
		"funnel-user",
		headscale.GetEndpoint(),
		key.GetKey(),
	)
	require.NoError(t, err)

	err = scenario.WaitForTailscaleSync()
	requireNoErrSync(t, err)

	allClients, err := scenario.ListTailscaleClients()
	requireNoErrListClients(t, err)
	require.Len(t, allClients, 1)

	client := allClients[0]

	// Verify the client received HTTPS, Funnel, and FunnelPorts capabilities.
	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		status, err := client.Status()
		assert.NoError(c, err)

		assert.True(c,
			status.Self.HasCap(tailcfg.CapabilityHTTPS),
			"node should have CapabilityHTTPS when funnel is enabled",
		)
		assert.True(c,
			status.Self.HasCap(tailcfg.NodeAttrFunnel),
			"node should have NodeAttrFunnel when funnel is enabled",
		)

		// Check that a FunnelPorts capability key exists in the CapMap.
		hasFunnelPorts := false

		for cap := range status.Self.CapMap {
			if cap == tailcfg.CapabilityFunnelPorts || len(cap) > len(tailcfg.CapabilityFunnelPorts) &&
				string(cap[:len(tailcfg.CapabilityFunnelPorts)]) == string(tailcfg.CapabilityFunnelPorts) {
				hasFunnelPorts = true

				break
			}
		}

		assert.True(c, hasFunnelPorts,
			"node should have CapabilityFunnelPorts when funnel is enabled",
		)
	}, 10*time.Second, 500*time.Millisecond,
		"node should receive funnel capabilities",
	)
}

// TestServeHTTPEndToEnd is an end-to-end integration test for `tailscale serve`.
// It sets up two nodes in the same tailnet:
//   - server: runs a local HTTP server on localhost:8080, then uses
//     `tailscale serve` to proxy tailnet port 80 → localhost:8080
//   - consumer: curls the served website via the server's tailnet FQDN
//
// This verifies the full serve flow: the Headscale control plane signals
// the CapabilityHTTPS capability, the Tailscale client accepts the serve
// configuration, and a peer can reach the proxied service.
func TestServeHTTPEndToEnd(t *testing.T) {
	IntegrationSkip(t)

	spec := ScenarioSpec{
		NodesPerUser: 0, // manual node creation
		Users:        []string{"serve-e2e"},
	}

	scenario, err := NewScenario(spec)

	require.NoError(t, err)
	defer scenario.ShutdownAssertNoPanics(t)

	// Serve is enabled by default.
	headscale, err := scenario.Headscale(
		hsic.WithTestName("serve-e2e"),
	)
	requireNoErrHeadscaleEnv(t, err)

	user, err := scenario.CreateUser("serve-e2e")
	require.NoError(t, err)

	network := scenario.networks[scenario.testDefaultNetwork]

	// Create two nodes manually: server and consumer.
	// The server node runs a Python HTTP server bound to localhost:8080.
	// We use WithExtraCommands to start the webserver on 127.0.0.1 only,
	// so it is NOT directly reachable by peers — only through tailscale serve.
	err = scenario.CreateTailscaleNodesInUser(
		"serve-e2e",
		"head",
		2,
		tsic.WithNetwork(network),
		tsic.WithNetfilter("off"),
		tsic.WithPackages("python3", "curl"),
		tsic.WithExtraCommands(
			"mkdir -p /srv/www",
			`echo "Hello from Tailscale Serve!" > /srv/www/index.html`,
			"(cd /srv/www && python3 -m http.server --bind 127.0.0.1 8080 &)",
		),
	)
	require.NoError(t, err)

	key, err := scenario.CreatePreAuthKey(user.GetId(), true, false)
	require.NoError(t, err)

	err = scenario.RunTailscaleUp(
		"serve-e2e",
		headscale.GetEndpoint(),
		key.GetKey(),
	)
	require.NoError(t, err)

	err = scenario.WaitForTailscaleSync()
	requireNoErrSync(t, err)

	allClients, err := scenario.ListTailscaleClients()
	requireNoErrListClients(t, err)
	require.Len(t, allClients, 2)

	server := allClients[0]
	consumer := allClients[1]

	// Verify the server node has the HTTPS capability.
	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		status, err := server.Status()
		assert.NoError(c, err)

		assert.True(c,
			status.Self.HasCap(tailcfg.CapabilityHTTPS),
			"server should have CapabilityHTTPS",
		)
	}, 10*time.Second, 500*time.Millisecond,
		"server should receive CapabilityHTTPS capability",
	)

	// Verify the local webserver is running on the server node.
	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		result, err := server.Curl("http://127.0.0.1:8080/index.html")
		assert.NoError(c, err)
		assert.Contains(c, result, "Hello from Tailscale Serve!")
	}, 15*time.Second, 1*time.Second,
		"local webserver should be running on server",
	)

	// Configure tailscale serve on the server node:
	// Proxy HTTP port 80 on the tailnet → localhost:8080
	// Uses the v2 serve CLI: tailscale serve --bg --http=80 http://127.0.0.1:8080
	t.Log("Configuring tailscale serve on server node...")

	stdout, stderr, err := server.Execute([]string{
		"tailscale", "serve", "--bg", "--http=80", "http://127.0.0.1:8080",
	})
	require.NoErrorf(t, err,
		"tailscale serve failed: stdout=%s stderr=%s", stdout, stderr,
	)

	// Verify serve status shows the configuration.
	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		stdout, _, err := server.Execute([]string{
			"tailscale", "serve", "status",
		})
		assert.NoError(c, err)
		assert.Contains(c, stdout, "http://127.0.0.1:8080",
			"serve status should show the proxy target",
		)
	}, 10*time.Second, 1*time.Second,
		"tailscale serve should report configured status",
	)

	// Get the server's FQDN for building the URL.
	serverFQDN := strings.TrimSuffix(server.MustFQDN(), ".")

	// Build the URL: use HTTP on port 80, which is what we configured.
	targetURL := fmt.Sprintf("http://%s/index.html", serverFQDN)
	t.Logf("Consumer will curl: %s", targetURL)

	// From the consumer node, curl the served website.
	// The traffic flow is:
	//   consumer → tailscale network → server's tailscale daemon
	//   → tailscale serve proxy → localhost:8080 → Python HTTP server
	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		result, err := consumer.Curl(
			targetURL,
			tsic.WithCurlMaxTime(10*time.Second),
		)
		assert.NoError(c, err)
		assert.Contains(c, result, "Hello from Tailscale Serve!",
			"consumer should see the served content from server",
		)
	}, 30*time.Second, 2*time.Second,
		"consumer should be able to reach server's served website",
	)

	// Additionally verify the consumer CANNOT reach the webserver directly
	// on port 8080 (since it's bound to localhost only on the server).
	serverIPv4 := server.MustIPv4()
	directURL := "http://" + net.JoinHostPort(serverIPv4.String(), "8080") + "/index.html"

	_, err = consumer.CurlFailFast(directURL)
	assert.Error(t, err,
		"consumer should NOT be able to reach port 8080 directly (localhost-only binding)",
	)
}
