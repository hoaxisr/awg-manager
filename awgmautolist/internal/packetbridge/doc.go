// Package packetbridge connects normalized packet events and DNS answers to
// the autolist judge.
//
// The bridge owns no packet parsing, routing, probing, or HTTP behavior. It
// converts packet direction to the judge's client-to-server flow convention,
// filters local destinations, and exposes judge output through Results and
// optional callbacks. Like judge.Judge, a Bridge is not safe for concurrent
// use.
package packetbridge
