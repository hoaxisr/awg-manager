// internal/singbox/deviceproxy_config.go
package singbox

import (
	"encoding/json"
	"fmt"
)

// BuildDeviceProxyConfig produces a standalone JSON document containing
// ONLY the device-proxy inbound + selector outbound + route rule for
// the given spec. The result is suitable for a direct write to the
// orchestrator slot 12-deviceproxy.json.
//
// AWG-direct outbounds referenced by the spec (spec.AWGTags) are NOT
// included — they live in 15-awg.json owned by the awgoutbounds
// package, and the spec only references their tags.
//
// If spec.Enabled is false, returns the JSON of an empty config: a
// well-formed but content-free document. Callers that need to fully
// remove the slot's effect should also call orchestrator.SetEnabled
// with false to move the file under disabled/.
//
// The result intentionally omits log/dns/experimental keys: those are
// owned by 00-base.json and must not collide across config.d slots.
func BuildDeviceProxyConfig(spec DeviceProxySpec) ([]byte, error) {
	return BuildDeviceProxyInstancesConfig([]DeviceProxyInstanceSpec{
		{
			ID:          "default",
			Enabled:     spec.Enabled,
			ListenAddr:  spec.ListenAddr,
			Port:        spec.Port,
			Auth:        spec.Auth,
			SelectedTag: spec.SelectedTag,
			AWGTags:     spec.AWGTags,
			SBTags:      spec.SBTags,
		},
	})
}

// BuildDeviceProxyInstancesConfig produces a standalone JSON document
// containing all enabled device-proxy instances.
func BuildDeviceProxyInstancesConfig(specs []DeviceProxyInstanceSpec) ([]byte, error) {
	scratch := NewConfig()

	for _, spec := range specs {
		if err := scratch.EnsureDeviceProxyInstance(spec); err != nil {
			return nil, fmt.Errorf("ensure device proxy instance %q: %w", spec.ID, err)
		}
	}

	cfg := scratch.ExtractDeviceProxy()
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}
	return data, nil
}
