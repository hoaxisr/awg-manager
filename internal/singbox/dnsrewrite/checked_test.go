package dnsrewrite

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
)

func TestCheckedEditsRefuseStaleEntry(t *testing.T) {
	for _, operation := range []string{"update", "delete"} {
		t.Run(operation, func(t *testing.T) {
			original := DNSRewrite{Pattern: "nas.invalid", IPs: []string{"192.0.2.1"}}
			store := &fakeStore{items: []DNSRewrite{original}}
			orch := newFakeOrch()
			svc := NewService(store, orch)
			revision := Revision(store.items)
			changed := DNSRewrite{Pattern: original.Pattern, IPs: []string{"192.0.2.2"}}
			if err := svc.Update(0, changed); err != nil {
				t.Fatal(err)
			}
			calls := orch.saveCalls
			var err error
			if operation == "update" {
				err = svc.UpdateChecked(context.Background(), 0, original, revision)
			} else {
				err = svc.DeleteChecked(context.Background(), 0, revision)
			}
			if !errors.Is(err, ErrConflict) || !reflect.DeepEqual(store.items, []DNSRewrite{changed}) || orch.saveCalls != calls {
				t.Fatalf("stale edit: err=%v store=%+v saves=%d", err, store.items, orch.saveCalls)
			}
		})
	}
}

func TestCheckedEditsRefuseShiftOntoIdenticalDuplicate(t *testing.T) {
	for _, operation := range []string{"update", "delete"} {
		t.Run(operation, func(t *testing.T) {
			r := DNSRewrite{Pattern: "same.invalid", IPs: []string{"192.0.2.1"}}
			store := &fakeStore{items: []DNSRewrite{r, r}}
			orch := newFakeOrch()
			svc := NewService(store, orch)
			revision := Revision(store.items)
			if err := svc.Delete(0); err != nil {
				t.Fatal(err)
			} // REST removes the originally selected entry.
			calls := orch.saveCalls
			var err error
			if operation == "delete" {
				err = svc.DeleteChecked(context.Background(), 0, revision)
			} else {
				err = svc.UpdateChecked(context.Background(), 0, DNSRewrite{Pattern: "other.invalid", IPs: r.IPs}, revision)
			}
			if !errors.Is(err, ErrConflict) || len(store.items) != 1 || store.items[0].Pattern != r.Pattern || orch.saveCalls != calls {
				t.Fatalf("shifted duplicate was edited: %v %+v", err, store.items)
			}
		})
	}
}

func TestCheckedConcurrentWritersOnlyOneCommits(t *testing.T) {
	r := DNSRewrite{Pattern: "nas.invalid", IPs: []string{"192.0.2.1"}}
	store := &fakeStore{items: []DNSRewrite{r}}
	orch := newFakeOrch()
	svc := NewService(store, orch)
	revision := Revision(store.items)
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, ip := range []string{"192.0.2.2", "192.0.2.3"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- svc.UpdateChecked(context.Background(), 0, DNSRewrite{Pattern: r.Pattern, IPs: []string{ip}}, revision)
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 || orch.saveCalls != 1 {
		t.Fatalf("committed=%d conflicts=%d saves=%d", success, conflict, orch.saveCalls)
	}
}

func TestCheckedEditsRejectEmptyRevisionAndCancelledCaller(t *testing.T) {
	r := DNSRewrite{Pattern: "nas.invalid", IPs: []string{"192.0.2.1"}}
	store := &fakeStore{items: []DNSRewrite{r}}
	orch := newFakeOrch()
	svc := NewService(store, orch)
	if err := svc.UpdateChecked(context.Background(), 0, r, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("unguarded update: %v", err)
	}
	if err := svc.DeleteChecked(context.Background(), 0, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("unguarded delete: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := svc.UpdateChecked(ctx, 0, r, Revision(store.items)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled update: %v", err)
	}
	if err := svc.DeleteChecked(ctx, 0, Revision(store.items)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled delete: %v", err)
	}
	if orch.saveCalls != 0 || len(store.items) != 1 {
		t.Fatal("rejected edit mutated storage")
	}
}

func TestCheckedEditsManagedAndCancellation(t *testing.T) {
	r := DNSRewrite{Pattern: "managed.invalid", IPs: []string{"192.0.2.1"}, Managed: "preset"}
	store := &fakeStore{items: []DNSRewrite{r}}
	orch := newFakeOrch()
	svc := NewService(store, orch)
	if err := svc.UpdateChecked(context.Background(), 0, r, Revision(store.items)); !errors.Is(err, ErrManaged) {
		t.Fatalf("managed update: %v", err)
	}
	if err := svc.DeleteChecked(context.Background(), 0, Revision(store.items)); !errors.Is(err, ErrManaged) {
		t.Fatalf("managed delete: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := svc.AddContext(ctx, DNSRewrite{Pattern: "new.invalid", IPs: []string{"192.0.2.2"}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled add: %v", err)
	}
	if orch.saveCalls != 0 || !reflect.DeepEqual(store.items, []DNSRewrite{r}) {
		t.Fatal("refused write changed state")
	}
}
