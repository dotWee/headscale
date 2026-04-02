# Features

Headscale aims to implement a self-hosted, open source alternative to the Tailscale control server. Headscale's goal is
to provide self-hosters and hobbyists with an open-source server they can use for their projects and labs. This page
provides on overview of Headscale's feature and compatibility with the Tailscale control server:

- [x] Full "base" support of Tailscale's features
- [x] [Node registration](../ref/registration.md)
    - [x] [Web authentication](../ref/registration.md#web-authentication)
    - [x] [Pre authenticated key](../ref/registration.md#pre-authenticated-key)
- [x] [DNS](../ref/dns.md)
    - [x] [MagicDNS](https://tailscale.com/kb/1081/magicdns)
    - [x] [Global and restricted nameservers (split DNS)](https://tailscale.com/kb/1054/dns#nameservers)
    - [x] [search domains](https://tailscale.com/kb/1054/dns#search-domains)
    - [x] [Extra DNS records (Headscale only)](../ref/dns.md#setting-extra-dns-records)
- [x] [Taildrop (File Sharing)](https://tailscale.com/kb/1106/taildrop)
- [x] [Tags](../ref/tags.md)
- [x] [Routes](../ref/routes.md)
    - [x] [Subnet routers](../ref/routes.md#subnet-router)
    - [x] [Exit nodes](../ref/routes.md#exit-node)
- [x] Dual stack (IPv4 and IPv6)
- [x] Ephemeral nodes
- [x] Embedded [DERP server](../ref/derp.md)
- [x] Access control lists ([GitHub label "policy"](https://github.com/juanfont/headscale/labels/policy%20%F0%9F%93%9D))
    - [x] ACL management via API
    - [x] Some [Autogroups](https://tailscale.com/kb/1396/targets#autogroups), currently: `autogroup:internet`,
      `autogroup:nonroot`, `autogroup:member`, `autogroup:tagged`, `autogroup:self`
    - [x] [Auto approvers](https://tailscale.com/kb/1337/acl-syntax#auto-approvers) for [subnet
      routers](../ref/routes.md#automatically-approve-routes-of-a-subnet-router) and [exit
      nodes](../ref/routes.md#automatically-approve-an-exit-node-with-auto-approvers)
    - [x] [Tailscale SSH](https://tailscale.com/kb/1193/tailscale-ssh)
- [x] [Node registration using Single-Sign-On (OpenID Connect)](../ref/oidc.md) ([GitHub label "OIDC"](https://github.com/juanfont/headscale/labels/OIDC))
    - [x] Basic registration
    - [x] Update user profile from identity provider
    - [ ] OIDC groups cannot be used in ACLs
- [x] [Serve and Funnel](../ref/serve.md) ([#1921](https://github.com/juanfont/headscale/issues/1921))
    - [x] [Serve](../ref/serve.md#serve) - expose local services to the tailnet
        - [x] HTTP mode (`tailscale serve --http`)
        - [x] HTTPS mode with [ACME certificate provisioning](../ref/serve.md#https-certificate-provisioning)
        - [x] Feature query endpoint (`/machine/feature/query`)
        - [x] ACME DNS-01 challenge endpoint (`/machine/set-dns`)
    - [x] [Funnel](../ref/serve.md#funnel) - expose services to the public internet
        - [x] Capability signaling (`NodeAttrFunnel`, `CapabilityFunnelPorts`)
        - [x] Ingress authorization (`PeerCapabilityIngress`)
        - [ ] Requires external [ingress relay infrastructure](../ref/serve.md#limitations)
- [ ] [Network flow logs](https://tailscale.com/kb/1219/network-flow-logs) ([#1687](https://github.com/juanfont/headscale/issues/1687))
