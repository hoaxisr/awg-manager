package ops

import "context"

// Wired once before serving requests. Reconfiguration of an owned live device
// is quiesced before setconf so clearing its mark cannot leak outer packets.
func (o *OperatorOS5Impl) SetUplinkLifecycle(owns func(string) bool, beforeUp func(context.Context, string) error) {
	o.uplinkOwns = owns
	o.uplinkBeforeUp = beforeUp
}
func (o *OperatorOS5Impl) quiesceUplink(ctx context.Context, id, iface string) error {
	if o.uplinkOwns == nil || !o.uplinkOwns(id) {
		return nil
	}
	_, err := o.ipRun(ctx, "/opt/sbin/ip", "link", "set", "down", "dev", iface)
	return err
}
func (o *OperatorOS5Impl) restoreUplink(ctx context.Context, id string) error {
	if o.uplinkBeforeUp == nil {
		return nil
	}
	return o.uplinkBeforeUp(ctx, id)
}
