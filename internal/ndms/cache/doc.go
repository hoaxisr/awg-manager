// Package cache provides generic primitives used by the NDMS Query Stores.
//
// TTL is a thread-safe TTL-based cache with a Peek() that returns the last
// observed value even after expiry — used by Stores for the "stale-ok on
// NDMS error" behavior described in the design spec.
//
// SingleFlight coalesces concurrent callers that ask for the same key: the
// first caller triggers the fetch; the rest wait on the shared promise.
package cache
