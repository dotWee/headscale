# Tailscale Serve

Headscale supports the control-plane pieces required for private `tailscale serve`.

This page describes what is implemented, how it works, and which parts are still out of scope.

## Completion status

Headscale is currently close to feature-complete for private node-scoped tailnet Serve, but not for the full Tailscale Serve product surface.

Current rough status:

- Private node-scoped Serve: mostly implemented
- Funnel: mostly implemented for control-plane policy/capability flows, public ingress still partial
- Service-host Serve: mostly implemented for private tailnet workflows, public product parity still partial

In practical terms this means:

- using `tailscale serve` on a node to expose HTTP or TCP services inside the tailnet is supported
- enabling HTTPS for private Serve is supported with RFC2136-backed DNS-01 updates
- enabling Funnel capability and port policy is supported, but full managed public-ingress parity is not
- basic service-host workflows from newer Tailscale clients now work, but the full product surface is still incomplete

## Support level

Headscale currently supports:

- Private `tailscale serve` inside the tailnet
- Client-managed Serve configuration via the Tailscale LocalAPI
- Node-scoped HTTP proxy and TCP forwarding Serve modes
- `tailscale serve status` and `tailscale serve reset` for node-scoped private Serve
- HTTPS certificate provisioning support for Serve when `serve.https.enabled` is configured
- ACME DNS-01 challenge updates through RFC2136 and webhook-backed external DNS automation
- Operator-controlled Funnel capability advertisement and allowed-port policy
- collection of client-reported service-host metadata
- an upstream-style C2N response path for node-targeted Serve requests
- in-memory VIP service caching and service-host capability emission for fetched service metadata
- policy-gated service-host operation with `tailscale serve --service`
- service lifecycle changes with `tailscale serve advertise` and `tailscale serve drain`
- service config export/import with `tailscale serve get-config` and `tailscale serve set-config`
- tagged-node enforcement for service hosts
- `autoApprovers.services` for service-host publication approval
- `nodeAttrs`-driven Funnel allow/deny targeting

Headscale currently does not support:

- Provider-specific DNS integrations beyond RFC2136 and generic webhooks
- full managed-control-plane Funnel parity, including validated public ingress behavior
- full client workflow parity for service-host Serve

Status summary by area:

| Area | Status | Notes |
| --- | --- | --- |
| Node-scoped private Serve | Supported | HTTP proxy, TCP forwarding, status/reset, HTTPS control-plane support |
| Private Serve HTTPS | Supported | Requires `serve.https` and either RFC2136 or webhook DNS-01 support |
| Funnel capability and allowed-port policy | Supported (control-plane) | Capability + `allow_ports` + `nodeAttrs` targeting are enforced; full public ingress parity is still pending |
| Service metadata collection | Supported | `CollectServices` is consumed, `ServicesHash` changes are tracked, and fetched metadata is cached in memory |
| Service-host Serve | Supported (private workflows) | `--service`, policy-gated VIP publication, peer reachability, `advertise`/`drain`, `get-config`/`set-config` are covered; broader hosted-product parity is still incomplete |
| Full public Funnel behavior | Not supported | No complete public-ingress control-plane implementation |

## How the implementation works

Headscale does not store Serve configuration in its database.

The Serve configuration remains client-local state and uses Tailscale's upstream Go types directly, such as `ipn.ServeConfig`. In practice this means node-scoped commands such as:

- `tailscale serve`
- `tailscale serve status`
- `tailscale serve reset`

continue to operate through the local `tailscaled` instance on the node.

The newer `tailscale serve get-config` and `tailscale serve set-config` commands are part of Tailscale's service-host workflow. They currently require `--service` or `--all` and are not useful for Headscale's currently supported private node-scoped Serve mode.

Headscale provides the server-side primitives that current Tailscale clients expect:

- Node capability advertisement for Serve HTTPS
- Per-node certificate domains in the netmap DNS configuration
- `POST /machine/feature/query` for Serve/Funnel capability checks (policy-aware for Funnel per requesting node)
- `POST /machine/set-dns` for ACME DNS-01 TXT record updates
- `POST /machine/c2n/{token}` for node-targeted Serve-related C2N responses
- Funnel node capabilities and allowed-port advertisement via node `CapMap`
- `CollectServices` map-response support so clients can report service metadata
- in-memory VIP service caching and `NodeAttrServiceHost` netmap emission for service-host assignments once metadata is available
- basic service-host lifecycle support based on `/vip-services` metadata refreshes

## Private HTTP Serve

Plain HTTP Serve inside the tailnet works without additional Headscale configuration.

Example:

```shell
tailscale serve --bg --http 80 http://127.0.0.1:8080
tailscale serve status
```

Peers in the same tailnet can then access the served endpoint over the node's Tailscale name or address, subject to your ACLs.

Headscale's integration coverage currently exercises:

- HTTP proxy Serve
- `tailscale serve status`
- `tailscale serve reset`
- TCP forwarding with `tailscale serve --tcp`
- node-scoped TLS-terminated TCP with `tailscale serve --tls-terminated-tcp`
- node-scoped multi-port HTTP + TCP combinations
- Funnel enable/disable status handling
- Funnel allow/deny behavior via `nodeAttrs`
- service-host HTTP reachability and lifecycle operations (`advertise`/`drain`)
- service-host config round-trip (`get-config`/`set-config`)
- service-host TCP forwarding and TLS-terminated TCP
- service-host multi-port behavior and reconnect
- service-host VIP leak prevention with `autoApprovers.services`
- service-host VIP withdrawal after policy revocation
- service-host VIP publication after policy grant without local reconfiguration

Other node-scoped Serve combinations may work because configuration remains client-local, but they are not yet covered by Headscale's integration suite.

The CI integration matrix runs the private Serve and service-host coverage by default. Pebble-backed HTTPS e2e is wired as a scheduled/manual CI job and requires dedicated RFC2136/ACME test infrastructure plus `HEADSCALE_PEBBLE_DNS_NAMESERVER` and `HEADSCALE_PEBBLE_DNS_ZONE` CI secrets. That job now reports explicit `TESTED` vs `NOT TESTED` status and fails when Pebble e2e is required but preflight requirements are missing.

## Funnel Capability And Policy

Headscale can now advertise Funnel capability to clients when it is enabled in server configuration.

This is intentionally narrower than full Funnel product parity:

- Headscale can tell clients that Funnel is allowed
- Headscale can restrict Funnel to a configured set of ports
- Headscale can allow/deny Funnel per node using ACL `nodeAttrs`
- current clients can toggle Funnel locally when the requested port is allowed
- Headscale does not yet implement the broader managed-control-plane behavior needed for full public-ingress parity

### Public Funnel ingress boundary

Headscale currently implements Funnel control-plane signaling, not the full hosted public-ingress product.
On startup with `serve.funnel.enabled`, Headscale logs an explicit warning to avoid treating this as managed ingress parity.

In practical terms:

- client-side Funnel capability and policy checks are supported
- Headscale does not provide a built-in managed public ingress edge
- end-to-end public ingress behavior remains out of scope until dedicated ingress infrastructure is added

See `docs/ref/funnel-ingress-design.md` for the current design direction.

Configuration example:

```yaml title="config.yaml"
serve:
  https:
    enabled: true

    dns:
      provider: rfc2136
      rfc2136:
        nameserver: 192.0.2.53:53
        zone: example.com

  funnel:
    enabled: true
    allow_ports:
      - 443
      - 8443
      - 10080-10081
```

Current requirements:

- `serve.funnel.enabled` requires `serve.https.enabled`
- `serve.funnel.allow_ports` must be set explicitly
- allowed ports may be individual ports or inclusive ranges

## Service Metadata Collection

Headscale can optionally ask clients to report service-host metadata by setting:

```yaml title="config.yaml"
serve:
  service:
    collect: true
```

When enabled, Headscale sets `CollectServices=true` in map responses. Current clients can then include fields such as `Hostinfo.ServicesHash` and `Hostinfo.WireIngress` in later updates.

Headscale now consumes those signals server-side:

- changes in `Hostinfo.ServicesHash` mark cached VIP service metadata as stale
- Headscale can send a node-targeted C2N request and receive the raw HTTP response back over Noise
- when a stale service hash is seen, Headscale can fetch `GET /vip-services`
- cached VIP service metadata is stored in memory
- after control-plane restart, nodes with a cached `ServicesHash` are re-marked stale so metadata can be re-fetched without waiting for a new Hostinfo change
- assigned VIP service IPs are emitted back to the service host in `NodeAttrServiceHost`
- those VIP addresses are also added to the node's `AllowedIPs`

This is still only a partial service-host implementation. Headscale now supports:

- `tailscale serve --service`
- `tailscale serve advertise`
- `tailscale serve drain`
- `tailscale serve get-config --service`
- `tailscale serve set-config --service`
- `tailscale serve get-config --all`
- `tailscale serve set-config --all`
- tagged-node enforcement for service hosts
- peer reachability to advertised tailnet services through Headscale-managed VIPs and MagicDNS records

It still does not implement:

- the broader approval, policy, and managed-control-plane behavior of the hosted Tailscale product

At the moment, enabling `serve.service.collect` should be understood as enabling Headscale's partial service-host control-plane implementation, not as a claim of full service-host parity.

Current behavior around service-host approval:

- service hosts must be tagged nodes
- service publication can be restricted with ACL `autoApprovers.services`
- current Tailscale clients reject untagged `tailscale serve --service` usage locally
- Headscale also withholds VIP publication for untagged nodes as a control-plane safety check
- Headscale withholds VIP publication for unapproved or inactive services while preserving local client config

## Private HTTPS Serve

HTTPS Serve requires Headscale to participate in certificate provisioning.

When `tailscale serve` is used in HTTPS mode, the client checks whether the control server supports certificate provisioning and then requests ACME DNS-01 TXT record updates for the node's certificate name.

Headscale validates these requests strictly:

- only `TXT` records are accepted
- only `_acme-challenge.<node>.<domain>` is accepted for the requesting node
- the request node key must match the active Noise session

## Configuration

Serve HTTPS is configured in `config.yaml`:

```yaml title="config.yaml"
serve:
  domain: example.com

  https:
    enabled: true

    dns:
      provider: rfc2136
      ttl: 120
      timeout: 5s

      rfc2136:
        nameserver: 192.0.2.53:53
        zone: example.com
        network: udp
        tsig_key_name: ""
        tsig_secret: ""
        tsig_algorithm: hmac-sha256.

      webhook:
        url: ""
        bearer_token: ""
```

Current requirements:

- `serve.domain` defaults to `dns.base_domain` when empty, but it may be a separate delegated zone
- `serve.https.dns.ttl` and `serve.https.dns.timeout` must be greater than zero
- the Serve zone must be publicly delegated
- if using RFC2136, `serve.https.dns.rfc2136.nameserver` must be a valid `host[:port]` (port defaults to 53), the zone must be an authoritative zone (not an `_acme-challenge` label), and dynamic updates/TSIG must be configured when required
- if using webhook, `serve.https.dns.webhook.url` must be an absolute `http://` or `https://` URL that accepts JSON payloads for TXT challenge updates

See [Configuration](configuration.md), [DNS](dns.md), and [TLS](tls.md) for related settings.

## Operational notes

- HTTPS support is for private tailnet Serve. Certificate issuance still depends on public DNS because ACME DNS-01 is used.
- Headscale only updates the ACME challenge TXT record. It does not manage the rest of your authoritative DNS zone.
- DNS provider failures are normalized in server logs with retryability hints (`retryable=true|false`) to simplify operational triage.
- Serve availability is still subject to ACLs. Headscale enabling Serve does not bypass policy.
- Funnel enablement in Headscale currently means capability and port-policy advertisement to clients. It is not yet a claim of full public-ingress parity with Tailscale's managed control plane.
- Service-hosting still requires additional control-plane support for full parity, especially the broader control-plane semantics around service workflows.
- Pebble-backed HTTPS e2e coverage is available as an opt-in CI path and requires dedicated RFC2136/ACME test infrastructure.

## Implementation notes

The current implementation is intentionally narrow:

- Serve config is not persisted by Headscale
- No new Headscale API was added for editing Serve config
- No database migration is required
- RFC2136 is the only built-in DNS challenge backend
- Service-host and full Funnel parity still require additional upstream-style control-plane work

The biggest remaining gap to official parity is now the broader service-host and Funnel product surface. Headscale has the in-memory state, netmap side, `/vip-services` C2N fetch path, `--service`, `advertise`/`drain`, and `get-config`/`set-config`, but it still lacks the wider managed-control-plane parity of the hosted Tailscale product.

This keeps Headscale aligned with current Tailscale client behavior while leaving room for future Funnel and service-hosting work.
