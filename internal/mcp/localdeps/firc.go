package localdeps

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/firc"
	mcpsrv "github.com/hoaxisr/awg-manager/internal/mcp"
)

const maxFIRCGroups = 32

func (l *Local) FIRCStatus(ctx context.Context) (mcpsrv.FIRCStatus, error) {
	op := l.c.FIRC
	if op == nil {
		return mcpsrv.FIRCStatus{}, errUnavailable("AWGM Route")
	}
	if err := op.CheckNotForeign(); err != nil {
		return mcpsrv.FIRCStatus{}, errors.New("could not verify AWGM ownership of the FIRC runtime")
	}

	out := mcpsrv.FIRCStatus{
		Runtime:          "unknown",
		GroupsSource:     "unknown",
		Groups:           []mcpsrv.FIRCGroupStatus{},
		RecoveryRequired: op.IsRecoveryRequired(),
	}
	if l.c.RoutingEngine != nil {
		if engine, known := l.c.RoutingEngine(); known {
			selected := strings.EqualFold(strings.TrimSpace(engine), "firc")
			out.Selected = &selected
		}
	}

	var groups []firc.Group
	if out.Selected != nil && *out.Selected {
		readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		live, err := op.Client().GetGroups(readCtx, false)
		cancel()
		if err == nil && live != nil {
			groups = live
			out.GroupsSource = "runtime"
			out.Runtime = "responding"
		} else if ctx.Err() == nil && op.IsRunning(ctx) {
			out.Runtime = "responding"
		}
	}
	if err := ctx.Err(); err != nil {
		return mcpsrv.FIRCStatus{}, err
	}
	if out.GroupsSource != "runtime" {
		saved, err := op.LoadGroupsFile()
		if err != nil || saved == nil {
			return out, nil
		}
		groups = saved
		out.GroupsSource = "saved"
	}

	total := len(groups)
	out.GroupsTotal = &total
	out.GroupsComplete = total <= maxFIRCGroups
	for _, g := range groups[:min(total, maxFIRCGroups)] {
		out.Groups = append(out.Groups, fircGroupStatus(g, out.GroupsSource == "runtime"))
	}
	return out, nil
}

func fircGroupStatus(g firc.Group, fromRuntime bool) mcpsrv.FIRCGroupStatus {
	row := mcpsrv.FIRCGroupStatus{
		ID: sanitizeLabel(g.ID), Name: sanitizeLabel(g.Name), Enabled: g.Enable,
		ConfiguredOutbounds: make([]string, 0, len(g.Interface)),
	}
	for _, iface := range g.Interface {
		row.ConfiguredOutbounds = append(row.ConfiguredOutbounds, sanitizeLabel(iface))
	}
	// fircd supplies a reason for every non-live group. Older or partial
	// responses lacking both indicators leave liveness unknown.
	if !fromRuntime || (!g.Live && g.LiveReason == "") {
		return row
	}
	row.Live = &g.Live
	// fircd can echo interface[0] as active on a non-live group. The dns
	// and block targets are actions and have no egress interface.
	if g.Enable && g.Live && g.Active != "block" && g.Active != firc.DNSOnlyInterface {
		row.ActiveOutbound = sanitizeLabel(g.Active)
	}
	return row
}
