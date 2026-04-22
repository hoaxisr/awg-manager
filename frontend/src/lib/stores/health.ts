import { writable, type Readable } from 'svelte/store';

export type HealthState = {
	online: boolean;
	lastCheckAt: number;
	consecutiveFailures: number;
};

export interface HealthMonitor extends Readable<HealthState> {
	start: () => void;
	stop: () => void;
}

const POLL_INTERVAL_MS = 5_000;
const OFFLINE_THRESHOLD = 2; // consecutive failures before flipping online=false

function createHealthMonitor(): HealthMonitor {
	const state = writable<HealthState>({
		online: true,
		lastCheckAt: 0,
		consecutiveFailures: 0,
	});

	let timer: ReturnType<typeof setInterval> | null = null;

	async function tick() {
		try {
			const res = await fetch('/api/health', { method: 'GET' });
			if (!res.ok) throw new Error(`health ${res.status}`);
			state.update(() => ({
				online: true,
				lastCheckAt: Date.now(),
				consecutiveFailures: 0,
			}));
		} catch {
			state.update(s => {
				const fails = s.consecutiveFailures + 1;
				return {
					...s,
					lastCheckAt: Date.now(),
					consecutiveFailures: fails,
					online: fails < OFFLINE_THRESHOLD,
				};
			});
		}
	}

	return {
		subscribe: state.subscribe,
		start() {
			if (timer !== null) return;
			void tick(); // immediate first check
			timer = setInterval(tick, POLL_INTERVAL_MS);
		},
		stop() {
			if (timer !== null) {
				clearInterval(timer);
				timer = null;
			}
		},
	};
}

export const healthMonitor = createHealthMonitor();
