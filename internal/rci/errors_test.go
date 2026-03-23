package rci

import "testing"

func TestExtractError_StatusError(t *testing.T) {
	data := []byte(`{"status":[{"status":"error","message":"address conflict"}]}`)
	msg := ExtractError(data)
	if msg != "address conflict" {
		t.Errorf("got %q", msg)
	}
}

func TestExtractError_NestedError(t *testing.T) {
	data := []byte(`{"interface":{"Wireguard0":{"status":"error","message":"not found"}}}`)
	msg := ExtractError(data)
	if msg != "not found" {
		t.Errorf("got %q", msg)
	}
}

func TestExtractError_NoError(t *testing.T) {
	data := []byte(`{"interface":{"Wireguard0":{"state":"up"}}}`)
	msg := ExtractError(data)
	if msg != "" {
		t.Errorf("expected empty, got %q", msg)
	}
}

func TestExtractError_BatchWithError(t *testing.T) {
	data := []byte(`[{"status":"ok"}, {"status":"error","message":"failed"}]`)
	msg := ExtractError(data)
	if msg != "failed" {
		t.Errorf("got %q", msg)
	}
}

func TestExtractError_InvalidJSON(t *testing.T) {
	msg := ExtractError([]byte(`not json`))
	if msg != "" {
		t.Errorf("expected empty, got %q", msg)
	}
}

func TestExtractError_UnknownError(t *testing.T) {
	data := []byte(`{"status":"error"}`)
	msg := ExtractError(data)
	if msg != "unknown RCI error" {
		t.Errorf("got %q", msg)
	}
}
