/**
 * mcpKeys — polling store for the MCP bearer keys shown in Settings.
 *
 * `resource:invalidated` with resource "mcpKeys" (backend ResourceMcpKeys)
 * refetches after every create/revoke, so a second tab sees the change.
 */
import { api } from '$lib/api/client';
import { createPollingStore, type PollingStore } from './polling';
import { registerStore } from './storeRegistry';
import type { McpKey } from '$lib/types';

export const mcpKeys: PollingStore<McpKey[]> = createPollingStore<McpKey[]>(
	() => api.getMcpKeys(),
	{ staleTime: 5_000, pollInterval: 60_000 }
);

registerStore('mcpKeys', mcpKeys);
