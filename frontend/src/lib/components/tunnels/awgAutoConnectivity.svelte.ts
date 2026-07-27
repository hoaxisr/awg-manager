/**
 * Авто-проверка связности AWG-туннелей при входе на поверхность.
 *
 * Вынесено из routes/+page.svelte при расщеплении навигации v3: одна и та же
 * механика нужна странице /awg/tunnels и дашборду главной. Ключ поверхности —
 * само монтирование потребителя (lastAutoCheckKey стартует пустым), поэтому
 * отдельный entry-nonce не нужен: одновременно смонтирована ровно одна
 * поверхность, и запросы не задваиваются.
 */
import { untrack } from 'svelte';
import { tunnels } from '$lib/stores/tunnels';
import { api } from '$lib/api/client';
import type { TunnelListItem } from '$lib/types';
import type { TunnelRenderMode } from '$lib/constants/singboxLayout';

interface AwgAutoConnectivityOptions {
	awgList: () => TunnelListItem[];
	/** Текущий режим рендера списка — табличный проверяет туннели сам. */
	renderMode: () => TunnelRenderMode;
	loading: () => boolean;
	/** Поверхность показывает AWG-туннели (напр. дашборд включён). */
	enabled?: () => boolean;
}

export interface AwgAutoConnectivity {
	/** Растёт при входе на поверхность — карточки перепроверяют связность. */
	readonly nonce: number;
}

function checkTargets(list: TunnelListItem[]): TunnelListItem[] {
	return list.filter(
		(t) =>
			t.enabled &&
			(t.status === 'running' || t.status === 'broken') &&
			(t.connectivityCheck?.method ?? 'http') !== 'disabled',
	);
}

export function createAwgAutoConnectivity(opts: AwgAutoConnectivityOptions): AwgAutoConnectivity {
	let nonce = $state(0);
	let lastAutoCheckKey = '';

	$effect(() => {
		if (opts.enabled && !opts.enabled()) return;
		if (opts.loading()) return;

		const ids = checkTargets(opts.awgList())
			.map((t) => t.id)
			.sort()
			.join(',');
		if (!ids || ids === lastAutoCheckKey) return;
		lastAutoCheckKey = ids;
		nonce += 1;
	});

	// Только в табличном рендере не рендерятся TunnelCard — там срабатывает autoConnectivity.
	// Иначе connectivityMap не заполняется и подстрока статуса залипает на «Проверка…».
	// В list-card (мобильный «список» и сплошной дашборд) карточки сами
	// проверяются по тому же nonce — дублировать запросы со страницы нельзя.
	$effect(() => {
		const mode = opts.renderMode();
		const current = nonce;
		if (mode !== 'table' || opts.loading() || current <= 0) return;

		const targets = untrack(() => checkTargets(opts.awgList()));
		if (targets.length === 0) return;

		const timers: ReturnType<typeof setTimeout>[] = [];
		for (let i = 0; i < targets.length; i++) {
			const id = targets[i].id;
			timers.push(
				setTimeout(() => {
					void api
						.checkConnectivity(id)
						.then((result) => {
							tunnels.updateConnectivity(id, result.connected, result.latency ?? null);
						})
						.catch(() => {
							tunnels.updateConnectivity(id, false, null);
						});
				}, i * 180),
			);
		}
		return () => {
			for (const t of timers) clearTimeout(t);
		};
	});

	return {
		get nonce() { return nonce; },
	};
}
