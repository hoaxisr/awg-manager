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

	function schedule() {
		handle = setTimeout(async () => {
			await tick();
			if (active) schedule();
		}, intervalMs);
	}

	return {
		start() {
			if (active) return;
			active = true;
			schedule();
		},
		stop() {
			active = false;
			if (handle) clearTimeout(handle);
		}
	};
}
