# Tailscale Serve

Headscale supports the control-plane pieces required for private `tailscale serve`.

This page describes what is implemented, how it works, and which parts are still out of scope.

## Completion status

Headscale is currently close to feature-complete for private node-scoped tailnet Serve, but not for the full Tailscale Serve product surface.

Current rough status:

- Private node-scoped Serve: mostly implemented
- Funnel: partially implemented
- Service-host Serve: not implemented

In practical terms this means:

- using `tailscale serve` on a node to expose HTTP or TCP services inside the tailnet is supported
- enabling HTTPS for private Serve is supported with RFC2136-backed DNS-01 updates
- enabling Funnel capability and port policy is supported, but full managed public-ingress parity is not
- service-host workflows from newer Tailscale clients are still mostly out of scope

## Support level

Headscale currently supports:

- Private `tailscale serve` inside the tailnet
- Client-managed Serve configuration via the Tailscale LocalAPI
- Node-scoped HTTP proxy and TCP forwarding Serve modes
- `tailscale serve status` and `tailscale serve reset` for node-scoped private Serve
- HTTPS certificate provisioning support for Serve when `serve.https.enabled` is configured
- ACME DNS-01 challenge updates through RFC2136
- Operator-controlled Funnel capability advertisement and allowed-port policy
- collection of client-reported service-host metadata
- in-memory VIP service caching and service-host capability emission for already-fetched service metadata

Headscale currently does not support:

- Service-host mode such as `tailscale serve --service`
- Service advertisement flows such as `tailscale serve advertise` and `tailscale serve drain`
- Service-host configuration import/export via `tailscale serve get-config` and `tailscale serve set-config`
- Additional DNS challenge providers beyond RFC2136
- full managed-control-plane Funnel parity, including validated public ingress behavior
- control-plane `c2n` service discovery such as `/vip-services`

Status summary by area:

| Area | Status | Notes |
| --- | --- | --- |
| Node-scoped private Serve | Supported | HTTP proxy, TCP forwarding, status/reset, HTTPS control-plane support |
| Private Serve HTTPS | Supported | Requires `serve.https` and RFC2136 DNS-01 support |
| Funnel capability and allowed-port policy | Partial | Client-side enablement works, but not full managed-control-plane parity |
| Service metadata collection | Partial | `CollectServices` is consumed, `ServicesHash` changes are tracked, and fetched metadata can be cached in memory |
| Service-host Serve | Partial | Netmaps can now carry `NodeAttrServiceHost` and VIP `AllowedIPs`, but there is still no `c2n` fetch path or client workflow parity |
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
- `POST /machine/feature/query` for Serve/Funnel capability checks
- `POST /machine/set-dns` for ACME DNS-01 TXT record updates
- Funnel node capabilities and allowed-port advertisement via node `CapMap`
- `CollectServices` map-response support so clients can report service metadata
- in-memory VIP service caching and `NodeAttrServiceHost` netmap emission for service-host assignments once metadata is available

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

Other node-scoped Serve combinations may work because configuration remains client-local, but they are not yet covered by Headscale's integration suite.

## Funnel Capability And Policy

Headscale can now advertise Funnel capability to clients when it is enabled in server configuration.

This is intentionally narrower than full Funnel product parity:

- Headscale can tell clients that Funnel is allowed
- Headscale can restrict Funnel to a configured set of ports
- current clients can toggle Funnel locally when the requested port is allowed
- Headscale does not yet implement the broader managed-control-plane behavior needed for full public-ingress parity

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
- cached VIP service metadata is stored in memory
- assigned VIP service IPs are emitted back to the service host in `NodeAttrServiceHost`
- those VIP addresses are also added to the node's `AllowedIPs`

This is still only a partial service-host implementation. It does not implement:

- `tailscale serve --service`
- `tailscale serve advertise`
- `tailscale serve drain`
- control-plane `c2n` service discovery such as `/vip-services`
- full client workflow parity for `tailscale serve --service`

At the moment, enabling `serve.service.collect` should be understood as partial control-plane groundwork, not as a signal that Headscale is compliant with Tailscale service-host Serve.

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
```

Current requirements:

- `serve.domain` defaults to `dns.base_domain` when empty, but it may be a separate delegated zone
- the Serve zone must be publicly delegated
- the zone must allow dynamic updates through RFC2136
- if TSIG is required by your DNS server, the TSIG settings must be configured

See [Configuration](configuration.md), [DNS](dns.md), and [TLS](tls.md) for related settings.

## Operational notes

- HTTPS support is for private tailnet Serve. Certificate issuance still depends on public DNS because ACME DNS-01 is used.
- Headscale only updates the ACME challenge TXT record. It does not manage the rest of your authoritative DNS zone.
- Serve availability is still subject to ACLs. Headscale enabling Serve does not bypass policy.
- Funnel enablement in Headscale currently means capability and port-policy advertisement to clients. It is not yet a claim of full public-ingress parity with Tailscale's managed control plane.
- Service-hosting still requires additional control-plane support, especially a real `c2n` fetch path for `/vip-services` and the rest of the service-host client workflow.

## Implementation notes

The current implementation is intentionally narrow:

- Serve config is not persisted by Headscale
- No new Headscale API was added for editing Serve config
- No database migration is required
- RFC2136 is the only built-in DNS challenge backend
- Service-host and full Funnel parity still require additional upstream-style control-plane work

The biggest remaining gap to official parity is still service-host support. Headscale now has the in-memory state and netmap side of that work, but it still depends on control-plane `c2n` support and the remaining client workflow semantics.

This keeps Headscale aligned with current Tailscale client behavior while leaving room for future Funnel and service-hosting work.
