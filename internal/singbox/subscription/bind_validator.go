package subscription

import "context"

// BindInterfaceValidator checks egress interface names against the router's
// bindable-interface catalog. Optional on Service — when nil, bind is not
// validated (tests / legacy bootstrap).
type BindInterfaceValidator interface {
	ValidateBindInterface(ctx context.Context, name string) error
}

func validateBindInterfaceOptional(ctx context.Context, v BindInterfaceValidator, name string) error {
	if name == "" || v == nil {
		return nil
	}
	return v.ValidateBindInterface(ctx, name)
}
