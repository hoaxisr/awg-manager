import { m } from '$lib/i18n';
import { writable } from 'svelte/store';
import type { TunnelReferencedError } from '$lib/types';

export type OutboundReferencedState = {
	details: TunnelReferencedError;
	name: string;
	entityLabel: string;
} | null;

function createOutboundReferencedStore() {
	const { subscribe, set } = writable<OutboundReferencedState>(null);

	return {
		subscribe,
		show(details: TunnelReferencedError, name: string, entityLabel: string = m.tunnels_referenced_entity_default()) {
			set({ details, name, entityLabel });
		},
		close() {
			set(null);
		},
	};
}

export const outboundReferenced = createOutboundReferencedStore();
