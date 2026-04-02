package dns

import (
	"strings"
	"sync"

	"tailscale.com/tailcfg"
)

// ACMEChallengeStore is a thread-safe in-memory store for ACME DNS-01
// challenge TXT records. Tailscale clients use the /machine/set-dns
// endpoint to create these records when provisioning TLS certificates
// for tailscale serve HTTPS.
//
// Records are keyed by domain name and automatically served as
// ExtraRecords in the DNSConfig sent to all nodes.
type ACMEChallengeStore struct {
	mu      sync.RWMutex
	records map[string]string // domain name → TXT value
}

// NewACMEChallengeStore creates a new empty ACME challenge store.
func NewACMEChallengeStore() *ACMEChallengeStore {
	return &ACMEChallengeStore{
		records: make(map[string]string),
	}
}

// SetRecord stores (or overwrites) an ACME challenge TXT record.
func (s *ACMEChallengeStore) SetRecord(name, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Normalize: strip trailing dot for consistent lookup.
	name = strings.TrimSuffix(name, ".")

	s.records[name] = value
}

// RemoveRecord removes an ACME challenge TXT record.
func (s *ACMEChallengeStore) RemoveRecord(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	name = strings.TrimSuffix(name, ".")
	delete(s.records, name)
}

// Records returns all stored ACME challenge records as
// tailcfg.DNSRecord entries suitable for ExtraRecords.
func (s *ACMEChallengeStore) Records() []tailcfg.DNSRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.records) == 0 {
		return nil
	}

	records := make([]tailcfg.DNSRecord, 0, len(s.records))
	for name, value := range s.records {
		records = append(records, tailcfg.DNSRecord{
			Name:  name,
			Type:  "TXT",
			Value: value,
		})
	}

	return records
}

// Len returns the number of stored records.
func (s *ACMEChallengeStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return len(s.records)
}
