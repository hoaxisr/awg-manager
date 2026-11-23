package uplink

import (
	"context"
	"errors"
	"testing"
)

func TestBackupPauseRejectsMutationsAndRestartsReconciliation(t *testing.T) {
	c, a, _, _ := fixture(t)
	p := c.List()[0].Policy
	if err := c.Pause(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !c.BackupPaused() {
		t.Fatal("lifecycle cannot observe backup pause")
	}
	c.Tick(context.Background())
	if len(a.activated) > 0 {
		t.Fatal("backup raced activation")
	}
	if _, err := c.Put(context.Background(), p); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if err := c.Delete(context.Background(), p.ID, p.Revision); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if err := c.Switch(context.Background(), p.ID, "ISP2", p.Revision); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	c.BeginBackupResume()
	if c.BackupPaused() {
		t.Fatal("native lifecycle cannot resume after export")
	}
	if _, err := c.Put(context.Background(), p); !errors.Is(err, ErrConflict) {
		t.Fatal("policy mutations resumed before child services", err)
	}
	c.Resume()
	if c.BackupPaused() {
		t.Fatal("backup pause remained after resume")
	}
	c.Tick(context.Background())
	if state(c).Phase != "healthy" {
		t.Fatal(state(c))
	}
}
