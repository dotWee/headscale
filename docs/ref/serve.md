# Tailscale Serve

Headscale supports the control-plane pieces required for private `tailscale serve`.

This page describes what is implemented, how it works, and which parts are still out of scope.

## Support level

Headscale currently supports:

- Private `tailscale serve` inside the tailnet
- Client-managed Serve configuration via the Tailscale LocalAPI
- Node-scoped HTTP proxy and TCP forwarding Serve modes
- `tailscale serve status` and `tailscale serve reset` for node-scoped private Serve
- HTTPS certificate provisioning support for Serve when `serve.https.enabled` is configured
- ACME DNS-01 challenge updates through RFC2136

Headscale currently does not support:

- [Funnel](https://tailscale.com/kb/1223/funnel)
- Service-host mode such as `tailscale serve --service`
- Service advertisement flows such as `tailscale serve advertise` and `tailscale serve drain`
- Service-host configuration import/export via `tailscale serve get-config` and `tailscale serve set-config`
- Additional DNS challenge providers beyond RFC2136

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
- Funnel-related capability placeholders exist internally, but Funnel is not exposed as supported.
- Service-hosting requires additional control-plane support such as VIP service collection and `c2n` service discovery, which Headscale does not implement yet.

## Implementation notes

The current implementation is intentionally narrow:

- Serve config is not persisted by Headscale
- No new Headscale API was added for editing Serve config
- No database migration is required
- RFC2136 is the only built-in DNS challenge backend
- Service-host and Funnel parity still require additional upstream-style control-plane work

This keeps Headscale aligned with current Tailscale client behavior while leaving room for future Funnel and service-hosting work.
