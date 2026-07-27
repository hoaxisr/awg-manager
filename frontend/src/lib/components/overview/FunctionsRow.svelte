<script lang="ts">
	// Настроенные функции: подписки sing-box и VPN для устройств
	// (per-client policy routing). Карточка показывается, только если функция
	// вообще настроена — пустых заглушек на «Обзоре» нет.
	//
	// Активный узел подписки: selector — Subscription.activeMember, urltest —
	// живой указатель из subscriptionLiveActives (общий poll 5s). Показываем
	// только когда включённая подписка одна: у каждой свой активный узел.
	// «N устройств · M туннелей» — у каждого клиента свой туннель, единого
	// «активного туннеля» у функции нет.
	import { ChevronRight, Layers, Smartphone } from 'lucide-svelte';
	import { SectionLabel, StatusDot } from '$lib/components/ui';
	import { subscriptionsStore } from '$lib/stores/subscriptions';
	import { subscriptionLiveActives } from '$lib/stores/subscriptionLiveActives';
	import { clientRoutesStore } from '$lib/stores/routing';

	const DASH = '—';

	const subs = $derived($subscriptionsStore.data ?? []);
	const enabledSubs = $derived(subs.filter((s) => s.enabled));
	const subsFailed = $derived(enabledSubs.filter((s) => s.lastError).length);
	const subsSynced = $derived(
		enabledSubs.length > 0 && subsFailed === 0 && enabledSubs.every((s) => s.lastFetched),
	);

	const activeNode = $derived.by(() => {
		if (enabledSubs.length !== 1) return '';
		const sub = enabledSubs[0];
		if (sub.mode === 'urltest') return $subscriptionLiveActives[sub.id] || sub.activeMember || '';
		return sub.activeMember || '';
	});

	const routes = $derived($clientRoutesStore.data ?? []);
	const enabledRoutes = $derived(routes.filter((r) => r.enabled));
	const routeTunnels = $derived(new Set(enabledRoutes.map((r) => r.tunnelId)).size);

	const showSubs = $derived(subs.length > 0);
	const showRoutes = $derived(routes.length > 0);
</script>

{#if showSubs || showRoutes}
	<SectionLabel>Функции</SectionLabel>

	<div class="row" class:single={!showSubs || !showRoutes}>
		{#if showSubs}
			<div class="card">
				<div class="head">
					<span class="icon"><Layers size={17} aria-hidden="true" /></span>
					<span class="titles">
						<span class="title">Подписки</span>
						<span class="subtitle">провайдеры Sing-box</span>
					</span>
					<span class="state" class:alarm={subsFailed > 0}>
						<StatusDot
							variant={subsFailed > 0 ? 'error' : subsSynced ? 'success' : 'muted'}
							size="sm"
						/>
						{subsFailed > 0 ? 'ошибка' : subsSynced ? 'синхр.' : 'не обновлялись'}
					</span>
				</div>
				<div class="stats">
					<div class="stat">
						<span class="value">{subs.length}</span>
						<span class="label">Подписки</span>
					</div>
					{#if enabledSubs.length === 1}
						<div class="stat">
							<span class="value accent" class:muted={!activeNode}>{activeNode || DASH}</span>
							<span class="label">Активный узел</span>
						</div>
					{:else}
						<!-- У каждой подписки свой активный узел — единого «активного» нет. -->
						<div class="stat">
							<span class="value">{enabledSubs.length}</span>
							<span class="label">Включены</span>
						</div>
					{/if}
				</div>
				<a class="link" href="/sb/subscriptions">
					Открыть подписки <ChevronRight size={13} aria-hidden="true" />
				</a>
			</div>
		{/if}

		{#if showRoutes}
			<div class="card">
				<div class="head">
					<span class="icon"><Smartphone size={17} aria-hidden="true" /></span>
					<span class="titles">
						<span class="title">VPN для устройств</span>
						<span class="subtitle">per-device выход</span>
					</span>
					<span class="state">
						<StatusDot variant={enabledRoutes.length > 0 ? 'success' : 'muted'} size="sm" />
						{enabledRoutes.length > 0 ? 'активен' : 'выключен'}
					</span>
				</div>
				<div class="stats">
					<div class="stat">
						<span class="value">{enabledRoutes.length}</span>
						<span class="label">Устройства</span>
					</div>
					<div class="stat">
						<span class="value">{routeTunnels}</span>
						<span class="label">Туннели</span>
					</div>
				</div>
				<a class="link" href="/router/device-vpn">
					Настроить <ChevronRight size={13} aria-hidden="true" />
				</a>
			</div>
		{/if}
	</div>
{/if}

<style>
	.row {
		display: grid;
		grid-template-columns: repeat(2, minmax(0, 1fr));
		gap: 0.75rem;
		margin-top: 0.625rem;
	}

	.row.single {
		grid-template-columns: minmax(0, 1fr);
	}

	.card {
		display: flex;
		flex-direction: column;
		padding: 0.9375rem 1rem;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		min-width: 0;
	}

	.head {
		display: flex;
		align-items: center;
		gap: 0.625rem;
	}

	.icon {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: 32px;
		height: 32px;
		flex: none;
		border-radius: var(--radius-sm);
		background: var(--color-accent-tint);
		color: var(--color-accent);
	}

	.titles {
		display: flex;
		flex-direction: column;
		flex: 1;
		min-width: 0;
	}

	.title {
		font-size: 0.875rem;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.subtitle {
		font-size: 0.6875rem;
		color: var(--color-text-muted);
	}

	.state {
		display: inline-flex;
		align-items: center;
		gap: 0.375rem;
		flex: none;
		font-size: 0.6875rem;
		color: var(--color-success);
	}

	.state.alarm { color: var(--color-error); }

	.stats {
		display: flex;
		gap: 1.25rem;
		margin-top: 0.875rem;
		padding-top: 0.8125rem;
		border-top: 1px solid var(--color-border);
	}

	.stat {
		display: flex;
		flex-direction: column;
		gap: 0.125rem;
		min-width: 0;
	}

	.value {
		font-family: var(--font-mono);
		font-size: 1rem;
		font-weight: 600;
		color: var(--color-text-primary);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.value.accent { color: var(--color-accent); }
	.value.muted { color: var(--color-text-muted); }

	.label {
		font-size: 0.6875rem;
		color: var(--color-text-muted);
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.link {
		display: inline-flex;
		align-items: center;
		gap: 0.25rem;
		margin-top: auto;
		padding-top: 0.8125rem;
		font-size: 0.75rem;
		font-weight: 500;
		color: var(--color-accent);
		text-decoration: none;
	}

	.link:hover { text-decoration: underline; }

	@media (max-width: 700px) {
		.row {
			grid-template-columns: minmax(0, 1fr);
		}
	}
</style>
