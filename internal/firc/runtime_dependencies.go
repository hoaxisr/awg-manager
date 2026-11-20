package firc

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
)

// RuntimeDependents is deliberately stricter than the best-effort UI config
// loader: unreadable/corrupt routing settings must never authorize removal.
func (o *Operator) RuntimeDependents() ([]string, error) {
	if o.IsRecoveryRequired() {
		return nil, ErrRecoveryRequired
	}
	data, err := os.ReadFile(o.extConfigPath())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ext ExtConfig
	if err := json.Unmarshal(data, &ext); err != nil {
		return nil, fmt.Errorf("read FIRC routing dependencies: %w", err)
	}
	result := slices.Clone(ext.ServerRouting.EnabledServers)
	for _, id := range []string{"awg_server", "wdtt_server", "freeturn_server", "vless_xhttp_server"} {
		if ext.ServerRouting.IsServerEnabled(id) && !slices.Contains(result, id) {
			result = append(result, id)
		}
	}
	slices.Sort(result)
	return slices.Compact(result), nil
}
