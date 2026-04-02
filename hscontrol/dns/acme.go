package dns

import (
	"strings"
	"sync"
	"time"

	"tailscale.com/tailcfg"
)

const defaultACMETTL = 10 * time.Minute

// acmeRecord holds a TXT value together with its creation timestamp
// for TTL-based expiry.
type acmeRecord struct {
	value     string
	createdAt time.Time
}

// ACMEChallengeStore is a thread-safe in-memory store for ACME DNS-01
// challenge TXT records. Tailscale clients use the /machine/set-dns
// endpoint to create these records when provisioning TLS certificates
// for tailscale serve HTTPS.
//
// Records expire after a configurable TTL (default 10 minutes).
// Expired records are excluded from Records() and removed by
// CleanExpired().
type ACMEChallengeStore struct {
	mu      sync.RWMutex
	records map[string]acmeRecord
	ttl     time.Duration

	// nowFunc returns the current time. It can be overridden in
	// tests to control TTL expiry without sleeping.
	nowFunc func() time.Time
}

// NewACMEChallengeStore creates a new empty ACME challenge store
// with the default TTL.
func NewACMEChallengeStore() *ACMEChallengeStore {
	return &ACMEChallengeStore{
		records: make(map[string]acmeRecord),
		ttl:     defaultACMETTL,
		nowFunc: time.Now,
	}
}

// SetRecord stores (or overwrites) an ACME challenge TXT record.
// The record's TTL timer starts from the time of this call.
func (s *ACMEChallengeStore) SetRecord(name, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	name = strings.TrimSuffix(name, ".")

	s.records[name] = acmeRecord{
		value:     value,
		createdAt: s.nowFunc(),
	}
}

// RemoveRecord removes an ACME challenge TXT record.
func (s *ACMEChallengeStore) RemoveRecord(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	name = strings.TrimSuffix(name, ".")
	delete(s.records, name)
}

// Records returns all non-expired ACME challenge records as
// tailcfg.DNSRecord entries suitable for ExtraRecords.
func (s *ACMEChallengeStore) Records() []tailcfg.DNSRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()

	now := s.nowFunc()

	var records []tailcfg.DNSRecord

	for name, rec := range s.records {
		if now.Sub(rec.createdAt) >= s.ttl {
			continue // expired
		}

		records = append(records, tailcfg.DNSRecord{
			Name:  name,
			Type:  "TXT",
			Value: rec.value,
		})
	}

	return records
}

// CleanExpired removes all records that have exceeded their TTL.
// It returns the number of records removed.
func (s *ACMEChallengeStore) CleanExpired() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.nowFunc()
	removed := 0

	for name, rec := range s.records {
		if now.Sub(rec.createdAt) >= s.ttl {
			delete(s.records, name)

			removed++
		}
	}

	return removed
}

// Len returns the number of stored records (including expired ones
// that have not yet been cleaned).
func (s *ACMEChallengeStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return len(s.records)
}
