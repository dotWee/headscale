package hscontrol

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/juanfont/headscale/hscontrol/types"
	"github.com/juanfont/headscale/hscontrol/types/change"
	"github.com/rs/zerolog/log"
	"tailscale.com/tailcfg"
)

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

	// Store the ACME challenge record.
	ns.headscale.acmeChallenges.SetRecord(dnsReq.Name, dnsReq.Value)

	// Merge ACME challenge records into the DNS config and notify all
	// connected clients so the records propagate via MagicDNS.
	mergeACMERecords(ns.headscale)

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

// mergeACMERecords appends any ACME challenge records into the
// TailcfgDNSConfig.ExtraRecords slice. Existing non-ACME extra
// records are preserved; stale ACME entries are replaced.
func mergeACMERecords(h *Headscale) {
	if h.cfg.TailcfgDNSConfig == nil {
		return
	}

	acmeRecords := h.acmeChallenges.Records()

	// Filter out old ACME records from ExtraRecords, keep everything else.
	existing := h.cfg.TailcfgDNSConfig.ExtraRecords
	cleaned := make([]tailcfg.DNSRecord, 0, len(existing))

	for _, r := range existing {
		if r.Type == "TXT" && strings.HasPrefix(r.Name, "_acme-challenge.") {
			continue // drop old ACME entries; fresh ones will be appended
		}

		cleaned = append(cleaned, r)
	}

	h.cfg.TailcfgDNSConfig.ExtraRecords = append(cleaned, acmeRecords...)
}
