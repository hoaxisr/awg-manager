/**
 * Self-rescheduling poll: schedules the next tick only after the current one
 * settles, so a slow router never sees overlapping requests (unlike setInterval).
 *
 * The first tick fires after `intervalMs`, not immediately — callers do the
 * initial load themselves, then `start()`. Holds no reactive state, only plain
 * closure variables, so it is safe to share between Svelte components.
 */
export interface SelfReschedulingPoll {
	start(): void;
	stop(): void;
}

export function createSelfReschedulingPoll(
	tick: () => void | Promise<void>,
	intervalMs = 2000
): SelfReschedulingPoll {
	let handle: ReturnType<typeof setTimeout> | undefined;
	let active = false;
	let disposed = false;

	function schedule() {
		handle = setTimeout(async () => {
			await tick();
			if (active) schedule();
		}, intervalMs);
	}

	return {
		start() {
			// `disposed` guard: stop() may fire before start() when the component is
			// destroyed mid initial load (onMount awaits before start()). Without it,
			// a late start() would revive the poll into an orphaned timer loop.
			if (active || disposed) return;
			active = true;
			schedule();
		},
		stop() {
			active = false;
			disposed = true;
			if (handle) clearTimeout(handle);
		}
	};
}
