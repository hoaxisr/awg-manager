// Package query is the read side of the NDMS CQRS layer.
//
// Each Store owns the cached view of one NDMS resource. Stores share the
// same shape: TTL cache + single-flight dedup + stale-ok fallback on
// upstream errors. See docs/superpowers/specs/2026-04-17-ndms-rci-
// architecture-design.md §4 for the full design.
//
// Stores depend on a Getter interface (subset of transport.Client) so
// tests can inject a fake without spinning up httptest.
package query
