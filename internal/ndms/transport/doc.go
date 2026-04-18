// Package transport is the low-level NDMS HTTP client for awg-manager.
//
// Client exposes Get / GetRaw / Post / PostBatch — the only HTTP-level
// entry points used by every query/ and command/ consumer. All calls go
// through a bounded concurrency semaphore that keeps NDMS from being
// overloaded by our own bursts.
package transport
