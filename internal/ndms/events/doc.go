// Package events is the NDMS hook consumer — the push-side counterpart
// to the query-side Stores. Hook scripts deployed to /opt/etc/ndm/*.d/
// append one line per event to a spool file on tmpfs (DefaultSpoolPath);
// SpoolReader reads it via inotify in file order and hands Events to
// api.HookSink, which enqueues them into a pending-set Dispatcher; the
// Dispatcher's worker goroutine invalidates the affected Store caches (so
// on the next Read, fresh NDMS data is fetched). See design spec §6 for
// the full design.
package events
