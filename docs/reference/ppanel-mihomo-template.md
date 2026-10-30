# PPanel Mihomo template

Use [`configs/ppanel-clash-mihomo.example.yaml.tmpl`](../../configs/ppanel-clash-mihomo.example.yaml.tmpl) as a PPanel subscription application template with output format `yaml`.

The template uses PPanel's native Go-template variables instead of V2Board's automatic node insertion. It preserves each subscribed node's protocol, transport, authentication and REALITY fields, while rejecting empty subscriptions, unsupported protocols, duplicate names and names that collide with policy groups.

## Routing and groups

The eight visible groups are preserved: Global, Automatic, Manual, AI, Steam, Microsoft@CN, Microsoft and GoogleFCM. Automatic and manual selection contain subscription nodes only; the static direct outbound cannot win node health checks.

Google CN is routed to the node-only automatic group before the CN direct rule. Foreign-domain rules remain before the CN-IP direct fallback. Direct rules reference the custom direct policy so its `ipv4-prefer` setting applies consistently.

A ninth hidden single-outbound group exposes direct latency without adding direct to automatic node selection:

- Direct: `https://connect.rom.miui.com/generate_204`
- Proxy nodes: `https://www.gstatic.com/generate_204`

These URLs must remain separate when the server blocks CN destinations. A dashboard-level custom latency URL can override group URLs; select the core-configured URL source when using this profile.

## Rules and downloads

This public profile uses full GeoSite and GeoIP DAT files with the standard loader and a 72-hour update interval. Geo data and MetaCubeXD GitHub downloads use the configured `v6.gh-proxy.org` prefix. DNS-over-HTTPS endpoints are not GitHub downloads and are not wrapped by that prefix.

The public example contains only public DNS resolvers. Keep private DNS addresses, internal domains and authenticated DoH paths in an ignored local copy; never commit them to a public repository.

## Validation

The maintained private deployment was rendered through PPanel's real `Client.Build` path and checked with Mihomo v1.19.33 for a REALITY node and a mixed ten-protocol fixture. The proxied GeoSite and GeoIP files were downloaded and parsed by the core. Direct domestic latency and node gstatic latency were also tested independently through the Mihomo API.

Mihomo's `nameserver-policy` splits comma-separated GeoSite keys into separate ordered policies. Prefer explicit positive CN/private policies over treating `geosite:!cn,!private` as an AND expression. `#RULES` means the DNS upstream connection follows routing rules; it does not itself mean “always proxy.”
