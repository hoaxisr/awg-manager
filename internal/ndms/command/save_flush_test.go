package command

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

type recordingPoster struct {
	payloads []any
	err      error
}

func (p *recordingPoster) Post(_ context.Context, payload any) (json.RawMessage, error) {
	p.payloads = append(p.payloads, payload)
	if p.err != nil {
		return nil, p.err
	}
	return json.RawMessage(`{}`), nil
}

type countingInvalidator struct {
	count int
}

func (i *countingInvalidator) InvalidateAll() { i.count++ }

func requireJSONEqual(t *testing.T, got any, want string) {
	t.Helper()

	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal got: %v", err)
	}

	var gotAny any
	if err := json.Unmarshal(gotJSON, &gotAny); err != nil {
		t.Fatalf("unmarshal got json: %v", err)
	}

	var wantAny any
	if err := json.Unmarshal([]byte(want), &wantAny); err != nil {
		t.Fatalf("unmarshal want json: %v", err)
	}

	if !reflect.DeepEqual(gotAny, wantAny) {
		t.Fatalf("payload mismatch\n got: %s\nwant: %s", gotJSON, want)
	}
}

func TestSaveCoordinatorFlushSuccessSendsSavePayload(t *testing.T) {
	poster := &recordingPoster{}
	save := NewSaveCoordinator(poster, nil, 0, 0, 0, nil)
	save.SetSaveTimings(SaveEventCap, 0, SaveAfterRemoval)

	if err := save.Flush(context.Background()); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}

	if len(poster.payloads) != 1 {
		t.Fatalf("Post calls = %d, want 1", len(poster.payloads))
	}

	requireJSONEqual(t, poster.payloads[0], `{
		"system": {
			"configuration": {
				"save": {}
			}
		}
	}`)

	st := save.Status()
	if st.State != SaveStateIdle {
		t.Fatalf("State = %v, want %v", st.State, SaveStateIdle)
	}
	if st.PendingCount != 0 {
		t.Fatalf("PendingCount = %d, want 0", st.PendingCount)
	}
	if st.LastError != "" {
		t.Fatalf("LastError = %q, want empty", st.LastError)
	}
}

func TestSaveCoordinatorFlushFailureSetsFailedState(t *testing.T) {
	poster := &recordingPoster{err: errors.New("boom")}
	save := NewSaveCoordinator(poster, nil, 0, 0, 0, nil)
	save.SetSaveTimings(SaveEventCap, 0, SaveAfterRemoval)

	err := save.Flush(context.Background())
	if err == nil {
		t.Fatal("Flush() error = nil, want error")
	}
	if err.Error() != "boom" {
		t.Fatalf("Flush() error = %v, want boom", err)
	}

	st := save.Status()
	if st.State != SaveStateFailed {
		t.Fatalf("State = %v, want %v", st.State, SaveStateFailed)
	}
	if st.LastError == "" {
		t.Fatal("LastError is empty, want boom")
	}
}

func TestSaveCoordinatorFlushInvalidatesOnSuccess(t *testing.T) {
	poster := &recordingPoster{}
	invalidator := &countingInvalidator{}
	save := NewSaveCoordinator(poster, nil, 0, 0, 0, invalidator)
	save.SetSaveTimings(SaveEventCap, 0, SaveAfterRemoval)

	if err := save.Flush(context.Background()); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}
	if invalidator.count != 1 {
		t.Fatalf("invalidator count = %d, want 1", invalidator.count)
	}
}

func TestSaveCoordinatorFlushDoesNotInvalidateOnFailure(t *testing.T) {
	poster := &recordingPoster{err: errors.New("boom")}
	invalidator := &countingInvalidator{}
	save := NewSaveCoordinator(poster, nil, 0, 0, 0, invalidator)
	save.SetSaveTimings(SaveEventCap, 0, SaveAfterRemoval)

	if err := save.Flush(context.Background()); err == nil {
		t.Fatal("Flush() error = nil, want error")
	}
	if invalidator.count != 0 {
		t.Fatalf("invalidator count = %d, want 0", invalidator.count)
	}
}
