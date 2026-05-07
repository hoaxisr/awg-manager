// Package payloads holds stateless command builders for NDMS RCI POST
// requests. Each Cmd* function returns a map[string]any payload ready for
// transport.Client.Post or transport.Client.PostBatch. No HTTP, no state,
// no dependencies on transport — pure data construction.
package payloads
