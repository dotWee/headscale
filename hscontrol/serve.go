package hscontrol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/juanfont/headscale/hscontrol/types"
	"github.com/miekg/dns"
	"github.com/rs/zerolog/log"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
)

const (
	serveFeatureName  = "serve"
	funnelFeatureName = "funnel"
)

var (
	errServeDNSDisabled     = errors.New("tailscale serve HTTPS is not enabled")
	errServeDNSProvider     = errors.New("unsupported serve DNS provider")
	errInvalidServeDNSName  = errors.New("dns challenge name is not allowed for this node")
	errInvalidServeDNSType  = errors.New("only TXT dns records are supported")
	errInvalidServeDNSNode  = errors.New("dns request node key does not match noise session")
	errInvalidServeDNSValue = errors.New("dns challenge value must not be empty")
	errUnknownServeFeature  = errors.New("unknown serve feature")
	errServeNodeUnavailable = errors.New("node not found for serve request")
)

type serveDNSManager interface {
	SetDNS(ctx context.Context, name, value string) error
}

type rfc2136DNSManager struct {
	nameserver    string
	zone          string
	ttl           uint32
	network       string
	tsigKeyName   string
	tsigSecret    string
	tsigAlgorithm string
	timeout       timeouts
}

type webhookDNSManager struct {
	url         string
	bearerToken string
	timeout     timeouts
}

type timeouts struct {
	request time.Duration
}

func newServeDNSManager(cfg *types.Config) (serveDNSManager, error) {
	if !cfg.Serve.HTTPS.Enabled {
		return nil, nil
	}

	switch cfg.Serve.HTTPS.DNS.Provider {
	case "rfc2136":
		return &rfc2136DNSManager{
			nameserver:    normalizeRFC2136Nameserver(cfg.Serve.HTTPS.DNS.RFC2136.Nameserver),
			zone:          dns.Fqdn(cfg.Serve.HTTPS.DNS.RFC2136.Zone),
			ttl:           cfg.Serve.HTTPS.DNS.TTL,
			network:       cmpOr(cfg.Serve.HTTPS.DNS.RFC2136.Network, "udp"),
			tsigKeyName:   dns.Fqdn(cfg.Serve.HTTPS.DNS.RFC2136.TSIGKeyName),
			tsigSecret:    cfg.Serve.HTTPS.DNS.RFC2136.TSIGSecret,
			tsigAlgorithm: cmpOr(cfg.Serve.HTTPS.DNS.RFC2136.TSIGAlgorithm, dns.HmacSHA256),
			timeout: timeouts{
				request: cfg.Serve.HTTPS.DNS.Timeout,
			},
		}, nil
	case "webhook":
		return &webhookDNSManager{
			url:         cfg.Serve.HTTPS.DNS.Webhook.URL,
			bearerToken: cfg.Serve.HTTPS.DNS.Webhook.BearerToken,
			timeout: timeouts{
				request: cfg.Serve.HTTPS.DNS.Timeout,
			},
		}, nil
	case "":
		return nil, fmt.Errorf("%w: empty provider", errServeDNSProvider)
	default:
		return nil, fmt.Errorf("%w: %s", errServeDNSProvider, cfg.Serve.HTTPS.DNS.Provider)
	}
}

func (m *rfc2136DNSManager) SetDNS(ctx context.Context, name, value string) error {
	fqdn := dns.Fqdn(name)
	msg := new(dns.Msg)
	msg.SetUpdate(m.zone)

	msg.RemoveRRset([]dns.RR{
		&dns.TXT{
			Hdr: dns.RR_Header{
				Name:   fqdn,
				Rrtype: dns.TypeTXT,
				Class:  dns.ClassINET,
				Ttl:    m.ttl,
			},
		},
	})
	msg.Insert([]dns.RR{
		&dns.TXT{
			Hdr: dns.RR_Header{
				Name:   fqdn,
				Rrtype: dns.TypeTXT,
				Class:  dns.ClassINET,
				Ttl:    m.ttl,
			},
			Txt: []string{value},
		},
	})

	client := &dns.Client{
		Net:     m.network,
		Timeout: m.timeout.request,
	}

	if m.tsigKeyName != "." && m.tsigKeyName != "" {
		msg.SetTsig(m.tsigKeyName, m.tsigAlgorithm, 300, time.Now().Unix())
		client.TsigSecret = map[string]string{m.tsigKeyName: m.tsigSecret}
	}

	log.Debug().
		Str("provider", "rfc2136").
		Str("zone", m.zone).
		Str("nameserver", m.nameserver).
		Str("name", fqdn).
		Uint32("ttl", m.ttl).
		Msg("updating serve ACME DNS challenge")

	resp, _, err := client.ExchangeContext(ctx, msg, m.nameserver)
	if err != nil {
		return fmt.Errorf("sending RFC2136 update for %q via %q: %w", fqdn, m.nameserver, err)
	}
	if resp == nil {
		return fmt.Errorf("sending RFC2136 update for %q via %q: empty response", fqdn, m.nameserver)
	}
	if resp.Rcode != dns.RcodeSuccess {
		return fmt.Errorf("sending RFC2136 update for %q via %q: %s", fqdn, m.nameserver, dns.RcodeToString[resp.Rcode])
	}

	return nil
}

func (m *webhookDNSManager) SetDNS(ctx context.Context, name, value string) error {
	payload, err := json.Marshal(map[string]string{
		"type":  "TXT",
		"name":  name,
		"value": value,
	})
	if err != nil {
		return fmt.Errorf("marshalling webhook DNS payload: %w", err)
	}

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		m.url,
		bytes.NewReader(payload),
	)
	if err != nil {
		return fmt.Errorf("creating webhook DNS request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if m.bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+m.bearerToken)
	}

	client := &http.Client{Timeout: m.timeout.request}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("sending webhook DNS update for %q via %q: %w", name, m.url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf(
			"sending webhook DNS update for %q via %q: status %d",
			name,
			m.url,
			resp.StatusCode,
		)
	}

	return nil
}

func serveFeatureResponse(
	cfg *types.Config,
	feature string,
	funnelAllowed bool,
) (*tailcfg.QueryFeatureResponse, error) {
	switch feature {
	case serveFeatureName:
		if cfg.Serve.HTTPS.Enabled {
			return &tailcfg.QueryFeatureResponse{Complete: true}, nil
		}

		return &tailcfg.QueryFeatureResponse{
			Text: "Tailscale Serve HTTPS is disabled on this Headscale server. An administrator must enable serve.https and configure DNS challenge support.",
		}, nil
	case funnelFeatureName:
		if cfg.Serve.Funnel.Enabled && funnelAllowed {
			return &tailcfg.QueryFeatureResponse{Complete: true}, nil
		}
		if cfg.Serve.Funnel.Enabled && !funnelAllowed {
			return &tailcfg.QueryFeatureResponse{
				Text: "Tailscale Funnel is disabled for this node by policy. An administrator must allow funnel usage for this node (for example via nodeAttrs).",
			}, nil
		}

		return &tailcfg.QueryFeatureResponse{
			Text: "Tailscale Funnel is disabled on this Headscale server. An administrator must enable serve.funnel and allow the requested ports.",
		}, nil
	default:
		return nil, fmt.Errorf("%w: %s", errUnknownServeFeature, feature)
	}
}

func serveCertDomain(cfg *types.Config, node types.NodeView) (string, error) {
	if !cfg.Serve.HTTPS.Enabled {
		return "", errServeDNSDisabled
	}

	domain := cfg.Serve.Domain
	if domain == "" {
		domain = cfg.BaseDomain
	}
	if domain == "" {
		return "", errServeDNSDisabled
	}

	fqdn, err := node.GetFQDN(domain)
	if err != nil {
		return "", err
	}

	return strings.TrimSuffix(strings.ToLower(fqdn), "."), nil
}

func validateServeDNSRequest(
	cfg *types.Config,
	node types.NodeView,
	sessionNodeKey key.NodePublic,
	req tailcfg.SetDNSRequest,
) error {
	if !cfg.Serve.HTTPS.Enabled {
		return errServeDNSDisabled
	}
	if req.NodeKey != sessionNodeKey {
		return errInvalidServeDNSNode
	}
	if !strings.EqualFold(req.Type, "TXT") {
		return errInvalidServeDNSType
	}
	if strings.TrimSpace(req.Value) == "" {
		return errInvalidServeDNSValue
	}

	certDomain, err := serveCertDomain(cfg, node)
	if err != nil {
		return err
	}

	name := normalizeDNSName(req.Name)
	expected := normalizeDNSName("_acme-challenge." + certDomain)
	if name != expected {
		return fmt.Errorf("%w: got %q, want %q", errInvalidServeDNSName, name, expected)
	}

	return nil
}

func normalizeDNSName(name string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
}

func normalizeRFC2136Nameserver(nameserver string) string {
	if nameserver == "" {
		return ""
	}
	if _, _, err := net.SplitHostPort(nameserver); err == nil {
		return nameserver
	}

	return net.JoinHostPort(nameserver, "53")
}

func cmpOr[T comparable](value, fallback T) T {
	var zero T
	if value == zero {
		return fallback
	}

	return value
}

