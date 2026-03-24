# Funnel Ingress Proxy Design (Draft)

## Goal

Provide production-grade, policy-aware public ingress for `tailscale funnel`
workloads while keeping control-plane and data-plane responsibilities explicit.

## Scope

- Implement control-plane approval and publication flow for funnel-enabled nodes.
- Define data-plane ingress proxy behavior for HTTP and TLS-terminated TCP.
- Ensure policy enforcement happens per node and per exposed port.
- Keep client-facing behavior aligned with upstream Serve/Funnel UX.

## Non-Goals

- Replacing client-local `ipn.ServeConfig` persistence.
- Introducing Headscale-side persistence for user Serve configs.
- Supporting arbitrary L7 routing beyond current Serve semantics.

## Architecture

- **Control plane**
  - Node advertises Serve/Funnel config through Hostinfo and service metadata.
  - Policy engine computes funnel eligibility from `nodeAttrs` and configured ports.
  - Mapper advertises funnel capability only for approved nodes/ports.
- **Ingress plane**
  - Public edge accepts inbound traffic for approved FQDN/port tuples.
  - Edge resolves destination node/VIP from control-plane published state.
  - Edge forwards traffic to tailnet destination over authenticated tunnel links.
- **Node plane**
  - Client keeps local Serve handlers (`ipn.ServeConfig`) and local listener wiring.
  - Unapproved funnel endpoints remain local-only and are not publicly published.

## Proposed Components

- **Funnel registry (control-plane state)**
  - In-memory index keyed by `fqdn:port`.
  - Value contains node ID, service target, mode (HTTP/TCP/TLS-terminated TCP), and approval status.
- **Ingress edge worker**
  - Terminates public listener sockets.
  - Applies coarse rate limits and request size limits.
  - Enforces control-plane approval decisions before forwarding.
- **Forwarding transport**
  - Reuses existing tailnet reachability primitives where possible.
  - Tracks backend health and fail-fast behavior for unreachable nodes.

## Request Flow

1. Node enables funnel locally and advertises config.
2. Headscale validates node/port policy and publishes approved mapping.
3. Public request arrives at ingress edge for `service.domain:port`.
4. Edge validates active approval and resolves target node backend.
5. Edge forwards stream/request to backend node over tailnet path.
6. Response returns to client; observability events are emitted.

## Security Model

- Policy is authoritative: no publication without explicit control-plane approval.
- Publication is least-privilege: per-node and per-port, not global.
- Ingress never forwards unknown or stale mappings.
- Revocation must be near-real-time on policy change, service clear, or node offline.

## Failure Handling

- **Stale mapping:** treat as not found, trigger refresh, do not forward.
- **Backend unreachable:** return upstream-aligned 502/connection failure semantics.
- **Policy revoked:** remove mapping and reject new ingress immediately.
- **Control-plane unavailable:** fail closed for new publications; serve only still-valid cached entries if explicitly configured.

## Observability

- Metrics:
  - active funnel mappings
  - rejected ingress by reason (policy, stale, missing backend)
  - backend dial latency and failure rate
- Logs:
  - mapping lifecycle (publish, update, revoke)
  - ingress decision points with request correlation IDs

## Rollout Plan

1. Ship control-plane gating and mapping index behind feature flag.
2. Add shadow ingress mode (validate decisions without forwarding).
3. Enable forwarding for allowlisted test domains.
4. Gradually expand to general availability.

## Open Questions

- Where to run edge workers (embedded vs dedicated process)?
- Do we need sticky routing for long-lived TCP streams?
- How should multi-region ingress choose nearest healthy edge?
