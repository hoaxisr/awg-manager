// Pure split of the router outbound catalog into the two sections the
// Outbounds chip renders (FE-spec §5.3):
//   - atomic   — direct outbounds (single endpoint, no members)
//   - composite — selector / urltest / loadbalance groups (have members,
//                 an active member and a group-test)
//
// `loadbalance` is kept on the composite side (not hidden): it is a valid
// composite type the core can carry, it shows up in the shared
// COMPOSITE_OUTBOUND_TYPES set and the overview's activeComposites view, so
// the catalog stays honest about what exists in the config. The reused edit
// modal narrows it to urltest on edit — that one-way migration is owned by
// the sb-router modal, not this view.

import type { SingboxRouterOutbound } from '$lib/types';
import { COMPOSITE_OUTBOUND_TYPES } from '$lib/components/sb-router/compositeOutboundDisplay';

export interface PartitionedOutbounds {
	atomic: SingboxRouterOutbound[];
	composite: SingboxRouterOutbound[];
}

export function partitionOutbounds(
	outbounds: SingboxRouterOutbound[],
): PartitionedOutbounds {
	const atomic: SingboxRouterOutbound[] = [];
	const composite: SingboxRouterOutbound[] = [];
	for (const o of outbounds) {
		if (COMPOSITE_OUTBOUND_TYPES.has(o.type)) composite.push(o);
		else atomic.push(o);
	}
	return { atomic, composite };
}
