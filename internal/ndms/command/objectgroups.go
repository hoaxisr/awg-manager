package command

import (
	"context"
	"fmt"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

type ObjectGroupCommands struct {
	poster  Poster
	save    *SaveCoordinator
	queries *query.Queries
}

func NewObjectGroupCommands(p Poster, s *SaveCoordinator, q *query.Queries) *ObjectGroupCommands {
	return &ObjectGroupCommands{poster: p, save: s, queries: q}
}

// DeleteGroups removes multiple FQDN groups in one POST.
func (c *ObjectGroupCommands) DeleteGroups(ctx context.Context, names []string) error {
	if len(names) == 0 {
		return nil
	}
	fqdn := map[string]any{}
	for _, n := range names {
		fqdn[n] = map[string]any{"no": true}
	}
	payload := map[string]any{
		"object-group": map[string]any{"fqdn": fqdn},
	}
	return postMutation(ctx, c.poster, c.save, payload, "delete fqdn groups",
		c.queries.ObjectGroups.InvalidateAll,
		c.queries.RunningConfig.InvalidateAll)
}

// FQDNGroupMutation describes addresses to add or remove from one group.
type FQDNGroupMutation struct {
	Name           string
	AddIncludes    []string
	RemoveIncludes []string
	AddExcludes    []string
	RemoveExcludes []string
}

// UpsertGroup applies additive/subtractive changes to one FQDN group.
func (c *ObjectGroupCommands) UpsertGroup(ctx context.Context, m FQDNGroupMutation) error {
	if m.Name == "" {
		return fmt.Errorf("upsert fqdn group: empty name")
	}
	if len(m.AddIncludes)+len(m.RemoveIncludes)+len(m.AddExcludes)+len(m.RemoveExcludes) == 0 {
		return nil
	}
	group := map[string]any{}
	if include := buildEntries(m.AddIncludes, m.RemoveIncludes); len(include) > 0 {
		group["include"] = include
	}
	if exclude := buildEntries(m.AddExcludes, m.RemoveExcludes); len(exclude) > 0 {
		group["exclude"] = exclude
	}
	payload := map[string]any{
		"object-group": map[string]any{
			"fqdn": map[string]any{m.Name: group},
		},
	}
	return postMutation(ctx, c.poster, c.save, payload, "upsert fqdn group "+m.Name,
		c.queries.ObjectGroups.InvalidateAll,
		c.queries.RunningConfig.InvalidateAll)
}

// buildEntries builds the include/exclude array: removes first (no: true),
// then adds.
func buildEntries(adds, removes []string) []any {
	entries := make([]any, 0, len(adds)+len(removes))
	for _, addr := range removes {
		entries = append(entries, map[string]any{"address": addr, "no": true})
	}
	for _, addr := range adds {
		entries = append(entries, map[string]any{"address": addr})
	}
	return entries
}
