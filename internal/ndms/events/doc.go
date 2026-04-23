// Package events is the NDMS hook consumer — the push-side counterpart
// to the query-side Stores. Hook scripts deployed to /opt/etc/ndm/*.d/
// POST to /api/hook/ndms; the handler enqueues typed Events into a
// pending-set Dispatcher; the Dispatcher's worker goroutine invalidates
// the affected Store caches (so on the next Read, fresh NDMS data is
// fetched). See design spec §6 for the full design.
package events
