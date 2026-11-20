//go:build linux

package wireguard

import (
	"context"
	"fmt"
	"strconv"

	"github.com/hoaxisr/awg-manager/internal/uplink"
)

// adoptAppliedMark is called only while holding the manager/tunnel locks,
// immediately after our checked NDMS connect-via operation. Unsolicited mark
// changes observed by Validate/Probe are still conflicts, never auto-adopted.
func (m *Managed) adoptAppliedMark(ctx context.Context, target ManagedTarget, previous Config, device string) error {
	if m.deps.PeerMark == nil {
		return nil
	}
	mark, err := m.mark(ctx, target)
	if err != nil {
		return err
	}
	if mark == previous.Mark {
		return nil
	}
	a, err := m.kernel()
	if err != nil {
		return err
	}
	// Audit the entire old reservation even though the runtime mark has just
	// changed. This does not relax ownership of any rule or table route.
	if _, err = a.auditResources(ctx, previous, false, false); err != nil {
		return err
	}
	next := previous
	next.Mark, next.RetiringMark = mark, previous.Mark
	// Include the new mark in overlap/selector checks before any mutation.
	if _, err = a.auditResources(ctx, next, true, false); err != nil {
		return err
	}
	m.profiles[target.Ref] = next
	if err = m.save(); err != nil {
		m.profiles[target.Ref] = previous
		return err
	}
	// Durable ownership of both marks precedes changes. A crash or error leaves
	// a recoverable disabled-policy cleanup path; it cannot appear healthy.
	if err = m.ensure(ctx, next); err != nil {
		return err
	}
	a, err = m.kernel()
	if err != nil {
		return err
	}
	if err = a.verifyPath(ctx, next, device); err != nil {
		return err
	}
	s, err := a.readResources(ctx, next, false)
	if err != nil {
		return err
	}
	for _, family := range s.RetiringRules {
		if _, err = m.deps.Run(ctx, "ip", family, "rule", "del", "pref", strconv.Itoa(next.Priority), "fwmark", fmt.Sprintf("0x%x", previous.Mark), "lookup", strconv.Itoa(next.Table)); err != nil {
			return err
		}
	}
	next.RetiringMark = 0
	// Prove the old guards are gone and the new mark still selects our table.
	if _, err = a.readResources(ctx, next, false); err != nil {
		return err
	}
	if err = a.verifyPath(ctx, next, device); err != nil {
		return err
	}
	m.profiles[target.Ref] = next
	if err = m.save(); err != nil {
		next.RetiringMark = previous.Mark
		m.profiles[target.Ref] = next
		return fmt.Errorf("%w: commit native mark handover: %v", uplink.ErrConflict, err)
	}
	return nil
}
