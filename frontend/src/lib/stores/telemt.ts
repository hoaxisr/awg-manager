/**
 * telemt — polling store for Telegram MTProto Proxy (telemt).
 *
 * `resource:invalidated` with resource "telemt.status" (backend ResourceTelemtStatus)
 * triggers an immediate refetch through the store registry after every mutation.
 */
import { api } from '$lib/api/client';
import { createPollingStore, type PollingStore } from './polling';
import { registerStore } from './storeRegistry';
import type { TelemtStatus } from '$lib/types';

export const telemtStatus: PollingStore<TelemtStatus> = createPollingStore<TelemtStatus>(
	() => api.telemtStatus(),
	{ staleTime: 5_000, pollInterval: 0 }
);

registerStore('telemt.status', telemtStatus);
