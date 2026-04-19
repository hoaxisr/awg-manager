package command

import (
	"context"
	"fmt"
)

// postMutation is the common post-then-invalidate pattern shared by most
// command methods: POST a single payload, wrap a transport error with
// opDesc, Request a save on success, then run each cache invalidator.
//
// opDesc is used as an error prefix ("opDesc: <err>") — short present-tense
// phrasing such as "create policy foo" reads best in logs.
//
// Invalidators are plain closures so callers can mix per-key
// (`c.queries.Interfaces.Invalidate(name)`) and whole-store
// (`c.queries.RunningConfig.InvalidateAll`) cache drops in the same call.
// InvalidateAll without parens works as a method value.
func postMutation(
	ctx context.Context,
	poster Poster,
	save *SaveCoordinator,
	payload any,
	opDesc string,
	invalidators ...func(),
) error {
	if _, err := poster.Post(ctx, payload); err != nil {
		return fmt.Errorf("%s: %w", opDesc, err)
	}
	save.Request()
	for _, inv := range invalidators {
		inv()
	}
	return nil
}
