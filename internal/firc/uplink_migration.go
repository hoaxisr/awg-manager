package firc

import (
	"context"
	"errors"
)

// Conversion may change IDs or replace a whole slot. Until a verified mapping
// exists, reject it while any managed policy is enabled, before file mutation.
func (o *Operator) guardUplinkMigration(ctx context.Context) (func(), error) {
	hooks := o.DefaultEngineHooks()
	if hooks.AcquireTransition == nil {
		return func() {}, nil
	}
	guard, err := hooks.AcquireTransition(ctx)
	if err != nil {
		return nil, err
	}
	if guard == nil {
		return nil, errors.New("engine transition guard unavailable")
	}
	if err := guard.CheckTarget(""); err != nil {
		guard.Release()
		return nil, err
	}
	return guard.Release, nil
}
