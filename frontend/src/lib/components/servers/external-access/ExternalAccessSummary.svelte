<script lang="ts">
	import { onMount } from 'svelte';
	import { api } from '$lib/api/client';
	import { Button } from '$lib/components/ui';
	import type {
		XrayConfig,
		XrayStatus,
		XHTTPTuning,
		TgWebProxyConfig,
		TgWebProxyStatus,
		CDNDispatcherConfig,
	} from '$lib/types';
	import { EXTERNAL_ACCESS_LABELS } from './connectionLabels';
	import {
		buildExternalAccessSummary,
		computeEffectiveVerifyKind,
	} from './externalAccessModel';
	import ExternalAccessWizard from './ExternalAccessWizard.svelte';
	import ConnectionChecks from '../wizard/ConnectionChecks.svelte';
	import {
		Sparkles,
		AlertTriangle,
		CheckCircle2,
		XCircle,
		ArrowRight,
		Network,
		RefreshCw,
		Shield,
		Send,
	} from 'lucide-svelte';

	interface Props {
		activeKind: 'xray' | 'tgwebproxy' | 'dispatcher';
		onSelectService: (id: string) => void;
		onRefreshParent?: () => void;
	}

	let { activeKind, onSelectService, onRefreshParent }: Props = $props();

	let xrayConfig = $state<XrayConfig | null>(null);
	let xrayStatus = $state<XrayStatus | null>(null);
	let xrayTuning = $state<XHTTPTuning | null>(null);
	let tgConfig = $state<TgWebProxyConfig | null>(null);
	let tgStatus = $state<TgWebProxyStatus | null>(null);
	let dispatcherConfig = $state<CDNDispatcherConfig | null>(null);
	let loading = $state(true);
	let wizardOpen = $state(false);
	let showRoutesTable = $state(false);

	const summary = $derived(
		buildExternalAccessSummary({
			xrayConfig,
			xrayStatus,
			xrayTuning,
			tgConfig,
			tgStatus,
			dispatcherConfig,
		}),
	);

	const effectiveVerifyKind = $derived(computeEffectiveVerifyKind(summary));

	onMount(() => {
		void loadOverview();
	});

	export async function loadOverview() {
		loading = true;
		try {
			const [xc, xs, xt, tc, ts, dc] = await Promise.all([
				api.getXrayServerConfig().catch(() => null),
				api.getXrayServerStatus().catch(() => null),
				api.getXrayTuning().catch(() => null),
				api.getTgWebProxyConfig().catch(() => null),
				api.getTgWebProxyStatus().catch(() => null),
				api.getDispatcherConfig().catch(() => null),
			]);
			xrayConfig = xc;
			xrayStatus = xs;
			xrayTuning = xt;
			tgConfig = tc;
			tgStatus = ts;
			dispatcherConfig = dc;
		} finally {
			loading = false;
		}
	}

	function handleWizardApplied() {
		void loadOverview();
		onRefreshParent?.();
	}
</script>

<section class="summary-card" aria-label="Сводка внешнего доступа">
	<div class="summary-header">
		<div class="header-left">
			<div class="title-row">
				<h2 class="title">{EXTERNAL_ACCESS_LABELS.sectionTitle}</h2>
				<span class="port-pill font-mono">:9009 → VLESS / Telegram</span>
			</div>
			<p class="subtitle">{EXTERNAL_ACCESS_LABELS.sectionSubtitle}</p>
		</div>

		<div class="header-actions">
			<button
				type="button"
				class="refresh-btn"
				title="Обновить сводку внешнего доступа"
				onclick={loadOverview}
				disabled={loading}
			>
				<RefreshCw size={14} class={loading ? 'spin' : ''} />
			</button>
			<Button variant="primary" size="sm" onclick={() => (wizardOpen = true)}>
				<Sparkles size={14} class="mr-1" />
				{EXTERNAL_ACCESS_LABELS.wizardButton}
			</Button>
		</div>
	</div>

	<div class="services-grid">
		<!-- VLESS (XHTTP) Summary Block -->
		<div class="service-box" class:active-service={activeKind === 'xray'}>
			<div class="service-top">
				<div class="service-title-wrap">
					<Shield size={16} class="text-sky-400" />
					<span class="service-name">{EXTERNAL_ACCESS_LABELS.vlessTitle}</span>
					{#if !summary.vless.installed || summary.vless.unavailableReason}
						<span class="status-badge badge-warn">{EXTERNAL_ACCESS_LABELS.statusNeedsCheck}</span>
					{:else}
						<span class="status-badge {summary.vless.running ? 'badge-ok' : 'badge-off'}">
							{summary.vless.running ? EXTERNAL_ACCESS_LABELS.statusRunning : EXTERNAL_ACCESS_LABELS.statusStopped}
						</span>
					{/if}
				</div>
				<button
					type="button"
					class="nav-link"
					onclick={() => onSelectService('__xray_server__')}
				>
					<span>Открыть</span>
					<ArrowRight size={13} />
				</button>
			</div>

			{#if !summary.vless.installed || summary.vless.unavailableReason}
				<div class="warning-line">
					<AlertTriangle size={14} class="text-amber-400 shrink-0" />
					<span>{summary.vless.unavailableReason || 'C-демон awgm-xhttpd недоступен'}</span>
				</div>
			{/if}

			<div class="service-meta-grid">
				<div class="meta-item">
					<span class="meta-label">Домены ({summary.vless.domains.length || 0}):</span>
					<span class="meta-val font-mono">
						{summary.vless.domains.length > 0 ? summary.vless.domains.join(', ') : 'Не указан'}
					</span>
				</div>
				<div class="meta-item">
					<span class="meta-label">Путь и транспорт:</span>
					<span class="meta-val font-mono">{summary.vless.path} · {summary.vless.presetLabel}</span>
				</div>
				<div class="meta-item">
					<span class="meta-label">Выход трафика:</span>
					<span class="meta-val" class:legacy-warn={summary.vless.legacyEgress}>
						{#if summary.vless.legacyEgress}
							<AlertTriangle size={12} class="inline-icon" />
						{/if}
						{summary.vless.egressLabel}
					</span>
				</div>
				<div class="meta-item">
					<span class="meta-label">Клиенты:</span>
					<span class="meta-val">{summary.vless.activeClientCount} активных из {summary.vless.clientCount}</span>
				</div>
			</div>
		</div>

		<!-- Telegram Summary Block -->
		<div class="service-box" class:active-service={activeKind === 'tgwebproxy'}>
			<div class="service-top">
				<div class="service-title-wrap">
					<Send size={16} class="text-sky-400" />
					<span class="service-name">{EXTERNAL_ACCESS_LABELS.telegramTitle}</span>
					{#if summary.telegram.overallState === 'running'}
						<span class="status-badge badge-ok">{EXTERNAL_ACCESS_LABELS.statusRunning}</span>
					{:else if summary.telegram.overallState === 'partial'}
						<span class="status-badge badge-warn">{EXTERNAL_ACCESS_LABELS.statusPartial}</span>
					{:else}
						<span class="status-badge badge-off">{EXTERNAL_ACCESS_LABELS.statusStopped}</span>
					{/if}
				</div>
				<button
					type="button"
					class="nav-link"
					onclick={() => onSelectService('__tg_webproxy__')}
				>
					<span>Открыть</span>
					<ArrowRight size={13} />
				</button>
			</div>

			<div class="tg-split-badges">
				<span class="split-pill {summary.telegram.mtprotoOnline ? 'pill-on' : 'pill-off'}">
					{#if summary.telegram.mtprotoOnline}
						<CheckCircle2 size={12} />
					{:else}
						<XCircle size={12} />
					{/if}
					MTProto (:{summary.telegram.directPort}) — {summary.telegram.mtprotoOnline ? 'активен' : 'выключен'}
				</span>
				<span class="split-pill {summary.telegram.webOnline ? 'pill-on' : 'pill-off'}">
					{#if summary.telegram.webOnline}
						<CheckCircle2 size={12} />
					{:else}
						<XCircle size={12} />
					{/if}
					Веб-подключение (:{summary.telegram.webListenPort}) — {summary.telegram.webOnline ? 'активно' : 'выключено'}
				</span>
			</div>

			<div class="service-meta-grid">
				<div class="meta-item">
					<span class="meta-label">Прямой хост MTProto:</span>
					<span class="meta-val font-mono">
						{summary.telegram.directHost ? `${summary.telegram.directHost}:${summary.telegram.directPort}` : 'Не указан'}
					</span>
				</div>
				<div class="meta-item">
					<span class="meta-label">Веб-эндпоинты ({summary.telegram.webEndpoints.length}):</span>
					<span class="meta-val font-mono">
						{#if summary.telegram.webEndpoints.length > 0}
							{summary.telegram.webEndpoints.map((e) => `${e.hostname} (${e.carrierMode})`).join(', ')}
						{:else}
							Не настроены
						{/if}
					</span>
				</div>
				<div class="meta-item">
					<span class="meta-label">Выход трафика:</span>
					<span class="meta-val">{summary.telegram.egressLabel}</span>
				</div>
			</div>
		</div>
	</div>

	<!-- Dispatcher Route Warnings & Expandable Route Table -->
	{#if summary.dispatcher.warnings.length > 0}
		<div class="warnings-box">
			{#each summary.dispatcher.warnings as warn}
				<div class="warning-line">
					<AlertTriangle size={14} class="text-amber-400 shrink-0" />
					<span>{warn}</span>
				</div>
			{/each}
		</div>
	{/if}

	<div class="dispatcher-bar">
		<div class="disp-left">
			<Network size={14} class="text-slate-400" />
			<span class="disp-text">
				Входящие подключения (<code>{summary.dispatcher.listenAddr}</code>):
				<strong>{summary.dispatcher.routes.length}</strong> маршрутов
			</span>
			{#if summary.dispatcher.routes.length > 0}
				<button
					type="button"
					class="toggle-routes-btn"
					onclick={() => (showRoutesTable = !showRoutesTable)}
				>
					{showRoutesTable ? 'Скрыть таблицу доменов' : 'Показать таблицу доменов'}
				</button>
			{/if}
		</div>
		<button
			type="button"
			class="disp-nav-btn"
			class:active={activeKind === 'dispatcher'}
			onclick={() => onSelectService('__dispatcher__')}
		>
			<span>Дополнительно → {EXTERNAL_ACCESS_LABELS.dispatcherTitle}</span>
			<ArrowRight size={13} />
		</button>
	</div>

	{#if showRoutesTable && summary.dispatcher.routes.length > 0}
		<div class="routes-mini-table-wrap">
			<table class="routes-mini-table">
				<thead>
					<tr>
						<th>Домен</th>
						<th>VLESS ({summary.dispatcher.xrayPathPrefix}/*)</th>
						<th>Telegram (/, /api/*)</th>
						<th>Статус</th>
					</tr>
				</thead>
				<tbody>
					{#each summary.dispatcher.routes as r (r.id)}
						<tr class:disabled-row={!r.enabled}>
							<td class="font-mono">{r.domain}</td>
							<td>{r.routeXray ? 'Да' : '—'}</td>
							<td>{r.routeTelegram ? 'Да' : '—'}</td>
							<td>{r.enabled ? 'Активен' : 'Отключён'}</td>
						</tr>
					{/each}
				</tbody>
			</table>
		</div>
	{/if}

	<ConnectionChecks
		kind={effectiveVerifyKind}
		currentFingerprint={summary.configFingerprint}
		compact={true}
	/>
</section>

<ExternalAccessWizard
	bind:open={wizardOpen}
	initialInput={{
		xrayConfig,
		xrayStatus,
		xrayTuning,
		tgConfig,
		tgStatus,
		dispatcherConfig,
	}}
	onclose={() => {
		wizardOpen = false;
		handleWizardApplied();
	}}
	onapplied={handleWizardApplied}
	onsettled={handleWizardApplied}
/>

<style>
	.summary-card {
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		padding: 1rem 1.25rem;
		margin-bottom: 1rem;
		display: flex;
		flex-direction: column;
		gap: 0.875rem;
	}

	.summary-header {
		display: flex;
		justify-content: space-between;
		align-items: flex-start;
		gap: 1rem;
		flex-wrap: wrap;
	}

	.header-left {
		display: flex;
		flex-direction: column;
		gap: 3px;
	}

	.title-row {
		display: flex;
		align-items: center;
		gap: 8px;
		flex-wrap: wrap;
	}

	.title {
		font-size: 15px;
		font-weight: 600;
		color: var(--color-text-primary);
		margin: 0;
	}

	.port-pill {
		font-size: 11px;
		padding: 2px 7px;
		border-radius: var(--radius-sm);
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		color: var(--color-text-muted);
	}

	.subtitle {
		font-size: 12px;
		color: var(--color-text-muted);
		margin: 0;
	}

	.header-actions {
		display: flex;
		align-items: center;
		gap: 8px;
	}

	.refresh-btn {
		width: 30px;
		height: 30px;
		display: flex;
		align-items: center;
		justify-content: center;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		color: var(--color-text-muted);
		cursor: pointer;
	}

	.refresh-btn:hover:not(:disabled) {
		color: var(--color-text-primary);
		border-color: var(--color-accent);
	}

	.services-grid {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 0.75rem;
	}

	@media (max-width: 900px) {
		.services-grid {
			grid-template-columns: 1fr;
		}
	}

	.service-box {
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		padding: 0.75rem 0.875rem;
		display: flex;
		flex-direction: column;
		gap: 0.625rem;
	}

	.service-box.active-service {
		border-color: var(--color-accent);
	}

	.service-top {
		display: flex;
		justify-content: space-between;
		align-items: center;
		gap: 8px;
	}

	.service-title-wrap {
		display: flex;
		align-items: center;
		gap: 6px;
		flex-wrap: wrap;
	}

	.service-name {
		font-size: 13px;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.status-badge {
		font-size: 10px;
		font-weight: 600;
		padding: 2px 6px;
		border-radius: 999px;
	}

	.badge-ok {
		background: rgba(34, 197, 94, 0.15);
		color: #4ade80;
	}

	.badge-warn {
		background: rgba(245, 158, 11, 0.15);
		color: #fbbf24;
	}

	.badge-off {
		background: var(--color-bg-secondary);
		color: var(--color-text-muted);
	}

	.nav-link {
		display: inline-flex;
		align-items: center;
		gap: 4px;
		background: none;
		border: none;
		font-size: 11px;
		color: var(--color-accent);
		cursor: pointer;
		padding: 2px 4px;
	}

	.nav-link:hover {
		text-decoration: underline;
	}

	.tg-split-badges {
		display: flex;
		flex-wrap: wrap;
		gap: 6px;
	}

	.split-pill {
		display: inline-flex;
		align-items: center;
		gap: 4px;
		font-size: 11px;
		padding: 2px 7px;
		border-radius: 4px;
		border: 1px solid var(--color-border);
	}

	.pill-on {
		background: rgba(34, 197, 94, 0.1);
		border-color: rgba(34, 197, 94, 0.3);
		color: #4ade80;
	}

	.pill-off {
		background: var(--color-bg-secondary);
		color: var(--color-text-muted);
	}

	.service-meta-grid {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 6px 10px;
	}

	.meta-item {
		display: flex;
		flex-direction: column;
		gap: 1px;
		min-width: 0;
	}

	.meta-label {
		font-size: 10px;
		color: var(--color-text-muted);
	}

	.meta-val {
		font-size: 11px;
		color: var(--color-text-primary);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.meta-val.legacy-warn {
		color: #fbbf24;
		font-weight: 600;
	}

	.warnings-box {
		display: flex;
		flex-direction: column;
		gap: 4px;
		padding: 8px 10px;
		background: rgba(245, 158, 11, 0.1);
		border: 1px solid rgba(245, 158, 11, 0.3);
		border-radius: var(--radius-sm);
		font-size: 11px;
		color: #fde68a;
	}

	.warning-line {
		display: flex;
		align-items: center;
		gap: 6px;
	}

	.dispatcher-bar {
		display: flex;
		justify-content: space-between;
		align-items: center;
		flex-wrap: wrap;
		gap: 8px;
		padding: 6px 10px;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		font-size: 12px;
	}

	.disp-left {
		display: flex;
		align-items: center;
		gap: 8px;
		flex-wrap: wrap;
		color: var(--color-text-secondary);
	}

	.toggle-routes-btn {
		background: none;
		border: none;
		color: var(--color-accent);
		font-size: 11px;
		cursor: pointer;
		padding: 0;
	}

	.toggle-routes-btn:hover {
		text-decoration: underline;
	}

	.disp-nav-btn {
		display: inline-flex;
		align-items: center;
		gap: 4px;
		background: none;
		border: 1px solid transparent;
		border-radius: 4px;
		padding: 3px 8px;
		font-size: 11px;
		color: var(--color-text-muted);
		cursor: pointer;
	}

	.disp-nav-btn:hover,
	.disp-nav-btn.active {
		color: var(--color-text-primary);
		border-color: var(--color-border);
		background: var(--color-bg-secondary);
	}

	.routes-mini-table-wrap {
		overflow-x: auto;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
	}

	.routes-mini-table {
		width: 100%;
		border-collapse: collapse;
		font-size: 11px;
	}

	.routes-mini-table th,
	.routes-mini-table td {
		padding: 6px 10px;
		text-align: left;
		border-bottom: 1px solid var(--color-border);
	}

	.routes-mini-table th {
		background: var(--color-bg-primary);
		color: var(--color-text-muted);
		font-weight: 500;
	}

	.disabled-row {
		opacity: 0.5;
	}

	.font-mono {
		font-family: var(--font-mono);
	}

	.summary-card :global(.mr-1) {
		margin-right: 4px;
	}

	.refresh-btn :global(.spin) {
		animation: spin 1s linear infinite;
	}

	@keyframes spin {
		from { transform: rotate(0deg); }
		to { transform: rotate(360deg); }
	}
</style>
