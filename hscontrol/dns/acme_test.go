package dns

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestACMEChallengeStore_SetAndGet(t *testing.T) {
	store := NewACMEChallengeStore()

	assert.Equal(t, 0, store.Len())
	assert.Nil(t, store.Records())

	store.SetRecord("_acme-challenge.node1.example.com", "challenge-token-1")

	assert.Equal(t, 1, store.Len())

	records := store.Records()
	require.Len(t, records, 1)
	assert.Equal(t, "_acme-challenge.node1.example.com", records[0].Name)
	assert.Equal(t, "TXT", records[0].Type)
	assert.Equal(t, "challenge-token-1", records[0].Value)
}

func TestACMEChallengeStore_Overwrite(t *testing.T) {
	store := NewACMEChallengeStore()

	store.SetRecord("_acme-challenge.node1.example.com", "token-v1")
	store.SetRecord("_acme-challenge.node1.example.com", "token-v2")

	assert.Equal(t, 1, store.Len())

	records := store.Records()
	require.Len(t, records, 1)
	assert.Equal(t, "token-v2", records[0].Value)
}

func TestACMEChallengeStore_MultipleRecords(t *testing.T) {
	store := NewACMEChallengeStore()

	store.SetRecord("_acme-challenge.node1.example.com", "token-1")
	store.SetRecord("_acme-challenge.node2.example.com", "token-2")

	assert.Equal(t, 2, store.Len())

	records := store.Records()
	require.Len(t, records, 2)
}

func TestACMEChallengeStore_Remove(t *testing.T) {
	store := NewACMEChallengeStore()

	store.SetRecord("_acme-challenge.node1.example.com", "token-1")
	store.SetRecord("_acme-challenge.node2.example.com", "token-2")

	assert.Equal(t, 2, store.Len())

	store.RemoveRecord("_acme-challenge.node1.example.com")

	assert.Equal(t, 1, store.Len())

	records := store.Records()
	require.Len(t, records, 1)
	assert.Equal(t, "_acme-challenge.node2.example.com", records[0].Name)
}

func TestACMEChallengeStore_RemoveNonExistent(t *testing.T) {
	store := NewACMEChallengeStore()

	// Removing a non-existent record should not panic.
	store.RemoveRecord("_acme-challenge.nonexistent.example.com")

	assert.Equal(t, 0, store.Len())
}

func TestACMEChallengeStore_TrailingDotNormalization(t *testing.T) {
	store := NewACMEChallengeStore()

	// Set with trailing dot.
	store.SetRecord("_acme-challenge.node1.example.com.", "token-1")

	assert.Equal(t, 1, store.Len())

	records := store.Records()
	require.Len(t, records, 1)

	// The stored name should NOT have a trailing dot.
	assert.Equal(t, "_acme-challenge.node1.example.com", records[0].Name)

	// Remove with trailing dot should match.
	store.RemoveRecord("_acme-challenge.node1.example.com.")

	assert.Equal(t, 0, store.Len())
}

func TestACMEChallengeStore_TTLExpiry(t *testing.T) {
	now := time.Now()

	store := &ACMEChallengeStore{
		records: make(map[string]acmeRecord),
		ttl:     5 * time.Minute,
		nowFunc: func() time.Time { return now },
	}

	store.SetRecord("_acme-challenge.node1.example.com", "token-1")

	// Record should be visible immediately.
	records := store.Records()
	require.Len(t, records, 1)

	// Advance time past TTL.
	now = now.Add(6 * time.Minute)

	// Record should be excluded from results.
	records = store.Records()
	assert.Empty(t, records, "expired records should not appear in Records()")

	// Len still counts it (not cleaned yet).
	assert.Equal(t, 1, store.Len())
}

func TestACMEChallengeStore_CleanExpired(t *testing.T) {
	now := time.Now()

	store := &ACMEChallengeStore{
		records: make(map[string]acmeRecord),
		ttl:     5 * time.Minute,
		nowFunc: func() time.Time { return now },
	}

	store.SetRecord("_acme-challenge.node1.example.com", "token-1")
	store.SetRecord("_acme-challenge.node2.example.com", "token-2")

	assert.Equal(t, 2, store.Len())

	// No records expired yet.
	assert.Equal(t, 0, store.CleanExpired())
	assert.Equal(t, 2, store.Len())

	// Advance time past TTL.
	now = now.Add(6 * time.Minute)

	// Add a fresh record after the time advance.
	store.SetRecord("_acme-challenge.node3.example.com", "token-3")

	// Clean should remove the 2 expired records but keep the fresh one.
	removed := store.CleanExpired()
	assert.Equal(t, 2, removed)
	assert.Equal(t, 1, store.Len())

	records := store.Records()
	require.Len(t, records, 1)
	assert.Equal(t, "_acme-challenge.node3.example.com", records[0].Name)
}

func TestACMEChallengeStore_SetResetsTimer(t *testing.T) {
	now := time.Now()

	store := &ACMEChallengeStore{
		records: make(map[string]acmeRecord),
		ttl:     5 * time.Minute,
		nowFunc: func() time.Time { return now },
	}

	store.SetRecord("_acme-challenge.node1.example.com", "token-v1")

	// Advance 3 minutes (within TTL).
	now = now.Add(3 * time.Minute)

	// Overwrite resets the timer.
	store.SetRecord("_acme-challenge.node1.example.com", "token-v2")

	// Advance another 3 minutes — original would have expired (6 min total),
	// but reset keeps it alive (only 3 min since last Set).
	now = now.Add(3 * time.Minute)

	records := store.Records()
	require.Len(t, records, 1)
	assert.Equal(t, "token-v2", records[0].Value)
}
