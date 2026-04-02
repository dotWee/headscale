package dns

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

const defaultProviderTimeout = 30 * time.Second

var (
	errCommandNotConfigured    = errors.New("ACME DNS command provider: create command is not configured")
	errCommandEmpty            = errors.New("ACME DNS command is empty after expansion")
	errWebhookURLNotConfigured = errors.New("ACME DNS webhook provider: create URL is not configured")
	errWebhookBadStatus        = errors.New("ACME DNS webhook returned non-success status")
)

// ACMEDNSProvider creates and removes ACME DNS-01 challenge TXT
// records in public DNS so they are verifiable by certificate
// authorities like Let's Encrypt.
type ACMEDNSProvider interface {
	// CreateTXTRecord creates an _acme-challenge TXT record.
	CreateTXTRecord(ctx context.Context, domain, value string) error

	// RemoveTXTRecord removes an _acme-challenge TXT record.
	RemoveTXTRecord(ctx context.Context, domain, value string) error
}

// CommandDNSProvider implements ACMEDNSProvider by executing a
// user-configured command. The command template supports {domain}
// and {token} placeholders.
type CommandDNSProvider struct {
	CreateCmd string
	RemoveCmd string
	Timeout   time.Duration
}

// NewCommandDNSProvider creates a command-based ACME DNS provider.
func NewCommandDNSProvider(createCmd, removeCmd string, timeout time.Duration) *CommandDNSProvider {
	if timeout == 0 {
		timeout = defaultProviderTimeout
	}

	return &CommandDNSProvider{
		CreateCmd: createCmd,
		RemoveCmd: removeCmd,
		Timeout:   timeout,
	}
}

func (p *CommandDNSProvider) CreateTXTRecord(ctx context.Context, domain, value string) error {
	if p.CreateCmd == "" {
		return errCommandNotConfigured
	}

	return p.runCommand(ctx, p.CreateCmd, domain, value)
}

func (p *CommandDNSProvider) RemoveTXTRecord(ctx context.Context, domain, value string) error {
	if p.RemoveCmd == "" {
		// Removal is optional — not all providers support it.
		log.Debug().
			Str("domain", domain).
			Msg("ACME DNS command provider: no remove command configured, skipping")

		return nil
	}

	return p.runCommand(ctx, p.RemoveCmd, domain, value)
}

func (p *CommandDNSProvider) runCommand(
	ctx context.Context,
	cmdTemplate, domain, value string,
) error {
	expanded := strings.NewReplacer(
		"{domain}", domain,
		"{token}", value,
	).Replace(cmdTemplate)

	parts := strings.Fields(expanded)
	if len(parts) == 0 {
		return fmt.Errorf("%w: %q", errCommandEmpty, cmdTemplate)
	}

	ctx, cancel := context.WithTimeout(ctx, p.Timeout)
	defer cancel()

	//nolint:gosec // Command is operator-configured, not user-supplied
	cmd := exec.CommandContext(ctx, parts[0], parts[1:]...)

	cmd.Env = append(cmd.Environ(),
		"ACME_DOMAIN="+domain,
		"ACME_TOKEN="+value,
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf(
			"ACME DNS command failed: %w (output: %s)",
			err, strings.TrimSpace(string(output)),
		)
	}

	log.Info().
		Str("domain", domain).
		Str("command", parts[0]).
		Msg("ACME DNS command executed successfully")

	return nil
}

// WebhookDNSProvider implements ACMEDNSProvider by calling HTTP
// endpoints to create and remove TXT records.
type WebhookDNSProvider struct {
	CreateURL string
	RemoveURL string
	Headers   map[string]string
	Timeout   time.Duration
	Client    *http.Client
}

// NewWebhookDNSProvider creates a webhook-based ACME DNS provider.
func NewWebhookDNSProvider(
	createURL, removeURL string,
	headers map[string]string,
	timeout time.Duration,
) *WebhookDNSProvider {
	if timeout == 0 {
		timeout = defaultProviderTimeout
	}

	return &WebhookDNSProvider{
		CreateURL: createURL,
		RemoveURL: removeURL,
		Headers:   headers,
		Timeout:   timeout,
		Client:    &http.Client{Timeout: timeout},
	}
}

type webhookPayload struct {
	Domain string `json:"domain"`
	Token  string `json:"token"`
}

func (p *WebhookDNSProvider) CreateTXTRecord(ctx context.Context, domain, value string) error {
	if p.CreateURL == "" {
		return errWebhookURLNotConfigured
	}

	return p.callWebhook(ctx, http.MethodPost, p.CreateURL, domain, value)
}

func (p *WebhookDNSProvider) RemoveTXTRecord(ctx context.Context, domain, value string) error {
	if p.RemoveURL == "" {
		log.Debug().
			Str("domain", domain).
			Msg("ACME DNS webhook provider: no remove URL configured, skipping")

		return nil
	}

	return p.callWebhook(ctx, http.MethodPost, p.RemoveURL, domain, value)
}

func (p *WebhookDNSProvider) callWebhook(
	ctx context.Context,
	method, url, domain, value string,
) error {
	payload := webhookPayload{
		Domain: domain,
		Token:  value,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshalling webhook payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating webhook request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	for k, v := range p.Headers {
		req.Header.Set(k, v)
	}

	resp, err := p.Client.Do(req)
	if err != nil {
		return fmt.Errorf("ACME DNS webhook request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))

		return fmt.Errorf(
			"%w: status %d: %s",
			errWebhookBadStatus,
			resp.StatusCode, strings.TrimSpace(string(respBody)),
		)
	}

	log.Info().
		Str("domain", domain).
		Str("url", url).
		Int("status", resp.StatusCode).
		Msg("ACME DNS webhook called successfully")

	return nil
}
