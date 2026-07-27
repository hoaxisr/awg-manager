<script lang="ts">
	// Интеграции: FreeTurn, WDTT, HR Neo. Карточка показывается только для
	// установленной интеграции (freeturn/wdtt — installedVersion, HR Neo —
	// installed). Поля берутся как есть: у HR Neo нет «gvisor» и прочего,
	// чего нет в HydraRouteStatus.
	import { SectionLabel, StatusDot } from '$lib/components/ui';
	import type { StatusDotVariant } from '$lib/components/ui';
	import { freeturnStatus } from '$lib/stores/freeturnStatus';
	import { wdttStatus } from '$lib/stores/wdttStatus';
	import { hydrarouteStatus } from '$lib/stores/hydraroute';

	interface Card {
		href: string;
		title: string;
		state: string;
		detail: string;
		variant: StatusDotVariant;
		alarm: boolean;
	}

	const ft = $derived($freeturnStatus.data);
	const wd = $derived($wdttStatus.data);
	const hr = $derived($hydrarouteStatus.data);

	const cards = $derived.by(() => {
		const list: Card[] = [];

		if (ft?.installedVersion) {
			const instances = [...(ft.clients ?? []), ...(ft.servers ?? [])];
			const running = instances.filter((i) => i.status?.running).length;
			const failed = instances.some((i) => i.status?.lastError);
			const dtls = instances.reduce((n, i) => n + (i.status?.dtlsConnections ?? 0), 0);
			list.push({
				href: '/services/freeturn',
				title: 'FreeTurn',
				state: running > 0 ? 'Работает' : 'Остановлен',
				detail: failed
					? 'ошибка процесса'
					: [dtls > 0 ? `${dtls} DTLS` : '', `v${ft.installedVersion}`].filter(Boolean).join(' · '),
				variant: failed ? 'error' : running > 0 ? 'success' : 'muted',
				alarm: failed,
			});
		}

		if (wd?.installedVersion) {
			const instances = wd.clients ?? [];
			const running = instances.filter((i) => i.status?.running).length;
			const failed = instances.some((i) => i.status?.lastError);
			list.push({
				href: '/services/wdtt',
				title: 'WDTT',
				state: running > 0 ? 'Работает' : 'Остановлен',
				detail: failed ? 'ошибка процесса' : `v${wd.installedVersion}`,
				variant: failed ? 'error' : running > 0 ? 'success' : 'muted',
				alarm: failed,
			});
		}

		if (hr?.installed) {
			const dead = hr.processState === 'dead';
			list.push({
				href: '/services/hrneo',
				title: 'HR Neo',
				state: dead ? 'Процесс умер' : hr.running ? 'Работает' : 'Остановлен',
				detail: [hr.version ? `v${hr.version}` : '', hr.processState ?? ''].filter(Boolean).join(' · '),
				variant: dead ? 'error' : hr.running ? 'success' : 'muted',
				alarm: dead,
			});
		}

		return list;
	});
</script>

{#if cards.length > 0}
	<SectionLabel>Интеграции</SectionLabel>

	<div class="row">
		{#each cards as card (card.href)}
			<a class="card" href={card.href}>
				<span class="head">
					<span class="title">{card.title}</span>
					<StatusDot variant={card.variant} />
				</span>
				<span class="state">{card.state}</span>
				<span class="detail" class:alarm={card.alarm}>{card.detail}</span>
			</a>
		{/each}
	</div>
{/if}

<style>
	.row {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
		gap: 0.75rem;
		margin-top: 0.625rem;
	}

	.card {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
		padding: 0.8125rem 0.9375rem;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		text-decoration: none;
		min-width: 0;
	}

	.card:hover {
		background: var(--color-bg-hover);
		border-color: var(--color-border-hover);
	}

	.head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.5rem;
	}

	.title {
		font-size: 0.8125rem;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.state {
		font-size: 0.75rem;
		color: var(--color-text-secondary);
	}

	.detail {
		font-family: var(--font-mono);
		font-size: 0.6875rem;
		color: var(--color-text-muted);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.detail.alarm { color: var(--color-error); }
</style>
