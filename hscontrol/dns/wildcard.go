package dns

import (
	"context"
	"fmt"
	"strings"

	"github.com/rs/zerolog/log"
	"tailscale.com/tailcfg"
	"tailscale.com/types/views"

	"github.com/juanfont/headscale/hscontrol/types"
)

// WildcardResolver handles wildcard DNS resolution for *.base_domain queries
type WildcardResolver struct {
	state StateReader
	cfg   ConfigReader
}

// StateReader interface provides read access to state for wildcard DNS resolution
type StateReader interface {
	ListNodes(nodeIDs ...types.NodeID) views.Slice[types.NodeView]
}

// ConfigReader interface provides read access to configuration for wildcard DNS resolution
type ConfigReader interface {
	GetDNSConfig() types.DNSConfig
}

// configWrapper wraps types.Config to implement ConfigReader
type configWrapper struct {
	cfg *types.Config
}

func (c *configWrapper) GetDNSConfig() types.DNSConfig {
	return c.cfg.DNSConfig
}

// NewWildcardResolver creates a new wildcard DNS resolver
func NewWildcardResolver(state StateReader, cfg *types.Config) *WildcardResolver {
	return &WildcardResolver{
		state: state,
		cfg:   &configWrapper{cfg: cfg},
	}
}

// ResolveWildcardDNS resolves wildcard DNS queries like *.base_domain to node IPs
func (wr *WildcardResolver) ResolveWildcardDNS(ctx context.Context, query string) ([]tailcfg.DNSRecord, error) {
	var records []tailcfg.DNSRecord

	// Check if wildcard DNS is enabled and we have a base domain
	dnsConfig := wr.cfg.GetDNSConfig()
	if !dnsConfig.WildcardDNS || dnsConfig.BaseDomain == "" {
		return records, nil
	}

	// Check if query matches wildcard pattern for base domain
	if !wr.isWildcardQueryForBaseDomain(query) {
		return records, nil
	}

	// Extract hostname from wildcard query
	hostname, err := wr.extractHostnameFromWildcard(query)
	if err != nil {
		log.Debug().Err(err).Str("query", query).Msg("failed to extract hostname from wildcard query")
		return records, nil
	}

	// Find node by hostname
	node, err := wr.findNodeByHostname(ctx, hostname)
	if err != nil {
		log.Debug().Err(err).Str("hostname", hostname).Msg("failed to find node by hostname")
		return records, nil
	}

	// Generate DNS records for the node
	nodeRecords := wr.generateDNSRecordsForNode(node, query)
	records = append(records, nodeRecords...)

	log.Debug().
		Str("query", query).
		Str("hostname", hostname).
		Str("node", node.Hostname()).
		Int("records", len(records)).
		Msg("resolved wildcard DNS query")

	return records, nil
}

// isWildcardQueryForBaseDomain checks if query matches *.base_domain pattern
func (wr *WildcardResolver) isWildcardQueryForBaseDomain(query string) bool {
	// Remove trailing dot if present
	query = strings.TrimSuffix(query, ".")

	dnsConfig := wr.cfg.GetDNSConfig()
	baseDomain := strings.TrimSuffix(dnsConfig.BaseDomain, ".")

	// Check if query ends with .base_domain or is exactly the base domain
	if query == baseDomain {
		return false // Base domain itself is not a wildcard query
	}

	// For wildcard queries, we need to check if query ends with .base_domain
	// Example: query="testnode.example.com", baseDomain="example.com" should match
	result := strings.HasSuffix(query, "."+baseDomain)
	log.Debug().
		Str("query", query).
		Str("baseDomain", baseDomain).
		Str("expectedSuffix", "."+baseDomain).
		Bool("result", result).
		Msg("checking wildcard query pattern")

	return result
}

// extractHostnameFromWildcard extracts hostname from wildcard query like node.base_domain
func (wr *WildcardResolver) extractHostnameFromWildcard(query string) (string, error) {
	query = strings.TrimSuffix(query, ".")

	dnsConfig := wr.cfg.GetDNSConfig()
	baseDomain := strings.TrimSuffix(dnsConfig.BaseDomain, ".")

	log.Debug().
		Str("query", query).
		Str("baseDomain", baseDomain).
		Msg("extracting hostname from wildcard")

	// If query is exactly the base domain, return empty (should be handled by normal DNS)
	if query == baseDomain {
		return "", fmt.Errorf("query is base domain, not wildcard")
	}

	// Query should be hostname.base_domain
	// Check if the query actually ends with .base_domain
	if !strings.HasSuffix(query, "."+baseDomain) {
		log.Debug().
			Str("query", query).
			Str("expectedSuffix", "."+baseDomain).
			Msg("query does not match base domain")
		return "", fmt.Errorf("query does not match base domain")
	}

	// Extract hostname (everything before .base_domain)
	// Remove the .base_domain suffix to get the hostname
	hostname := strings.TrimSuffix(query, "."+baseDomain)
	log.Debug().
		Str("query", query).
		Str("hostname", hostname).
		Msg("extracted hostname from wildcard")

	return hostname, nil
}

// findNodeByHostname finds a node by its hostname
func (wr *WildcardResolver) findNodeByHostname(ctx context.Context, hostname string) (types.NodeView, error) {
	nodes := wr.state.ListNodes()

	for _, node := range nodes.All() {
		nodeName := node.Hostname()
		if nodeName == hostname {
			return node, nil
		}
	}

	return types.NodeView{}, fmt.Errorf("node not found: %s", hostname)
}

// generateDNSRecordsForNode generates DNS records for a node
func (wr *WildcardResolver) generateDNSRecordsForNode(node types.NodeView, originalQuery string) []tailcfg.DNSRecord {
	var records []tailcfg.DNSRecord

	// Get node IP prefixes
	prefixes := node.Prefixes()

	for _, prefix := range prefixes {
		// Create A or AAAA record depending on IP version
		record := tailcfg.DNSRecord{
			Name:  originalQuery,
			Type:  wr.getRecordType(prefix.Addr().String()),
			Value: prefix.Addr().String(),
		}
		records = append(records, record)
	}

	return records
}

// getRecordType returns the DNS record type (A or AAAA) for the given IP
func (wr *WildcardResolver) getRecordType(ip string) string {
	if strings.Contains(ip, ":") {
		return "AAAA"
	}
	return "A"
}

// GetWildcardDNSRecords returns DNS records for wildcard queries in a batch
func (wr *WildcardResolver) GetWildcardDNSRecords(ctx context.Context, queries []string) ([]tailcfg.DNSRecord, error) {
	var allRecords []tailcfg.DNSRecord

	for _, query := range queries {
		records, err := wr.ResolveWildcardDNS(ctx, query)
		if err != nil {
			log.Debug().Err(err).Str("query", query).Msg("failed to resolve wildcard query")
			continue
		}
		allRecords = append(allRecords, records...)
	}

	return allRecords, nil
}

// IsWildcardEnabled returns true if wildcard DNS is enabled
func (wr *WildcardResolver) IsWildcardEnabled() bool {
	dnsConfig := wr.cfg.GetDNSConfig()
	return dnsConfig.WildcardDNS && dnsConfig.BaseDomain != ""
}

// GetDNSConfig returns the DNS configuration
func (wr *WildcardResolver) GetDNSConfig() types.DNSConfig {
	return wr.cfg.GetDNSConfig()
}
