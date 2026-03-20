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

func TestServeHTTPPeerReachability(t *testing.T) {
	IntegrationSkip(t)

	spec := ScenarioSpec{
		NodesPerUser: 0,
		Users:        []string{"user1"},
		MaxWait:      dockertestMaxWait(),
	}

	scenario, err := NewScenario(spec)
	require.NoError(t, err)
	defer scenario.ShutdownAssertNoPanics(t)

	err = scenario.CreateHeadscaleEnv(
		[]tsic.Option{},
		hsic.WithTestName("serve-http-peer"),
	)
	requireNoErrHeadscaleEnv(t, err)

	headscale, err := scenario.Headscale()
	require.NoError(t, err)

	user, err := GetUserByName(headscale, "user1")
	require.NoError(t, err)

	authKey, err := scenario.CreatePreAuthKey(user.GetId(), true, false)
	require.NoError(t, err)

	serveNode, err := scenario.CreateTailscaleNode(
		"head",
		tsic.WithNetwork(scenario.networks[scenario.testDefaultNetwork]),
		tsic.WithNetfilter("off"),
	)
	require.NoError(t, err)

	clientNode, err := scenario.CreateTailscaleNode(
		"head",
		tsic.WithNetwork(scenario.networks[scenario.testDefaultNetwork]),
		tsic.WithPackages("curl"),
		tsic.WithDockerWorkdir("/"),
		tsic.WithNetfilter("off"),
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
	serveFQDN = trimDotSuffix(serveFQDN)

	_, stderr, err := serveNode.Execute([]string{
		"tailscale", "serve", "--bg", "--http", "80", "text:serve-ok",
	})
	require.NoError(t, err, stderr)

	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		stdout, stderr, err := serveNode.Execute([]string{"tailscale", "serve", "status", "--json"})
		assert.NoError(c, err, stderr)

		var cfg ipn.ServeConfig
		err = json.Unmarshal([]byte(stdout), &cfg)
		assert.NoError(c, err)
		assert.NotNil(c, cfg.Web)

		hostPort := ipn.HostPort(net.JoinHostPort(serveFQDN, "80"))
		assert.Contains(c, cfg.Web, hostPort)
	}, 30*time.Second, 500*time.Millisecond, "serve status should report the HTTP handler")

	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		result, err := clientNode.Curl(fmt.Sprintf("http://%s", serveFQDN))
		assert.NoError(c, err)
		assert.Contains(c, result, "serve-ok")
	}, 30*time.Second, 500*time.Millisecond, "peer should reach the served HTTP endpoint")
}

func trimDotSuffix(name string) string {
	if len(name) > 0 && name[len(name)-1] == '.' {
		return name[:len(name)-1]
	}

	return name
}
