package multi

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/samosvalishe/free-turn-proxy/internal/provider"
)

// fakeProvider записывает streamID, с которыми его звали, и кодирует свой индекс
// в выданных Credentials - чтобы проверить и маршрут, и значение.
type fakeProvider struct {
	idx        int
	backoff    int64
	gotCreds   []int
	gotHandle  []int
	gotReset   []int
	authResult bool
}

func (f *fakeProvider) GetCredentials(_ context.Context, streamID int) (provider.Credentials, error) {
	f.gotCreds = append(f.gotCreds, streamID)
	return provider.Credentials{User: fmt.Sprintf("p%d-s%d", f.idx, streamID)}, nil
}
func (f *fakeProvider) IsAuthError(error) bool     { return f.authResult }
func (f *fakeProvider) HandleAuthError(s int) bool { f.gotHandle = append(f.gotHandle, s); return true }
func (f *fakeProvider) ResetErrors(s int)          { f.gotReset = append(f.gotReset, s) }
func (f *fakeProvider) BackoffUntilUnix() int64    { return f.backoff }
func (f *fakeProvider) Name() string               { return fmt.Sprintf("fake%d", f.idx) }

func newFakes(n int) ([]provider.Provider, []*fakeProvider) {
	ps := make([]provider.Provider, n)
	fs := make([]*fakeProvider, n)
	for i := range ps {
		f := &fakeProvider{idx: i}
		ps[i], fs[i] = f, f
	}
	return ps, fs
}

func TestNewPanicsOnEmpty(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on empty providers")
		}
	}()
	New(nil)
}

func TestGetCredentialsRoundRobin(t *testing.T) {
	ps, fs := newFakes(3)
	m := New(ps)

	// 3 провайдера x 10 стримов = 30 глобальных streamID.
	for sid := 1; sid <= 30; sid++ {
		creds, err := m.GetCredentials(context.Background(), sid)
		if err != nil {
			t.Fatalf("streamID %d: %v", sid, err)
		}
		wantIdx := (sid - 1) % 3
		wantInner := ((sid - 1) / 3) + 1
		want := fmt.Sprintf("p%d-s%d", wantIdx, wantInner)
		if creds.User != want {
			t.Fatalf("streamID %d: got %q, want %q", sid, creds.User, want)
		}
	}

	// Каждый провайдер получил ровно innerID 1..10.
	for i, f := range fs {
		if len(f.gotCreds) != 10 {
			t.Fatalf("provider %d got %d calls, want 10", i, len(f.gotCreds))
		}
		for j, inner := range f.gotCreds {
			if inner != j+1 {
				t.Fatalf("provider %d call %d: innerID %d, want %d", i, j, inner, j+1)
			}
		}
	}
}

func TestHandleAndResetRouting(t *testing.T) {
	ps, fs := newFakes(2)
	m := New(ps)

	// streamID 5: idx=(5-1)%2=0, inner=(5-1)/2+1=3.
	m.HandleAuthError(5)
	m.ResetErrors(5)
	if len(fs[0].gotHandle) != 1 || fs[0].gotHandle[0] != 3 {
		t.Fatalf("HandleAuthError routed wrong: %v", fs[0].gotHandle)
	}
	if len(fs[0].gotReset) != 1 || fs[0].gotReset[0] != 3 {
		t.Fatalf("ResetErrors routed wrong: %v", fs[0].gotReset)
	}
	if len(fs[1].gotHandle) != 0 {
		t.Fatalf("provider 1 should not be touched: %v", fs[1].gotHandle)
	}
}

func TestBackoffUntilUnixMax(t *testing.T) {
	ps, fs := newFakes(3)
	fs[0].backoff = 100
	fs[1].backoff = 500
	fs[2].backoff = 300
	if got := New(ps).BackoffUntilUnix(); got != 500 {
		t.Fatalf("BackoffUntilUnix = %d, want 500 (max)", got)
	}
}

func TestIsAuthErrorDelegatesToFirst(t *testing.T) {
	ps, fs := newFakes(2)
	fs[0].authResult = true
	if !New(ps).IsAuthError(errors.New("x")) {
		t.Fatal("IsAuthError should delegate to providers[0]")
	}
}

func TestName(t *testing.T) {
	ps, _ := newFakes(4)
	if got := New(ps).Name(); got != "multi(4)" {
		t.Fatalf("Name = %q, want multi(4)", got)
	}
}
