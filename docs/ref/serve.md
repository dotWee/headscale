# Serve and Funnel

Headscale supports [Tailscale Serve](https://tailscale.com/kb/1312/serve) and
[Tailscale Funnel](https://tailscale.com/kb/1223/funnel), allowing nodes to expose local services to the tailnet or to
the public internet.

- [Serve](#serve) exposes local services (HTTP, HTTPS, TCP) to other nodes in the same tailnet. Peers can access served
  content via the node's MagicDNS name.
- [Funnel](#funnel) extends Serve to expose services publicly over the internet. Funnel requires additional
  infrastructure for ingress relay and public DNS.

## Serve

Tailscale Serve lets a node proxy local services and make them available to other tailnet members. The Tailscale client
handles the proxying locally; the control server (Headscale) only signals that the feature is available by granting the
appropriate [capabilities](#capabilities).

### Configuration

Serve is **enabled by default**. To disable it, set `serve.enabled` to `false` in the
[configuration file](./configuration.md):

```yaml title="config.yaml"
serve:
  enabled: true
```

Or via environment variable:

```console
$ HEADSCALE_SERVE_ENABLED=true
```

### Using Serve

Once Headscale has Serve enabled, nodes can use the standard Tailscale CLI to expose local services.

#### Expose an HTTP service

Start a local web server and proxy it to the tailnet on port 80:

```console
$ tailscale serve --bg --http=80 http://127.0.0.1:3000
```

Other tailnet members can then access the service at `http://<node-name>.<base-domain>/`.

#### Expose an HTTPS service

By default, `tailscale serve` uses HTTPS with automatic TLS certificate provisioning:

```console
$ tailscale serve --bg 3000
```

This exposes the service at `https://<node-name>.<base-domain>/` on port 443. Certificate provisioning requires
additional DNS setup; see [HTTPS certificate provisioning](#https-certificate-provisioning).

#### Serve static text

```console
$ tailscale serve --bg --http=80 text:"Hello from my node"
```

#### View and manage serve configuration

```console
$ tailscale serve status
$ tailscale serve reset
```

!!! tip "Background mode"

    The `--bg` flag runs the serve configuration in the background. Without it, `tailscale serve` runs in the foreground
    and stops when you press Ctrl+C. Background configurations persist across restarts of the Tailscale daemon.

### HTTPS certificate provisioning

When using `tailscale serve` in HTTPS mode (the default), the Tailscale client provisions TLS certificates via
[ACME DNS-01 challenges](https://letsencrypt.org/docs/challenge-types/#dns-01-challenge). This process requires:

1. **Headscale signals `CertDomains`** in the DNS configuration sent to each node. This tells the client which domain
   name it can request certificates for (its MagicDNS FQDN, e.g., `mynode.headscale.net`).

2. **The client calls `POST /machine/set-dns`** on the control server to create an `_acme-challenge.<domain>` TXT DNS
   record containing the ACME challenge token.

3. **The ACME certificate authority (Let's Encrypt)** queries public DNS for the TXT record to verify domain ownership.

4. **The certificate is issued** and stored locally on the node.

Headscale implements steps 1 and 2. The TXT records are stored in memory and injected into the DNS configuration. For
the ACME CA to verify the challenge (step 3), the `_acme-challenge` TXT records must be resolvable via **public DNS**.

!!! warning "Public DNS requirement for HTTPS"

    HTTPS certificate provisioning requires that the base domain's DNS is configured so that `_acme-challenge.<node>.
    <base-domain>` TXT records are resolvable by Let's Encrypt. This works automatically when Headscale is the
    authoritative DNS server for the base domain. In other setups, additional DNS delegation may be required.

    If your deployment does not have public DNS for the base domain, use HTTP mode instead:

    ```console
    $ tailscale serve --bg --http=80 http://127.0.0.1:3000
    ```

    HTTP mode works without any DNS infrastructure and is fully supported.

!!! tip "Headscale's own TLS is separate"

    The TLS configuration described in [TLS](./tls.md) is for Headscale's own web service (the control server). It is
    completely separate from the TLS certificates that `tailscale serve` provisions for individual nodes.

## Funnel

[Tailscale Funnel](https://tailscale.com/kb/1223/funnel) extends Serve to expose services publicly over the internet.
When Funnel is enabled, Headscale grants additional capabilities and ingress authorization to nodes.

### Configuration

Funnel is **disabled by default** because it exposes services to the public internet. To enable it, set both
`serve.enabled` and `funnel.enabled` to `true`:

```yaml title="config.yaml" hl_lines="2 6"
serve:
  enabled: true

funnel:
  enabled: true
  # Ports that nodes are allowed to use for Funnel.
  # Default: [443, 8443, 10000]
  # allowed_ports:
  #   - 443
  #   - 8443
  #   - 10000
```

Or via environment variables:

```console
$ HEADSCALE_SERVE_ENABLED=true
$ HEADSCALE_FUNNEL_ENABLED=true
```

### Funnel ports

The `funnel.allowed_ports` setting controls which TCP ports nodes can use for Funnel. The default ports match the
Tailscale SaaS defaults:

| Port  | Description                 |
|-------|-----------------------------|
| 443   | Standard HTTPS              |
| 8443  | Alternative HTTPS           |
| 10000 | High port for custom use    |

### Limitations

Funnel requires infrastructure beyond Headscale's control server:

1. **Ingress relay nodes**: Traffic from the public internet must be forwarded to the target node through DERP relay
   nodes that support ingress. Headscale grants the `PeerCapabilityIngress` authorization, but the relay infrastructure
   must be deployed separately.

2. **Public DNS**: The node's FQDN must resolve publicly to the ingress relay's IP address. This requires DNS records
   pointing `<node>.<base-domain>` to the ingress infrastructure.

3. **HTTPS certificates**: Funnel uses HTTPS, which requires the same
   [certificate provisioning](#https-certificate-provisioning) setup as Serve HTTPS mode.

!!! warning "Funnel is not fully self-contained"

    Unlike Serve (which works entirely within the tailnet), Funnel depends on external infrastructure for ingress relay
    and public DNS. These are operational concerns that must be configured outside of Headscale.

## Capabilities

Headscale signals Serve and Funnel support to nodes through capabilities in the `CapMap` field of the node's
configuration (sent via `MapResponse`). The capabilities granted depend on the server configuration:

### When Serve is enabled

| Capability                | Description                                          |
|---------------------------|------------------------------------------------------|
| `CapabilityHTTPS`         | Enables HTTPS certificate provisioning on the node   |

The node also receives its FQDN in `DNSConfig.CertDomains`, telling the client which domain it can provision
certificates for.

### When Funnel is enabled

In addition to the Serve capabilities:

| Capability                   | Description                                         |
|------------------------------|-----------------------------------------------------|
| `NodeAttrFunnel`             | Enables the Funnel feature on the node              |
| `CapabilityFunnelPorts`      | Specifies which ports the node can use for Funnel   |
| `PeerCapabilityIngress`      | Grants ingress authorization (via packet filter)    |

The `CapabilityFunnelPorts` value encodes the allowed ports in the capability key URL (e.g.,
`https://tailscale.com/cap/funnel-ports?ports=443,8443,10000`). The Tailscale client parses this to determine which
ports are permitted.

## Feature query endpoint

When a user runs `tailscale serve` or `tailscale funnel` for the first time, the Tailscale client may call
`POST /machine/feature/query` to check if the feature is enabled on the control server. Headscale responds with:

- **Feature enabled**: `{ "Complete": true }` - the client proceeds.
- **Feature disabled**: `{ "Complete": false, "Text": "..." }` - the client displays the text explaining how to enable
  the feature.

## Architecture overview

The Serve and Funnel implementation follows the Tailscale protocol design where the control server is responsible for
capability signaling, not for storing or managing the serve configuration itself.

```
                    Control Plane (Headscale)
                    ┌──────────────────────────────┐
                    │ Signals capabilities:        │
                    │ - CapabilityHTTPS             │
                    │ - NodeAttrFunnel              │
                    │ - CapabilityFunnelPorts        │
                    │ - CertDomains                │
                    │ - PeerCapabilityIngress       │
                    │                              │
                    │ Handles:                     │
                    │ - POST /feature/query         │
                    │ - POST /set-dns (ACME)        │
                    └──────────┬───────────────────┘
                               │ MapResponse
                               ▼
           Node A (Server)              Node B (Consumer)
    ┌─────────────────────┐      ┌──────────────────────┐
    │ tailscale serve      │      │                      │
    │ --bg --http=80       │◄─────│ curl http://a.net/   │
    │ http://localhost:3000│      │                      │
    │                     │      │ (accesses service    │
    │ ServeConfig stored  │      │  via tailnet)        │
    │ LOCALLY on node     │      │                      │
    └─────────────────────┘      └──────────────────────┘
```

Key design points:

- **ServeConfig is local**: The serve configuration (what ports, what backends, what paths) is stored entirely on the
  Tailscale client. Headscale never sees or stores it.
- **Identity headers are local**: The `Tailscale-User-Login` and related headers that Serve adds to proxied requests
  are resolved from the local netmap cache, not from the control server.
- **Capabilities are global**: Headscale enables or disables Serve/Funnel for all nodes via the server configuration.
  Per-node capability control is not currently supported.
