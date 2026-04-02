package hscontrol

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/juanfont/headscale/hscontrol/types"
	"github.com/juanfont/headscale/hscontrol/types/change"
	"github.com/rs/zerolog/log"
	"tailscale.com/tailcfg"
)

const acmeDNSProviderTimeout = 30 * time.Second

// QueryFeatureHandler handles /machine/feature/query requests from the
// Tailscale client. The client sends a [tailcfg.QueryFeatureRequest] to
// check if a feature (e.g. "serve", "funnel") is enabled on the control
// server, and receives a [tailcfg.QueryFeatureResponse] indicating whether
// the feature is available.
func (ns *noiseServer) QueryFeatureHandler(
	writer http.ResponseWriter,
	req *http.Request,
) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		httpError(writer, NewHTTPError(
			http.StatusBadRequest,
			"Failed to read request body",
			err,
		))

		return
	}

	var featureReq tailcfg.QueryFeatureRequest

	err = json.Unmarshal(body, &featureReq)
	if err != nil {
		httpError(writer, NewHTTPError(
			http.StatusBadRequest,
			"Failed to parse feature query request",
			err,
		))

		return
	}

	log.Trace().
		Str("feature", featureReq.Feature).
		Str("machine_key", ns.machineKey.ShortString()).
		Msg("feature query request")

	resp := queryFeature(featureReq, ns.headscale.cfg)

	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(http.StatusOK)

	err = json.NewEncoder(writer).Encode(resp)
	if err != nil {
		log.Error().
			Caller().
			Err(err).
			Msg("failed to encode QueryFeatureResponse")
	}
}

// queryFeature evaluates whether a requested feature is enabled on this
// Headscale instance and returns an appropriate response.
func queryFeature(
	req tailcfg.QueryFeatureRequest,
	cfg *types.Config,
) *tailcfg.QueryFeatureResponse {
	switch req.Feature {
	case "serve":
		return queryServeFeature(cfg)
	case "funnel":
		return queryFunnelFeature(cfg)
	default:
		return &tailcfg.QueryFeatureResponse{
			Complete: false,
			Text: fmt.Sprintf(
				"Feature %q is not supported by this Headscale server.",
				req.Feature,
			),
		}
	}
}

// queryServeFeature returns the feature query response for "serve".
func queryServeFeature(cfg *types.Config) *tailcfg.QueryFeatureResponse {
	if cfg.Serve.Enabled {
		return &tailcfg.QueryFeatureResponse{
			Complete: true,
		}
	}

	return &tailcfg.QueryFeatureResponse{
		Complete: false,
		Text: "Tailscale Serve is not enabled on this Headscale server.\n" +
			"Ask your administrator to set 'serve.enabled: true' in the configuration.",
	}
}

// queryFunnelFeature returns the feature query response for "funnel".
func queryFunnelFeature(cfg *types.Config) *tailcfg.QueryFeatureResponse {
	if !cfg.Serve.Enabled {
		return &tailcfg.QueryFeatureResponse{
			Complete: false,
			Text: "Tailscale Serve and Funnel are not enabled on this Headscale server.\n" +
				"Ask your administrator to enable both 'serve.enabled' and 'funnel.enabled' in the configuration.",
		}
	}

	if !cfg.Funnel.Enabled {
		return &tailcfg.QueryFeatureResponse{
			Complete: false,
			Text: "Tailscale Funnel is not enabled on this Headscale server.\n" +
				"Ask your administrator to set 'funnel.enabled: true' in the configuration.",
		}
	}

	return &tailcfg.QueryFeatureResponse{
		Complete: true,
	}
}

// SetDNSHandler handles POST /machine/set-dns requests from the
// Tailscale client. The client sends a [tailcfg.SetDNSRequest] to
// create DNS records for ACME DNS-01 challenges when provisioning
// TLS certificates for tailscale serve HTTPS.
//
// The server stores the TXT record in an in-memory ACME challenge
// store and injects it into the DNSConfig.ExtraRecords sent to all
// nodes, then broadcasts a DNS change notification.
func (ns *noiseServer) SetDNSHandler(
	writer http.ResponseWriter,
	req *http.Request,
) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		httpError(writer, NewHTTPError(
			http.StatusBadRequest,
			"Failed to read request body",
			err,
		))

		return
	}

	var dnsReq tailcfg.SetDNSRequest

	err = json.Unmarshal(body, &dnsReq)
	if err != nil {
		httpError(writer, NewHTTPError(
			http.StatusBadRequest,
			"Failed to parse set-dns request",
			err,
		))

		return
	}

	log.Info().
		Str("name", dnsReq.Name).
		Str("type", dnsReq.Type).
		Str("machine_key", ns.machineKey.ShortString()).
		Msg("set-dns request")

	// Validate the request: only ACME challenge TXT records are supported.
	if dnsReq.Type != "TXT" {
		httpError(writer, NewHTTPError(
			http.StatusBadRequest,
			fmt.Sprintf("Unsupported DNS record type: %q, only TXT is supported", dnsReq.Type),
			nil,
		))

		return
	}

	if !strings.HasPrefix(dnsReq.Name, "_acme-challenge.") {
		httpError(writer, NewHTTPError(
			http.StatusBadRequest,
			"Only _acme-challenge. prefixed DNS records are supported",
			nil,
		))

		return
	}

	// Store the ACME challenge record for tracking and ExtraRecords.
	ns.headscale.acmeChallenges.SetRecord(dnsReq.Name, dnsReq.Value)

	// Create the TXT record in public DNS via the configured provider.
	if ns.headscale.acmeDNSProvider != nil {
		provCtx, provCancel := context.WithTimeout(
			req.Context(),
			acmeDNSProviderTimeout,
		)
		defer provCancel()

		err := ns.headscale.acmeDNSProvider.CreateTXTRecord(
			provCtx, dnsReq.Name, dnsReq.Value,
		)
		if err != nil {
			log.Error().
				Err(err).
				Str("domain", dnsReq.Name).
				Msg("failed to create ACME TXT record in public DNS")

			httpError(writer, NewHTTPError(
				http.StatusInternalServerError,
				"Failed to create DNS record for ACME challenge",
				err,
			))

			return
		}
	} else {
		log.Warn().
			Str("domain", dnsReq.Name).
			Msg("no ACME DNS provider configured; TXT record stored locally but may not be resolvable by ACME CA")
	}

	// Recompose extra records and notify all connected clients.
	ns.headscale.recomposeExtraRecords()
	ns.headscale.Change(change.ExtraRecords())

	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(http.StatusOK)

	err = json.NewEncoder(writer).Encode(tailcfg.SetDNSResponse{})
	if err != nil {
		log.Error().
			Caller().
			Err(err).
			Msg("failed to encode SetDNSResponse")
	}
}

// recomposeExtraRecords rebuilds TailcfgDNSConfig.ExtraRecords from
// all sources (file-based extra records + ACME challenge records)
// under a write lock. This ensures that the file watcher and ACME
// handler never race on ExtraRecords, and neither can clobber the other.
func (h *Headscale) recomposeExtraRecords() {
	if h.cfg.TailcfgDNSConfig == nil {
		return
	}

	h.cfg.ExtraRecordsMu.Lock()
	defer h.cfg.ExtraRecordsMu.Unlock()

	// Start from file-based extra records (the authoritative base).
	var base []tailcfg.DNSRecord
	if h.extraRecordMan != nil {
		base = h.extraRecordMan.Records()
	} else {
		// No file watcher; use the static records from config.
		// Filter out any ACME records that may have been appended
		// in a previous recompose cycle.
		for _, r := range h.cfg.DNSConfig.ExtraRecords {
			if r.Type == "TXT" && strings.HasPrefix(r.Name, "_acme-challenge.") {
				continue
			}

			base = append(base, r)
		}
	}

	// Append ACME challenge records.
	acmeRecords := h.acmeChallenges.Records()

	combined := make([]tailcfg.DNSRecord, 0, len(base)+len(acmeRecords))
	combined = append(combined, base...)
	combined = append(combined, acmeRecords...)

	h.cfg.TailcfgDNSConfig.ExtraRecords = combined
}
