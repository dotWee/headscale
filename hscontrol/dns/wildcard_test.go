package dns

import (
	"context"
	"net/netip"
	"testing"

	"github.com/juanfont/headscale/hscontrol/types"
	"tailscale.com/tailcfg"
	"tailscale.com/types/views"
)

// mockStateReader implements StateReader for testing
type mockStateReader struct {
	nodes []types.NodeView
}

func (m *mockStateReader) ListNodes(nodeIDs ...types.NodeID) views.Slice[types.NodeView] {
	return views.SliceOf(m.nodes)
}

// mockConfigReader implements ConfigReader for testing
type mockConfigReader struct {
	dnsConfig types.DNSConfig
}

func (m *mockConfigReader) GetDNSConfig() types.DNSConfig {
	return m.dnsConfig
}

// createTestNode creates a test node with given name and IPs
func createTestNode(name string, ips ...string) types.NodeView {
	var nodeIPs []netip.Addr
	for _, ip := range ips {
		nodeIPs = append(nodeIPs, netip.MustParseAddr(ip))
	}

	node := &types.Node{
		Hostname:  name,
		GivenName: name,
		IPv4:      nil,
		IPv6:      nil,
	}

	// Set IPs based on the provided addresses
	if len(nodeIPs) > 0 {
		if nodeIPs[0].Is4() {
			node.IPv4 = &nodeIPs[0]
		}
		if len(nodeIPs) > 1 && nodeIPs[1].Is6() {
			node.IPv6 = &nodeIPs[1]
		}
	}

	return node.View()
}

func TestWildcardResolver_ResolveWildcardDNS(t *testing.T) {
	tests := []struct {
		name        string
		query       string
		dnsConfig   types.DNSConfig
		nodes       []types.NodeView
		expectedLen int
		expectError bool
	}{
		{
			name:  "valid wildcard query for existing node",
			query: "testnode.example.com",
			dnsConfig: types.DNSConfig{
				WildcardDNS: true,
				BaseDomain:  "example.com",
			},
			nodes: []types.NodeView{
				createTestNode("testnode", "100.64.0.1", "fd7a:115c:a1e0::1"),
			},
			expectedLen: 2, // A and AAAA records
			expectError: false,
		},
		{
			name:  "wildcard query for non-existent node",
			query: "nonexistent.example.com",
			dnsConfig: types.DNSConfig{
				WildcardDNS: true,
				BaseDomain:  "example.com",
			},
			nodes: []types.NodeView{
				createTestNode("testnode", "100.64.0.1"),
			},
			expectedLen: 0,
			expectError: false,
		},
		{
			name:  "query for base domain itself",
			query: "example.com",
			dnsConfig: types.DNSConfig{
				WildcardDNS: true,
				BaseDomain:  "example.com",
			},
			nodes:       []types.NodeView{},
			expectedLen: 0,
			expectError: false,
		},
		{
			name:  "wildcard DNS disabled",
			query: "testnode.example.com",
			dnsConfig: types.DNSConfig{
				WildcardDNS: false,
				BaseDomain:  "example.com",
			},
			nodes: []types.NodeView{
				createTestNode("testnode", "100.64.0.1"),
			},
			expectedLen: 0,
			expectError: false,
		},
		{
			name:  "invalid query format",
			query: "invalid.query",
			dnsConfig: types.DNSConfig{
				WildcardDNS: true,
				BaseDomain:  "example.com",
			},
			nodes:       []types.NodeView{},
			expectedLen: 0,
			expectError: false,
		},
		{
			name:  "query with subdomain",
			query: "sub.testnode.example.com",
			dnsConfig: types.DNSConfig{
				WildcardDNS: true,
				BaseDomain:  "example.com",
			},
			nodes: []types.NodeView{
				createTestNode("testnode", "100.64.0.1"),
			},
			expectedLen: 0, // Should not match
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stateReader := &mockStateReader{nodes: tt.nodes}
			configReader := &mockConfigReader{dnsConfig: tt.dnsConfig}

			resolver := &WildcardResolver{
				state: stateReader,
				cfg:   configReader,
			}

			ctx := context.Background()
			records, err := resolver.ResolveWildcardDNS(ctx, tt.query)

			if tt.expectError {
				if err == nil {
					t.Errorf("expected error but got none")
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if len(records) != tt.expectedLen {
					t.Errorf("expected %d records, got %d", tt.expectedLen, len(records))
				}
			}
		})
	}
}

func TestWildcardResolver_IsWildcardEnabled(t *testing.T) {
	tests := []struct {
		name      string
		dnsConfig types.DNSConfig
		expected  bool
	}{
		{
			name: "wildcard DNS enabled with base domain",
			dnsConfig: types.DNSConfig{
				WildcardDNS: true,
				BaseDomain:  "example.com",
			},
			expected: true,
		},
		{
			name: "wildcard DNS disabled",
			dnsConfig: types.DNSConfig{
				WildcardDNS: false,
				BaseDomain:  "example.com",
			},
			expected: false,
		},
		{
			name: "no base domain",
			dnsConfig: types.DNSConfig{
				WildcardDNS: true,
				BaseDomain:  "",
			},
			expected: false,
		},
		{
			name: "both disabled and no base domain",
			dnsConfig: types.DNSConfig{
				WildcardDNS: false,
				BaseDomain:  "",
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configReader := &mockConfigReader{dnsConfig: tt.dnsConfig}
			resolver := &WildcardResolver{
				cfg: configReader,
			}

			result := resolver.IsWildcardEnabled()
			if result != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestWildcardResolver_ExtractHostnameFromWildcard(t *testing.T) {
	resolver := &WildcardResolver{}

	tests := []struct {
		name        string
		query       string
		baseDomain  string
		expected    string
		expectError bool
	}{
		{
			name:       "valid wildcard query",
			query:      "testnode.example.com",
			baseDomain: "example.com",
			expected:   "testnode",
		},
		{
			name:       "query with subdomain",
			query:      "sub.testnode.example.com",
			baseDomain: "example.com",
			expected:   "sub.testnode",
		},
		{
			name:        "base domain query",
			query:       "example.com",
			baseDomain:  "example.com",
			expectError: true,
		},
		{
			name:        "invalid domain",
			query:       "testnode.invalid.com",
			baseDomain:  "example.com",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set up config for this test
			dnsConfig := types.DNSConfig{
				BaseDomain: tt.baseDomain,
			}
			configReader := &mockConfigReader{dnsConfig: dnsConfig}
			resolver.cfg = configReader

			result, err := resolver.extractHostnameFromWildcard(tt.query)

			if tt.expectError {
				if err == nil {
					t.Errorf("expected error but got none")
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if result != tt.expected {
					t.Errorf("expected %q, got %q", tt.expected, result)
				}
			}
		})
	}
}

func TestWildcardResolver_GenerateDNSRecordsForNode(t *testing.T) {
	resolver := &WildcardResolver{}

	// Create a test node with both IPv4 and IPv6
	node := createTestNode("testnode", "100.64.0.1", "fd7a:115c:a1e0::1")

	records := resolver.generateDNSRecordsForNode(node, "testnode.example.com")

	// Should have 2 records (A and AAAA)
	if len(records) != 2 {
		t.Errorf("expected 2 records, got %d", len(records))
	}

	// Check that we have both A and AAAA records
	recordTypes := make(map[string]bool)
	for _, record := range records {
		recordTypes[record.Type] = true
		if record.Name != "testnode.example.com" {
			t.Errorf("expected name %q, got %q", "testnode.example.com", record.Name)
		}
	}

	if !recordTypes["A"] {
		t.Error("missing A record")
	}
	if !recordTypes["AAAA"] {
		t.Error("missing AAAA record")
	}

	// Check that IPv4 record has correct value
	var ipv4Record, ipv6Record *tailcfg.DNSRecord
	for i := range records {
		if records[i].Type == "A" {
			ipv4Record = &records[i]
		} else if records[i].Type == "AAAA" {
			ipv6Record = &records[i]
		}
	}

	if ipv4Record != nil && ipv4Record.Value != "100.64.0.1" {
		t.Errorf("expected IPv4 value %q, got %q", "100.64.0.1", ipv4Record.Value)
	}

	if ipv6Record != nil && ipv6Record.Value != "fd7a:115c:a1e0::1" {
		t.Errorf("expected IPv6 value %q, got %q", "fd7a:115c:a1e0::1", ipv6Record.Value)
	}
}
