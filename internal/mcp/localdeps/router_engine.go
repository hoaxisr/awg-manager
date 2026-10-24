package localdeps

import (
	"context"
	"errors"
	"fmt"
	"strings"

	mcpsrv "github.com/hoaxisr/awg-manager/internal/mcp"
	singboxorch "github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/singbox/router"
)

// maxUserConfigOut caps the expert slot returned to the model; the web
// editor shows it whole.
const maxUserConfigOut = 64 << 10

// SwitchSingboxRouterMode starts the switch in the background, as the REST
// handler does: it outlives the call (tens of seconds), and its terminal
// error is journalled here — the transition events reach no one when no
// tab is open.
func (l *Local) SwitchSingboxRouterMode(_ context.Context, mode string) error {
	if l.c.Router == nil {
		return errUnavailable("sing-box router")
	}
	l.singboxLog.Info("mode-switch", mode, "Routing mode switch requested (MCP): "+mode)
	go func() {
		if err := l.c.Router.SwitchRoutingMode(context.Background(), mode); err != nil {
			l.singboxLog.Warn("mode-switch", mode, "Routing mode switch failed (MCP): "+err.Error())
		}
	}()
	return nil
}

// UpdateSingboxRouterSettings reads the stored settings and lays the
// caller's fields over them: UpdateSettings takes the whole struct, and the
// REST handler likewise fills what the form did not send.
func (l *Local) UpdateSingboxRouterSettings(ctx context.Context, p mcpsrv.SingboxRouterSettingsPatch) (mcpsrv.SingboxRouterStatus, error) {
	if l.c.Router == nil {
		return mcpsrv.SingboxRouterStatus{}, errUnavailable("sing-box router")
	}
	sr, err := l.c.Router.GetSettings(ctx)
	if err != nil {
		return mcpsrv.SingboxRouterStatus{}, err
	}
	if p.Sniffer != nil {
		sr.SnifferEnabled = *p.Sniffer
	}
	if p.UDPTimeout != nil {
		sr.UDPTimeout = strings.TrimSpace(*p.UDPTimeout)
	}
	if p.BypassPorts != nil {
		sr.BypassExtraPorts = strings.TrimSpace(*p.BypassPorts)
	}
	if p.BypassSubnets != nil {
		sr.BypassExtraSubnets = strings.TrimSpace(*p.BypassSubnets)
	}
	if p.PolicyName != nil {
		sr.PolicyName = strings.TrimSpace(*p.PolicyName)
	}
	if p.DeviceMode != nil {
		sr.DeviceMode = strings.TrimSpace(*p.DeviceMode)
	}
	if err := l.c.Router.UpdateSettings(ctx, sr); err != nil {
		l.singboxLog.Warn("settings", "", "Failed to update sing-box router settings (MCP): "+err.Error())
		return mcpsrv.SingboxRouterStatus{}, err
	}
	l.singboxLog.Info("settings", "", "Sing-box router settings updated (MCP)")
	return l.SingboxRouterStatus(ctx)
}

func (l *Local) GetSingboxUserConfig(context.Context) (mcpsrv.SingboxUserConfig, error) {
	if l.c.UserConfig == nil {
		return mcpsrv.SingboxUserConfig{}, errUnavailable("the expert configuration")
	}
	raw, err := l.c.UserConfig.LoadEffective(singboxorch.SlotUser)
	if err != nil {
		return mcpsrv.SingboxUserConfig{}, err
	}
	out := mcpsrv.SingboxUserConfig{HasDraft: l.c.UserConfig.HasDraft(singboxorch.SlotUser)}
	for _, st := range l.c.UserConfig.Snapshot() {
		if st.Slot == singboxorch.SlotUser {
			out.Enabled = st.Enabled
		}
	}
	if len(raw) > maxUserConfigOut {
		raw, out.Truncated = raw[:maxUserConfigOut], true
	}
	out.Content = string(raw)
	return out, nil
}

// configCheck maps the orchestrator's verdict.
func configCheck(res singboxorch.ValidationResult) mcpsrv.SingboxConfigCheck {
	out := mcpsrv.SingboxConfigCheck{Ok: res.Ok()}
	for _, e := range res.Errors {
		msg := sanitizeText(e.Message, maxMessageRunes)
		if e.Severity == singboxorch.SeverityWarning {
			out.Warnings = append(out.Warnings, msg)
		} else {
			out.Errors = append(out.Errors, msg)
		}
	}
	return out
}

// userConfigAdvisories mirrors the editor's addressOrWarnings: a rule
// mixing a rule_set with its own addresses, which sing-box ANDs (#699).
// The generated slots are rewritten; the expert slot is the user's text,
// so it is only flagged.
func userConfigAdvisories(raw []byte) []string {
	var out []string
	for _, idx := range router.FlatAddressOrRuleIndexes(raw) {
		out = append(out, fmt.Sprintf("route.rules[%d] mixes a rule_set with its own domain/ip_cidr: sing-box requires both at once unless the set holds exactly one address rule, so it matches almost nothing — split it into two rules or write logical(or)", idx))
	}
	return out
}

// SaveSingboxUserConfig writes the draft, then checks the merged
// configuration with it, as the editor's save and check do. sing-box's own
// refusal (a check error) is reported as a failed check, not a failed call:
// the draft is saved either way.
func (l *Local) SaveSingboxUserConfig(_ context.Context, content []byte) (mcpsrv.SingboxConfigCheck, error) {
	if l.c.UserConfig == nil {
		return mcpsrv.SingboxConfigCheck{}, errUnavailable("the expert configuration")
	}
	if err := l.c.UserConfig.SaveDraft(singboxorch.SlotUser, content); err != nil {
		l.singboxLog.Warn("user-config-save", "90-user.json", "Failed to save draft (MCP): "+err.Error())
		return mcpsrv.SingboxConfigCheck{}, err
	}
	l.singboxLog.Info("user-config-save", "90-user.json", "User config draft saved (MCP)")
	res, err := l.c.UserConfig.CheckMerged(singboxorch.SlotUser, content)
	if err != nil {
		return mcpsrv.SingboxConfigCheck{Ok: false, Errors: []string{sanitizeText(err.Error(), maxMessageRunes)}, Warnings: userConfigAdvisories(content)}, nil
	}
	out := configCheck(res)
	out.Warnings = append(out.Warnings, userConfigAdvisories(content)...)
	return out, nil
}

// ApplySingboxUserConfig applies, then enables the slot — the editor's
// order: a refused apply leaves a parked slot parked.
func (l *Local) ApplySingboxUserConfig(context.Context) (mcpsrv.SingboxConfigCheck, error) {
	if l.c.UserConfig == nil {
		return mcpsrv.SingboxConfigCheck{}, errUnavailable("the expert configuration")
	}
	if !l.c.UserConfig.HasDraft(singboxorch.SlotUser) {
		return mcpsrv.SingboxConfigCheck{}, fmt.Errorf("there is no draft to apply; save one with save_singbox_user_config")
	}
	// Read before applying, as the editor does: the commit removes the
	// draft, and its advisories go back with the result. With a draft,
	// LoadEffective returns it.
	draft, _ := l.c.UserConfig.LoadEffective(singboxorch.SlotUser)
	res, err := l.c.UserConfig.ApplyDraft(singboxorch.SlotUser)
	if errors.Is(err, singboxorch.ErrNoDraft) {
		return mcpsrv.SingboxConfigCheck{}, fmt.Errorf("there is no draft to apply")
	}
	if err != nil {
		l.singboxLog.Warn("user-config-apply", "90-user.json", "Apply refused by sing-box check (MCP): "+err.Error())
		return mcpsrv.SingboxConfigCheck{Ok: false, Errors: []string{sanitizeText(err.Error(), maxMessageRunes)}}, nil
	}
	if !res.Ok() {
		l.singboxLog.Warn("user-config-apply", "90-user.json", "Apply refused: validation failed (MCP)")
		return configCheck(res), nil
	}
	if err := l.c.UserConfig.SetEnabled(singboxorch.SlotUser, true); err != nil {
		return mcpsrv.SingboxConfigCheck{}, fmt.Errorf("the configuration is applied, but the slot could not be enabled: %w", err)
	}
	l.singboxLog.Info("user-config-apply", "90-user.json", "User config draft applied (MCP)")
	out := configCheck(res)
	out.Warnings = append(out.Warnings, userConfigAdvisories(draft)...)
	return out, nil
}

func (l *Local) DiscardSingboxUserConfig(context.Context) error {
	if l.c.UserConfig == nil {
		return errUnavailable("the expert configuration")
	}
	if err := l.c.UserConfig.DiscardDraft(singboxorch.SlotUser); err != nil {
		return err
	}
	l.singboxLog.Info("user-config-discard", "90-user.json", "User config draft discarded (MCP)")
	return nil
}
