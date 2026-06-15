<script lang="ts">
	import { PageContainer, PageHeader } from '$lib/components/layout';
	import { Tabs } from '$lib/components/ui';

	// FE-spec §3: fixed order + labels of the 9 FakeIP sub-pages. Badges are
	// intentionally omitted here — real chip counters arrive in task 11.2.
	const CHIPS: { id: string; label: string }[] = [
		{ id: 'overview', label: 'Обзор' },
		{ id: 'inbounds', label: 'Inbounds' },
		{ id: 'outbounds', label: 'Outbounds' },
		{ id: 'rulesets', label: 'Rule sets' },
		{ id: 'dns', label: 'DNS' },
		{ id: 'routes', label: 'Маршруты' },
		{ id: 'devices', label: 'Устройства' },
		{ id: 'connections', label: 'Соединения' },
		{ id: 'logs', label: 'Журнал' }
	];

	let activeTab = $state('overview');

	let activeChip = $derived(CHIPS.find((c) => c.id === activeTab) ?? CHIPS[0]);
</script>

<PageContainer>
	<PageHeader title="FakeIP" description="Режим маршрутизации fakeip-tun" />

	<!--
		Hero slot (FE-spec): каждая под-страница показывает hero из config.json
		+ «Инспектор маршрутов». Это реальные компоненты из более поздних задач
		Slice 1E+ — здесь оставляем пустой слот, не строим фейковый просмотрщик.
	-->

	<Tabs
		tabs={CHIPS}
		active={activeTab}
		onchange={(id) => (activeTab = id)}
		urlParam="chip"
		defaultTab="overview"
	/>

	<section class="chip-stub">
		<h2 class="chip-stub-title">{activeChip.label}</h2>
		<p class="chip-stub-note">Раздел в разработке (Slice 1E+)</p>
	</section>
</PageContainer>

<style>
	.chip-stub {
		padding: 2rem;
		border: 1px dashed var(--border);
		border-radius: var(--radius);
		text-align: center;
	}

	.chip-stub-title {
		margin: 0 0 0.5rem;
		font-size: 1rem;
		font-weight: 600;
		color: var(--text-primary);
	}

	.chip-stub-note {
		margin: 0;
		font-size: 0.875rem;
		color: var(--text-muted);
	}
</style>
