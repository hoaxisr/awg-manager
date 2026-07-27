<script lang="ts">
	// Движок sing-box: статус перехвата, режим, счётчики правил и трафика.
	// Источники: singboxRouter.status/settings (НЕ polling — страница делает
	// reloadStatus()+reloadSettings() при маунте, дальше SSE), singboxMemory
	// (SSE, Go-heap движка, не RSS), singboxTrafficTotals (SSE, кумулятив до
	// рестарта движка → подпись «за сессию»).
	import { ChevronRight, TriangleAlert, Waypoints } from 'lucide-svelte';
	import { StatusDot } from '$lib/components/ui';
	import { singboxRouter } from '$lib/stores/singboxRouter';
	import { singboxMemory } from '$lib/stores/singboxMemory';
	import { singboxTrafficTotals } from '$lib/stores/singbox';
	import { formatBytes } from '$lib/utils/format';
	import { engineIssueText } from './overviewRollup';

	const DASH = '—';

	const routerStatus = singboxRouter.status;
	const routerSettings = singboxRouter.settings;
	const status = $derived($routerStatus);
	const settings = $derived($routerSettings);

	const badge = $derived.by(() => {
		if (!status) return { label: 'нет данных', variant: 'muted' as const };
		if (!status.installed) return { label: 'Не установлена', variant: 'muted' as const };
		if (status.active) return { label: 'Активна', variant: 'success' as const };
		if (status.enabled) return { label: 'Сбой', variant: 'error' as const };
		return { label: 'Выключена', variant: 'muted' as const };
	});

	// Режим живёт только в settings; у неустановленной маршрутизации он не
	// значит ничего — чип прячем.
	const mode = $derived(
		settings && status?.installed ? (settings.routingMode === 'fakeip-tun' ? 'FakeIP' : 'TProxy') : '',
	);

	const totals = $derived($singboxTrafficTotals);
	const totalBytes = $derived(totals.downloadBytes + totals.uploadBytes);
	const issue = $derived(engineIssueText(status));
</script>

<div class="engine" class:has-issue={!!issue}>
	<div class="head">
		<span class="icon"><Waypoints size={17} aria-hidden="true" /></span>
		<span class="titles">
			<span class="title">Sing-box · маршрутизация</span>
			<span class="subtitle">основной инструмент маршрутизации трафика</span>
		</span>
		<span class="badge badge-{badge.variant}">
			<StatusDot variant={badge.variant} size="sm" />
			{badge.label}
		</span>
		{#if mode}
			<span class="chip">{mode}</span>
		{/if}
		<a class="link" href="/sb/routing">Открыть <ChevronRight size={13} aria-hidden="true" /></a>
	</div>

	{#if !status}
		<p class="empty">Нет данных о движке маршрутизации.</p>
	{:else if !status.installed}
		<p class="empty">Маршрутизация sing-box не установлена — раздел «Маршрутизация».</p>
	{:else}
		<div class="stats">
			<div class="stat">
				<span class="value">{status.ruleCount ?? DASH}</span>
				<span class="label">Правила</span>
			</div>
			<div class="stat">
				<span class="value">{status.ruleSetCount ?? DASH}</span>
				<span class="label">Rule-sets</span>
			</div>
			<div class="stat">
				<span class="value">{status.deviceCount ?? DASH}</span>
				<span class="label">{status.deviceMode === 'all' ? 'Устройства · весь LAN' : 'Устройства'}</span>
			</div>
			<div class="stat">
				<span class="value" class:muted={$singboxMemory === 0}>
					{$singboxMemory > 0 ? formatBytes($singboxMemory, 0) : DASH}
				</span>
				<span class="label">heap движка{$singboxMemory > 0 ? '' : status.active ? ' · нет данных' : ' · движок остановлен'}</span>
			</div>
			<div class="stat">
				<span class="value" class:muted={totalBytes === 0}>
					{totalBytes > 0 ? formatBytes(totalBytes, 1) : DASH}
				</span>
				<span class="label">
					{#if totalBytes > 0}
						↓{formatBytes(totals.downloadBytes, 1)} · ↑{formatBytes(totals.uploadBytes, 1)} · за сессию
					{:else}
						трафик за сессию
					{/if}
				</span>
			</div>
			<div class="stat">
				<span class="value accent" class:muted={!status.final}>{status.final || DASH}</span>
				<span class="label">Финальный узел</span>
			</div>
		</div>

		{#if issue}
			<p class="issue"><TriangleAlert size={14} aria-hidden="true" />{issue}</p>
		{/if}
	{/if}
</div>

<style>
	.engine {
		display: flex;
		flex-direction: column;
		padding: 0.9375rem 1rem;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		min-width: 0;
	}

	.engine.has-issue {
		border-color: var(--color-warning-border);
	}

	.head {
		display: flex;
		align-items: center;
		gap: 0.625rem;
		flex-wrap: wrap;
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
		min-width: 170px;
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

	.badge {
		display: inline-flex;
		align-items: center;
		gap: 0.375rem;
		flex: none;
		padding: 0.25rem 0.625rem;
		border-radius: var(--radius-sm);
		font-size: 0.6875rem;
		font-weight: 500;
	}

	.badge-success { background: var(--color-success-tint); color: var(--color-success); }
	.badge-error { background: var(--color-error-tint); color: var(--color-error); }
	.badge-muted { background: var(--color-muted-tint); color: var(--color-text-secondary); }

	.chip {
		flex: none;
		padding: 0.1875rem 0.625rem;
		font-size: 0.75rem;
		font-weight: 500;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		background: var(--color-accent-tint);
		color: var(--color-accent);
	}

	.link {
		display: inline-flex;
		align-items: center;
		gap: 0.25rem;
		flex: none;
		font-size: 0.75rem;
		font-weight: 500;
		color: var(--color-accent);
		text-decoration: none;
	}

	.link:hover { text-decoration: underline; }

	.stats {
		display: grid;
		grid-template-columns: repeat(3, minmax(0, 1fr));
		gap: 0.8125rem 1.125rem;
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

	.empty {
		margin: 0.875rem 0 0;
		padding-top: 0.8125rem;
		border-top: 1px solid var(--color-border);
		font-size: 0.8125rem;
		color: var(--color-text-muted);
	}

	.issue {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		margin: 0.75rem 0 0;
		padding: 0.5rem 0.6875rem;
		border-radius: var(--radius-sm);
		background: var(--color-warning-tint);
		color: var(--color-warning);
		font-size: 0.75rem;
	}

	@media (max-width: 700px) {
		.stats {
			grid-template-columns: repeat(2, minmax(0, 1fr));
		}
	}
</style>
