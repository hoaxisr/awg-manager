import { derived } from 'svelte/store';
import { healthMonitor } from './health';

// serverOnline reflects the backend's reachability. Driven by the
// /api/health 5s poller (healthMonitor), independent of SSE state —
// an SSE disconnect alone no longer triggers the full-screen offline
// overlay. That only happens when 2+ consecutive health checks fail.
export const serverOnline = derived(healthMonitor, $h => $h.online);
