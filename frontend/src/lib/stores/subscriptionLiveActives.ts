import { readable } from 'svelte/store';
import { api } from '$lib/api/client';
import { subscriptionsStore } from './subscriptions';

// Тот же интервал, что и на детальной странице подписки: опрашиваем Clash
// про живой указатель «сейчас».
const URLTEST_POLL_MS = 5000;

/**
 * Живой активный член urltest-подписок: `{ subscriptionId: memberTag }`.
 *
 * Reference-counted, как polling-сторы проекта (см. stores/polling.ts):
 * первая подписка запускает опрос, последняя — останавливает. Потребители —
 * страница /sb/subscriptions и дашборд главной; общий стор не даёт им
 * задвоить поллинг при переходе между поверхностями.
 */
export const subscriptionLiveActives = readable<Record<string, string>>({}, (set) => {
	let ids: string[] = [];
	let idsKey = '';
	let cancelled = false;

	const tick = async (): Promise<void> => {
		const targets = ids;
		if (targets.length === 0) return;
		try {
			const results = await Promise.all(
				targets.map((id) =>
					api
						.getSubscriptionActiveNow(id)
						.then((r) => [id, r.now] as const)
						.catch(() => [id, ''] as const),
				),
			);
			if (cancelled) return;
			const next: Record<string, string> = {};
			for (const [id, now] of results) {
				if (now) next[id] = now;
			}
			set(next);
		} catch {
			/* ignore — keep last known */
		}
	};

	// Состав urltest-подписок приезжает из polling-стора списка; смена состава
	// перезапускает опрос сразу, как это делал $effect на главной.
	const unsubList = subscriptionsStore.subscribe((state) => {
		const next = (state.data ?? [])
			.filter((s) => s.enabled && s.mode === 'urltest')
			.map((s) => s.id)
			.sort();
		const nextKey = next.join(',');
		if (nextKey === idsKey) return;
		idsKey = nextKey;
		ids = next;
		if (ids.length === 0) {
			set({});
			return;
		}
		void tick();
	});

	const handle = setInterval(() => void tick(), URLTEST_POLL_MS);
	return () => {
		cancelled = true;
		clearInterval(handle);
		unsubList();
	};
});
