package dns

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCommandDNSProvider_CreateTXTRecord(t *testing.T) {
	provider := NewCommandDNSProvider(
		"echo create {domain} {token}",
		"echo remove {domain} {token}",
		5*time.Second,
	)

	err := provider.CreateTXTRecord(
		context.Background(),
		"_acme-challenge.node.example.com",
		"test-token-123",
	)
	require.NoError(t, err)
}

func TestCommandDNSProvider_RemoveTXTRecord(t *testing.T) {
	provider := NewCommandDNSProvider(
		"echo create",
		"echo remove {domain} {token}",
		5*time.Second,
	)

	err := provider.RemoveTXTRecord(
		context.Background(),
		"_acme-challenge.node.example.com",
		"test-token-123",
	)
	require.NoError(t, err)
}

func TestCommandDNSProvider_CreateCmdEmpty(t *testing.T) {
	provider := NewCommandDNSProvider("", "", 5*time.Second)

	err := provider.CreateTXTRecord(
		context.Background(),
		"_acme-challenge.node.example.com",
		"token",
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create command is not configured")
}

func TestCommandDNSProvider_RemoveCmdEmpty(t *testing.T) {
	provider := NewCommandDNSProvider("echo create", "", 5*time.Second)

	// Empty remove command should not error — it's optional.
	err := provider.RemoveTXTRecord(
		context.Background(),
		"_acme-challenge.node.example.com",
		"token",
	)
	require.NoError(t, err)
}

func TestCommandDNSProvider_CommandFailure(t *testing.T) {
	provider := NewCommandDNSProvider(
		"false",
		"",
		5*time.Second,
	)

	err := provider.CreateTXTRecord(
		context.Background(),
		"_acme-challenge.node.example.com",
		"token",
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "command failed")
}

func TestCommandDNSProvider_DefaultTimeout(t *testing.T) {
	provider := NewCommandDNSProvider("echo ok", "", 0)
	assert.Equal(t, defaultProviderTimeout, provider.Timeout)
}

func TestWebhookDNSProvider_CreateTXTRecord(t *testing.T) {
	var receivedPayload webhookPayload

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		assert.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))

		err := json.NewDecoder(r.Body).Decode(&receivedPayload)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)

			return
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	provider := NewWebhookDNSProvider(
		server.URL+"/create",
		server.URL+"/remove",
		map[string]string{"Authorization": "Bearer test-key"},
		5*time.Second,
	)

	err := provider.CreateTXTRecord(
		context.Background(),
		"_acme-challenge.node.example.com",
		"test-token-456",
	)
	require.NoError(t, err)
	assert.Equal(t, "_acme-challenge.node.example.com", receivedPayload.Domain)
	assert.Equal(t, "test-token-456", receivedPayload.Token)
}

func TestWebhookDNSProvider_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer server.Close()

	provider := NewWebhookDNSProvider(
		server.URL+"/create",
		"",
		nil,
		5*time.Second,
	)

	err := provider.CreateTXTRecord(
		context.Background(),
		"_acme-challenge.node.example.com",
		"token",
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "status 500")
}

func TestWebhookDNSProvider_CreateURLEmpty(t *testing.T) {
	provider := NewWebhookDNSProvider("", "", nil, 5*time.Second)

	err := provider.CreateTXTRecord(
		context.Background(),
		"_acme-challenge.node.example.com",
		"token",
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create URL is not configured")
}

func TestWebhookDNSProvider_RemoveURLEmpty(t *testing.T) {
	provider := NewWebhookDNSProvider("http://example.com/create", "", nil, 5*time.Second)

	// Empty remove URL should not error — it's optional.
	err := provider.RemoveTXTRecord(
		context.Background(),
		"_acme-challenge.node.example.com",
		"token",
	)
	assert.NoError(t, err)
}

func TestWebhookDNSProvider_DefaultTimeout(t *testing.T) {
	provider := NewWebhookDNSProvider("http://example.com", "", nil, 0)
	assert.Equal(t, defaultProviderTimeout, provider.Timeout)
}
