package hscontrol

import (
	"strings"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/juanfont/headscale/hscontrol/dns"
	"github.com/juanfont/headscale/hscontrol/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"tailscale.com/tailcfg"
)

func TestQueryFeature(t *testing.T) {
	tests := []struct {
		name string
		req  tailcfg.QueryFeatureRequest
		cfg  *types.Config
		want *tailcfg.QueryFeatureResponse
	}{
		{
			name: "serve-enabled",
			req:  tailcfg.QueryFeatureRequest{Feature: "serve"},
			cfg: &types.Config{
				Serve: types.ServeConfig{Enabled: true},
			},
			want: &tailcfg.QueryFeatureResponse{
				Complete: true,
			},
		},
		{
			name: "serve-disabled",
			req:  tailcfg.QueryFeatureRequest{Feature: "serve"},
			cfg: &types.Config{
				Serve: types.ServeConfig{Enabled: false},
			},
			want: &tailcfg.QueryFeatureResponse{
				Complete: false,
				Text: "Tailscale Serve is not enabled on this Headscale server.\n" +
					"Ask your administrator to set 'serve.enabled: true' in the configuration.",
			},
		},
		{
			name: "funnel-enabled",
			req:  tailcfg.QueryFeatureRequest{Feature: "funnel"},
			cfg: &types.Config{
				Serve:  types.ServeConfig{Enabled: true},
				Funnel: types.FunnelConfig{Enabled: true},
			},
			want: &tailcfg.QueryFeatureResponse{
				Complete: true,
			},
		},
		{
			name: "funnel-disabled-serve-enabled",
			req:  tailcfg.QueryFeatureRequest{Feature: "funnel"},
			cfg: &types.Config{
				Serve:  types.ServeConfig{Enabled: true},
				Funnel: types.FunnelConfig{Enabled: false},
			},
			want: &tailcfg.QueryFeatureResponse{
				Complete: false,
				Text: "Tailscale Funnel is not enabled on this Headscale server.\n" +
					"Ask your administrator to set 'funnel.enabled: true' in the configuration.",
			},
		},
		{
			name: "funnel-disabled-serve-disabled",
			req:  tailcfg.QueryFeatureRequest{Feature: "funnel"},
			cfg: &types.Config{
				Serve:  types.ServeConfig{Enabled: false},
				Funnel: types.FunnelConfig{Enabled: false},
			},
			want: &tailcfg.QueryFeatureResponse{
				Complete: false,
				Text: "Tailscale Serve and Funnel are not enabled on this Headscale server.\n" +
					"Ask your administrator to enable both 'serve.enabled' and 'funnel.enabled' in the configuration.",
			},
		},
		{
			name: "funnel-enabled-serve-disabled",
			req:  tailcfg.QueryFeatureRequest{Feature: "funnel"},
			cfg: &types.Config{
				Serve:  types.ServeConfig{Enabled: false},
				Funnel: types.FunnelConfig{Enabled: true},
			},
			want: &tailcfg.QueryFeatureResponse{
				Complete: false,
				Text: "Tailscale Serve and Funnel are not enabled on this Headscale server.\n" +
					"Ask your administrator to enable both 'serve.enabled' and 'funnel.enabled' in the configuration.",
			},
		},
		{
			name: "unknown-feature",
			req:  tailcfg.QueryFeatureRequest{Feature: "unknown-feature"},
			cfg:  &types.Config{},
			want: &tailcfg.QueryFeatureResponse{
				Complete: false,
				Text:     `Feature "unknown-feature" is not supported by this Headscale server.`,
			},
		},
		{
			name: "empty-feature",
			req:  tailcfg.QueryFeatureRequest{Feature: ""},
			cfg:  &types.Config{},
			want: &tailcfg.QueryFeatureResponse{
				Complete: false,
				Text:     `Feature "" is not supported by this Headscale server.`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := queryFeature(tt.req, tt.cfg)

			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("queryFeature() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestRecomposeExtraRecords(t *testing.T) {
	tests := []struct {
		name           string
		staticRecords  []tailcfg.DNSRecord
		acmeRecords    map[string]string
		wantLen        int
		wantACMECount  int
		wantOtherCount int
	}{
		{
			name:           "no-records",
			staticRecords:  nil,
			acmeRecords:    nil,
			wantLen:        0,
			wantACMECount:  0,
			wantOtherCount: 0,
		},
		{
			name: "acme-only",
			acmeRecords: map[string]string{
				"_acme-challenge.node1.example.com": "token-1",
			},
			wantLen:        1,
			wantACMECount:  1,
			wantOtherCount: 0,
		},
		{
			name: "static-plus-acme",
			staticRecords: []tailcfg.DNSRecord{
				{Name: "myapp.example.com", Value: "100.64.0.1"},
			},
			acmeRecords: map[string]string{
				"_acme-challenge.node1.example.com": "token-1",
			},
			wantLen:        2,
			wantACMECount:  1,
			wantOtherCount: 1,
		},
		{
			name: "stale-acme-in-static-are-filtered",
			staticRecords: []tailcfg.DNSRecord{
				{Name: "myapp.example.com", Value: "100.64.0.1"},
				{Name: "_acme-challenge.old.example.com", Type: "TXT", Value: "old-token"},
			},
			acmeRecords: map[string]string{
				"_acme-challenge.new.example.com": "new-token",
			},
			wantLen:        2,
			wantACMECount:  1,
			wantOtherCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mu := &sync.RWMutex{}
			store := dns.NewACMEChallengeStore()

			for name, value := range tt.acmeRecords {
				store.SetRecord(name, value)
			}

			h := &Headscale{
				cfg: &types.Config{
					DNSConfig: types.DNSConfig{
						ExtraRecords: tt.staticRecords,
					},
					TailcfgDNSConfig: &tailcfg.DNSConfig{},
					ExtraRecordsMu:   mu,
				},
				acmeChallenges: store,
			}

			h.recomposeExtraRecords()

			records := h.cfg.TailcfgDNSConfig.ExtraRecords
			require.Len(t, records, tt.wantLen)

			const acmeRecordType = "TXT"

			var acmeCount, otherCount int

			for _, r := range records {
				if r.Type == acmeRecordType && strings.HasPrefix(r.Name, "_acme-challenge.") {
					acmeCount++
				} else {
					otherCount++
				}
			}

			assert.Equal(t, tt.wantACMECount, acmeCount, "ACME record count")
			assert.Equal(t, tt.wantOtherCount, otherCount, "non-ACME record count")
		})
	}
}

func TestRecomposeExtraRecords_NilDNSConfig(t *testing.T) {
	h := &Headscale{
		cfg: &types.Config{
			TailcfgDNSConfig: nil,
		},
		acmeChallenges: dns.NewACMEChallengeStore(),
	}

	// Should not panic with nil DNSConfig.
	h.recomposeExtraRecords()
}
