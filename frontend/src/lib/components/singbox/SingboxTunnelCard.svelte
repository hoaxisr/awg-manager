<script lang="ts">
	import type { SingboxTunnel } from '$lib/types';
	import { goto } from '$app/navigation';
	import { api } from '$lib/api/client';
	import {
		singboxTunnels,
		singboxDelayHistory,
		singboxTraffic,
		triggerDelayCheck,
	} from '$lib/stores/singbox';
	import { untrack } from 'svelte';
	import { Modal, Button, TrafficSparkline, TrafficChart } from '$lib/components/ui';
	import { TunnelListActions } from '$lib/components/ui';
	import {
		TunnelDelaySparkBars,
		TunnelListEndpointLine,
		TunnelListTrafficCell,
		TunnelMetaText,
		TunnelSingboxPingButton,
		TunnelTitleRow,
	} from '$lib/components/tunnels';
	import { singboxDelayStatusDot } from '$lib/utils/statusDot';
	import { formatBitRate, formatBytes } from '$lib/utils/format';
	import { getTrafficRates, subscribeTraffic, loadHistory } from '$lib/stores/traffic';
	import { singboxDelayFromHistory } from '$lib/utils/singboxDelay';
	import type { SingboxLayoutMode } from '$lib/constants/singboxLayout';
	import { Eye, EyeOff } from 'lucide-svelte';
	import TunnelDiagnosticsModal from '$lib/components/testing/TunnelDiagnosticsModal.svelte';
	import { notifications } from '$lib/stores/notifications';
	import { showOutboundReferencedError } from '$lib/utils/outboundReferenced';

	interface Props {
		tunnel: SingboxTunnel;
		layout?: SingboxLayoutMode;
		renderMode?: import('$lib/constants/singboxLayout').TunnelRenderMode;
		autoDelayCheckNonce?: number;
		autoDelayCheckDelayMs?: number;
		ondetail?: (tag: string) => void;
	}

	let {
		tunnel,
		layout = 'compact',
		renderMode = 'compact',
		autoDelayCheckNonce = 0,
		autoDelayCheckDelayMs = 0,
		ondetail,
	}: Props = $props();

	let deleting = $state(false);
	let confirmDeleteOpen = $state(false);
	let diagnosticsOpen = $state(false);
	let showServer = $state(false);
	let checking = $state(false);

	const history = $derived($singboxDelayHistory.get(tunnel.tag) ?? []);
	const delayPresentation = $derived(
		singboxDelayFromHistory(history, { running: tunnel.running !== false }),
	);
	const latest = $derived(delayPresentation.latest);
	const positiveHistory = $derived(history.filter((v) => v > 0));
	const avg = $derived(
		positiveHistory.length > 0
			? Math.round(positiveHistory.reduce((s, v) => s + v, 0) / positiveHistory.length)
			: 0,
	);
	const traffic = $derived($singboxTraffic.get(tunnel.tag));

	const trafficSparkSeries = $derived.by(() => {
		const n = Math.min(rxRates.length, txRates.length);
		if (n === 0) return { rx: [] as number[], tx: [] as number[] };
		const take = Math.min(36, n);
		const start = n - take;
		return {
			rx: rxRates.slice(start, n),
			tx: txRates.slice(start, n),
		};
	});

	const cardState = $derived(delayPresentation.state);
	const latText = $derived(delayPresentation.label);
	const statusDot = $derived(singboxDelayStatusDot(cardState, tunnel.running !== false));

	const protocolLabel = $derived.by(() => {
		// Widen locally: the generated/static type is currently an exhaustive union,
		// but runtime data may contain a newer sing-box protocol before frontend types
		// are updated. Keep a safe fallback without making the default branch `never`.
		const protocol = tunnel.protocol as string | undefined;

		switch (protocol) {
			case 'vless':
				return 'VLESS';
			case 'hysteria2':
				return 'Hysteria2';
			case 'trojan':
				return 'Trojan';
			case 'shadowsocks':
				return 'Shadowsocks';
			case 'naive':
				return 'Naive';
			case 'mieru':
				return 'Mieru';
			default:
				return protocol ? protocol.charAt(0).toUpperCase() + protocol.slice(1) : '—';
		}
	});

	async function triggerCheck(): Promise<void> {
		if (checking) return;
		checking = true;
		try {
			await triggerDelayCheck(tunnel.tag);
		} finally {
			checking = false;
		}
	}

	let lastAutoDelayCheckNonce = 0;
	$effect(() => {
		const nonce = autoDelayCheckNonce;
		const delay = autoDelayCheckDelayMs;
		if (nonce <= 0 || nonce === lastAutoDelayCheckNonce) return;
		lastAutoDelayCheckNonce = nonce;
		if (tunnel.running !== true) return;

		const timer = setTimeout(() => {
			untrack(() => void triggerCheck());
		}, delay);
		return () => clearTimeout(timer);
	});

	async function remove(): Promise<void> {
		deleting = true;
		confirmDeleteOpen = false;
		try {
			const fresh = await api.singboxDeleteTunnel(tunnel.tag);
			// Instant update — beats waiting for the poll or SSE hint refetch.
			singboxTunnels.applyMutationResponse(fresh);
		} catch (e) {
			if (!showOutboundReferencedError(e, tunnel.tag, 'Туннель')) {
				notifications.error(e instanceof Error ? e.message : 'Не удалось удалить туннель');
			}
		} finally {
			deleting = false;
		}
	}

	function edit(): void {
		goto(`/singbox/${encodeURIComponent(tunnel.tag)}`);
	}

	// ─── Traffic sparkline (rate history fed by +layout SSE handler) ─
	let rxRates = $state<number[]>([]);
	let txRates = $state<number[]>([]);
	let tunnelTag = $derived(tunnel.tag);

	let inlineRxRate = $derived(rxRates.length > 0 ? rxRates[rxRates.length - 1] : 0);
	let inlineTxRate = $derived(txRates.length > 0 ? txRates[txRates.length - 1] : 0);

	$effect(() => {
		const tag = tunnelTag;
		const update = () => {
			const t = getTrafficRates(tag);
			rxRates = t.rx;
			txRates = t.tx;
		};
		update();
		return subscribeTraffic(update);
	});

	let initialLoadDone = false;
	$effect(() => {
		const tag = tunnelTag;
		if (initialLoadDone) return;
		initialLoadDone = true;
		untrack(() => loadHistory(tag));
	});

</script>

{#if renderMode === 'table'}
	<tr
		class="sbx-tunnel-list-row"
		class:ok={cardState === 'ok'}
		class:slow={cardState === 'slow'}
		class:fail={cardState === 'fail'}
		class:unknown={cardState === 'unknown'}
		class:stopped={cardState === 'stopped'}
	>
		<td class="tunnel-list-cell tunnel-list-cell--delay list-cell list-cell-delay" data-label="Delay">
			<TunnelSingboxPingButton
				layout="list"
				label={latText}
				state={cardState}
				{checking}
				onclick={triggerCheck}
			/>
		</td>
		<td class="tunnel-list-cell tunnel-list-cell--name list-cell list-cell-name" data-label="Туннель">
			<div class="tunnel-list-name-stack">
				<TunnelTitleRow
					title={tunnel.tag}
					dotVariant={statusDot.variant}
					dotPulse={statusDot.pulse}
					dotLabel={tunnel.tag}
					onTitleClick={edit}
				/>
				<TunnelMetaText mono>
					<span>{tunnel.proxyInterface || 'via sing-box'}</span>
					{#if tunnel.kernelInterface}
						<span class="meta-dot" aria-hidden="true">·</span><span>{tunnel.kernelInterface}</span>
					{/if}
				</TunnelMetaText>
				<TunnelListEndpointLine host={tunnel.server} port={tunnel.port} bind:show={showServer} />
			</div>
		</td>
		<td class="list-cell list-cell-badges" data-label="Протокол">
			<div class="badges-inline">
				<span class="badge b-{tunnel.protocol}">{protocolLabel}</span>
				{#if tunnel.security === 'reality'}
					<span class="badge b-reality">Reality</span>
				{:else if tunnel.security === 'tls'}
					<span class="badge b-tls">TLS</span>
				{/if}
				<span class="badge b-transport">{tunnel.transport.toUpperCase()}</span>
			</div>
		</td>
		<td class="list-cell list-cell-run" data-label="Процесс">
			<span class="run-pill" class:run-on={tunnel.running === true}>{tunnel.running === true ? 'running' : 'stopped'}</span>
		</td>
		<td class="tunnel-list-cell tunnel-list-cell--traffic list-cell list-cell-traffic" data-label="Трафик">
			<TunnelListTrafficCell
				rxRate={inlineRxRate}
				txRate={inlineTxRate}
				rxData={trafficSparkSeries.rx}
				txData={trafficSparkSeries.tx}
				onclick={() => ondetail?.(tunnel.tag)}
				title="Открыть детальный график"
			/>
		</td>
		<td class="tunnel-list-cell tunnel-list-cell--ping list-cell list-cell-ping-mini" data-label="Ping">
			<TunnelDelaySparkBars history={history} state={cardState} layout="list" onclick={triggerCheck} />
		</td>
		<td class="tunnel-list-cell tunnel-list-cell--actions list-cell list-cell-actions col-actions" data-label="Действия">
			<TunnelListActions
				onEdit={edit}
				editTitle="Изменить туннель «{tunnel.tag}»"
				onTest={() => (diagnosticsOpen = true)}
				testDisabled={!tunnel.kernelInterface}
				testTitle="Тест туннеля «{tunnel.tag}»"
				onDelete={() => (confirmDeleteOpen = true)}
				deleteDisabled={deleting}
				deleting={deleting}
				deleteTitle="Удалить туннель «{tunnel.tag}»"
			/>
		</td>
	</tr>
{:else if layout === 'dense' || renderMode === 'list-card'}
<div
	class="card view-dense"
	class:view-list={renderMode === 'list-card'}
	class:ok={cardState === 'ok'}
	class:slow={cardState === 'slow'}
	class:fail={cardState === 'fail'}
	class:unknown={cardState === 'unknown'}
	class:stopped={cardState === 'stopped'}
>
	<div class="header header-dense">
		<div class="header-dense-body">
			<div class="title-row-dense">
				<TunnelTitleRow
					title={tunnel.tag}
					dotVariant={statusDot.variant}
					dotPulse={statusDot.pulse}
					dense
					onTitleClick={edit}
				/>
			</div>
			<div class="meta-tags-dense">
				<span class="iface-dense" title="{tunnel.proxyInterface || 'via sing-box'}{tunnel.kernelInterface ? ` · ${tunnel.kernelInterface}` : ''}">
					<span>{tunnel.proxyInterface || 'via sing-box'}</span>
					{#if tunnel.kernelInterface}<span class="meta-dot" aria-hidden="true">·</span><span>{tunnel.kernelInterface}</span>{/if}
				</span>
				<span class="badge b-{tunnel.protocol}">{protocolLabel}</span>
				{#if tunnel.security === 'reality'}
					<span class="badge b-reality">Reality</span>
				{:else if tunnel.security === 'tls'}
					<span class="badge b-tls">TLS</span>
				{/if}
				<span class="badge b-transport">{tunnel.transport.toUpperCase()}</span>
			</div>
		</div>
		<div class="dense-toolbar">
			<div class="dense-toolbar-bottom">
				<TunnelSingboxPingButton layout="dense" label={latText} state={cardState} {checking} onclick={triggerCheck} />
			</div>
		</div>
	</div>

	{#if renderMode !== 'list-card'}
	<div class="details">
	<div class="details-dense-cols">
		<div class="details-dense-col details-dense-col-lead">
			<div class="kv-stacked-stat">
				<span class="kv-stacked-label">Сервер</span>
				<span class="kv-endpoint">
					<span class="kv-stacked-value" title={showServer ? tunnel.server : ''}>
						{showServer ? tunnel.server : '••••••••'}
					</span>
					<button class="icon-btn" onclick={() => (showServer = !showServer)} aria-label={showServer ? 'Скрыть' : 'Показать'}>
						{#if showServer}
							<Eye size={12} aria-hidden="true" />
						{:else}
							<EyeOff size={12} aria-hidden="true" />
						{/if}
					</button>
				</span>
			</div>
			{#if tunnel.protocol === 'naive'}
				<div class="kv-stacked-stat">
					<span class="kv-stacked-label">Логин</span>
					<span class="kv-stacked-value">{tunnel.username || '—'}</span>
				</div>
			{:else if tunnel.sni}
				<div class="kv-stacked-stat">
					<span class="kv-stacked-label">SNI</span>
					<span class="kv-stacked-value">{showServer ? tunnel.sni : '••••••••'}</span>
				</div>
			{/if}
		</div>
		<div class="details-dense-col details-dense-col-right">
			<div class="kv-stacked-stat">
				<span class="kv-stacked-label">Порт</span>
				<span class="kv-stacked-value">:{tunnel.port}</span>
			</div>
			<div class="kv-stacked-stat">
				<span class="kv-stacked-label">Delay</span>
				<span class="kv-stacked-value">
					{#if cardState === 'unknown'}—{:else if cardState === 'fail'}fail{:else}avg {avg}ms{/if}
				</span>
			</div>
		</div>
	</div>
	</div>
	{/if}

	<div class="actions">
		<TunnelListActions
			variant="labeled"
			onEdit={edit}
			editTitle="Изменить туннель «{tunnel.tag}»"
			onTest={() => (diagnosticsOpen = true)}
			testDisabled={!tunnel.kernelInterface}
			testTitle="Тест туннеля «{tunnel.tag}»"
			onDelete={() => (confirmDeleteOpen = true)}
			deleteDisabled={deleting}
			deleting={deleting}
			deleteTitle="Удалить туннель «{tunnel.tag}»"
		/>
	</div>

	{#if renderMode !== 'list-card'}
	<div class="charts-dense">
		<button
			type="button"
			class="traffic-inline"
			onclick={() => ondetail?.(tunnel.tag)}
			title="Открыть график трафика"
		>
			<TrafficSparkline
				rxData={trafficSparkSeries.rx}
				txData={trafficSparkSeries.tx}
				responsive
				height={20}
			/>
			<div class="traffic-inline-rates">
				<span class="traffic-inline-rate rx">↓ {formatBitRate(inlineRxRate)}</span>
				<span class="traffic-inline-rate tx">↑ {formatBitRate(inlineTxRate)}</span>
			</div>
		</button>
		<div class="chart-inline delay-inline">
			<div class="chart-inline-head">
				<span class="chart-inline-label">Delay (5 мин)</span>
			</div>
			<TunnelDelaySparkBars
				history={history}
				state={cardState}
				layout="dense"
				onclick={() => void triggerCheck()}
			/>
		</div>
	</div>
	{/if}
</div>
{:else}
<div
	class="card view-compact"
	class:ok={cardState === 'ok'}
	class:slow={cardState === 'slow'}
	class:fail={cardState === 'fail'}
	class:unknown={cardState === 'unknown'}
	class:stopped={cardState === 'stopped'}
>
	<div class="tunnel-card-intro">
	<div class="title-row">
		<TunnelTitleRow
			title={tunnel.tag}
			dotVariant={statusDot.variant}
			dotPulse={statusDot.pulse}
			onTitleClick={edit}
		/>
		<TunnelSingboxPingButton layout="compact" label={latText} state={cardState} {checking} onclick={triggerCheck} />
	</div>
	<div class="iface">
		<span>{tunnel.proxyInterface || 'via sing-box'}</span>
		{#if tunnel.kernelInterface}
			<span class="meta-dot" aria-hidden="true">·</span><span>{tunnel.kernelInterface}</span>
		{/if}
	</div>

	<div class="badges">
		<span class="badge b-{tunnel.protocol}">{protocolLabel}</span>
		{#if tunnel.security === 'reality'}
			<span class="badge b-reality">Reality</span>
		{:else if tunnel.security === 'tls'}
			<span class="badge b-tls">TLS</span>
		{/if}
		<span class="badge b-transport">{tunnel.transport.toUpperCase()}</span>
	</div>
	</div>

	<div class="divider divider-dashed"></div>

	<div class="row">
		<span class="label">Сервер</span>
		<div class="server-row value">
			{#if showServer}
				<span class="server-text">{tunnel.server}</span>
			{:else}
				<span class="server-hidden">●●●●●●●●</span>
			{/if}
			<button class="icon-btn" onclick={() => (showServer = !showServer)} aria-label={showServer ? 'Скрыть' : 'Показать'}>
				<Eye size={12} aria-hidden="true" />
			</button>
			<span class="port">:{tunnel.port}</span>
		</div>
	</div>

	{#if tunnel.protocol === 'naive'}
		<div class="row">
			<span class="label">Логин</span>
			<span class="value">{tunnel.username || '—'}</span>
		</div>
	{:else if tunnel.sni}
		<div class="row">
			<span class="label">SNI</span>
			<span class="value">
				{#if showServer}
					{tunnel.sni}
				{:else}
					<span class="server-hidden">●●●●●●●●</span>
				{/if}
			</span>
		</div>
	{/if}

	<div class="actions actions--bar">
		<TunnelListActions
			variant="labeled"
			onEdit={edit}
			editTitle="Изменить туннель «{tunnel.tag}»"
			onTest={() => (diagnosticsOpen = true)}
			testDisabled={!tunnel.kernelInterface}
			testTitle="Тест туннеля «{tunnel.tag}»"
			onDelete={() => (confirmDeleteOpen = true)}
			deleteDisabled={deleting}
			deleting={deleting}
			deleteTitle="Удалить туннель «{tunnel.tag}»"
		/>
	</div>

	<div class="chart-section">
		<div class="chart-body">
			<div class="chart-head">
				<span>Delay (5 мин)</span>
			</div>
			<TunnelDelaySparkBars
				history={history}
				state={cardState}
				layout="compact"
				onclick={() => void triggerCheck()}
			/>
			<div class="chart-head traffic-head">
				<span>Трафик</span>
				<span class="stats">
					↓ {formatBytes(traffic?.download ?? 0)} · ↑ {formatBytes(traffic?.upload ?? 0)}
				</span>
			</div>
			<TrafficChart
				{rxRates}
				{txRates}
				rxTotal={traffic?.download ?? 0}
				txTotal={traffic?.upload ?? 0}
				height={56}
				onclick={() => ondetail?.(tunnel.tag)}
			/>
		</div>
	</div>
</div>
{/if}

<TunnelDiagnosticsModal
	open={diagnosticsOpen}
	kind="singbox"
	targetId={tunnel.tag}
	displayName={tunnel.tag}
	subjectLabel="туннель"
	iface={tunnel.kernelInterface}
	loading={false}
	unavailableReason={tunnel.kernelInterface ? undefined : 'У этого sing-box туннеля нет kernel interface, расширенные тесты недоступны.'}
	onclose={() => (diagnosticsOpen = false)}
/>

<Modal
	open={confirmDeleteOpen}
	title="Удаление"
	size="sm"
	onclose={() => (confirmDeleteOpen = false)}
>
	<p class="confirm-text">Удалить туннель <strong>{tunnel.tag}</strong>?</p>
	{#snippet actions()}
		<Button variant="ghost" size="md" onclick={() => (confirmDeleteOpen = false)}>Отмена</Button>
		<Button variant="danger" size="md" onclick={remove}>Удалить</Button>
	{/snippet}
</Modal>

<style>
	.card {
		display: flex;
		flex-direction: column;
		gap: 10px;
		padding: 12px 14px;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		color: var(--color-text-primary);
		position: relative;
		transition: border-color var(--t-fast) ease;
	}
	.card.ok { border-color: var(--color-success-border); }
	.card.slow { border-color: var(--color-warning-border); }
	.card.fail { border-color: var(--color-error-border); }
	.card.unknown { border-color: var(--color-border); }
	.card.stopped { border-color: var(--color-muted-border); opacity: 0.7; }

	.card.view-dense {
		gap: 8px;
		padding: 10px 12px;
		position: relative;
	}

	.card.view-dense .header.header-dense {
		display: grid;
		grid-template-columns: minmax(0, 1fr) auto;
		align-items: flex-start;
		gap: 6px;
	}

	.header-dense-body {
		display: flex;
		flex-direction: column;
		gap: 1px;
		min-width: 0;
	}

	.title-row-dense {
		display: grid;
		grid-template-columns: auto minmax(0, 1fr);
		align-items: center;
		gap: 6px;
		min-width: 0;
	}

	.title-dense {
		margin: 0;
		padding: 0;
		border: none;
		background: none;
		font: inherit;
		font-size: 13px;
		font-weight: 600;
		color: inherit;
		cursor: pointer;
		text-align: left;
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.title-dense:hover {
		color: var(--color-accent);
	}

	.meta-tags-dense {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		margin-top: 3px;
		gap: 3px;
		min-width: 0;
	}

	.iface-dense {
		display: inline-flex;
		flex-wrap: wrap;
		align-items: center;
		font-size: 9px;
		font-weight: 500;
		font-family: var(--font-mono, monospace);
		color: var(--color-text-muted);
		min-width: 0;
	}

	.meta-dot {
		margin: 0 0.35em;
		opacity: 0.75;
	}

	.card.view-dense .meta-tags-dense .badge {
		font-size: 9px;
		padding: 1px 5px;
		line-height: 1.3;
	}

	.dense-toolbar {
		display: flex;
		flex-direction: column;
		align-items: flex-end;
		flex-shrink: 0;
	}

	.dense-toolbar-bottom {
		display: flex;
		align-items: center;
	}

	.details-dense-cols {
		display: grid;
		grid-template-columns: minmax(0, 1.2fr) 5.75rem;
		gap: 10px 10px;
		align-items: start;
	}

	.details-dense-col {
		display: flex;
		flex-direction: column;
		gap: 6px;
		min-width: 0;
	}

	.kv-stacked-stat {
		display: flex;
		flex-direction: column;
		gap: 1px;
		min-width: 0;
	}

	.card.view-dense .kv-endpoint {
		display: flex;
		align-items: center;
		gap: 2px;
		min-width: 0;
	}

	.kv-stacked-label {
		font-size: 9px;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		color: var(--color-text-muted);
		line-height: 1.2;
	}

	.kv-stacked-value {
		font-size: 10px;
		font-family: var(--font-mono, monospace);
		color: var(--color-text-secondary);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
		line-height: 1.25;
	}

	.card.view-dense .details {
		display: flex;
		flex-direction: column;
		gap: 6px;
		padding: 4px 0;
		border-top: 1px solid var(--color-border);
		border-bottom: 1px solid var(--color-border);
	}

	.charts-dense {
		display: flex;
		flex-direction: row;
		align-items: stretch;
		gap: 4px;
		width: 100%;
		min-width: 0;
	}

	.charts-dense > .delay-inline,
	.charts-dense > .traffic-inline {
		flex: 1 1 0;
		min-width: 0;
		width: auto;
	}

	.chart-inline {
		display: flex;
		flex-direction: column;
		gap: 3px;
		min-width: 0;
		padding: 5px 6px;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		background: var(--color-bg-secondary);
		font: inherit;
		color: inherit;
		text-align: left;
	}

	.chart-inline.delay-inline {
		padding: 5px 6px 4px;
	}

	.charts-dense .traffic-inline {
		display: flex;
		align-items: center;
		gap: 0.3rem;
		padding: 5px 4px 5px 5px;
		margin: 0;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		background: var(--color-bg-secondary);
		cursor: pointer;
		font: inherit;
		color: inherit;
		text-align: left;
		transition: background var(--t-fast) ease, border-color var(--t-fast) ease;
	}

	.charts-dense .traffic-inline :global(svg.responsive) {
		flex: 1 1 auto;
		width: 100%;
		min-width: 0;
	}

	.traffic-inline:hover {
		background: var(--color-bg-hover);
		border-color: var(--color-border-hover);
	}

	.traffic-inline:focus-visible {
		outline: 2px solid var(--color-accent);
		outline-offset: 2px;
	}

	.charts-dense .traffic-inline-rates {
		display: flex;
		flex-direction: column;
		align-items: flex-start;
		gap: 0.06rem;
		padding-block: 2px;
		min-width: 0;
		flex: 0 0 auto;
		font-size: 9px;
		line-height: 1.1;
		font-family: var(--font-mono, monospace);
		font-variant-numeric: tabular-nums;
	}

	.charts-dense .traffic-inline-rate {
		max-width: 100%;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.traffic-inline-rate.rx {
		color: var(--color-accent);
	}

	.traffic-inline-rate.tx {
		color: var(--color-success);
	}

	.chart-inline-head {
		display: flex;
		justify-content: space-between;
		align-items: baseline;
		gap: 6px;
		font-size: 9px;
		line-height: 1.2;
	}

	.chart-inline-label {
		color: var(--color-text-muted);
		text-transform: uppercase;
		letter-spacing: 0.04em;
		font-weight: 500;
	}

	.chart-inline-stats {
		color: var(--color-text-muted);
		font-family: var(--font-mono, monospace);
		font-variant-numeric: tabular-nums;
		white-space: nowrap;
	}

	.charts-dense .chart-inline-head {
		gap: 4px;
	}

	.card.view-dense .chart-inline.delay-inline {
		gap: 3px;
		padding: 5px 4px 5px 5px;
		overflow: hidden;
	}

	.card.view-dense .chart-inline.delay-inline .chart-inline-head {
		padding: 0;
	}

	.title-row {
		display: flex;
		align-items: center;
		gap: 6px;
		min-width: 0;
	}

	.title-row :global(.ping-btn) {
		flex-shrink: 0;
		margin-left: auto;
	}

	.dot {
		width: var(--sbx-status-dot);
		height: var(--sbx-status-dot);
		border-radius: 50%;
		background: var(--text-muted);
	}
	.dot.ok   { background: var(--latency-color-ok); box-shadow: 0 0 6px var(--latency-dot-ok-shadow); }
	.dot.slow { background: var(--latency-color-slow); box-shadow: 0 0 6px var(--latency-dot-slow-shadow); }
	.dot.fail { background: var(--latency-color-fail); box-shadow: 0 0 6px var(--latency-dot-fail-shadow); }
	.dot.stopped { background: var(--color-text-muted); }

	.title {
		margin: 0;
		min-width: 0;
		flex: 1 1 auto;
		font-size: var(--sbx-card-title);
		line-height: var(--sbx-card-title-line-height);
		font-weight: 600;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.iface {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		color: var(--color-text-muted);
		font-size: var(--sbx-card-meta);
		margin-bottom: 0;
		font-family: var(--font-mono, monospace);
	}

	.badges {
		display: flex;
		gap: 5px;
		flex-wrap: wrap;
	}
	.badge {
		padding: 2px 8px;
		font-size: var(--sbx-card-badge);
		border-radius: 10px;
		font-weight: 500;
	}
	.b-vless { background: rgba(59, 130, 246, 0.15); color: #60a5fa; }
	.b-hysteria2 { background: rgba(245, 158, 11, 0.15); color: #fbbf24; }
	.b-trojan { background: rgba(244, 63, 94, 0.15); color: #fb7185; }
	.b-shadowsocks { background: rgba(16, 185, 129, 0.15); color: #34d399; }
	.b-mieru { background: rgba(20, 184, 166, 0.18); color: #5eead4; }
	/* Cyan-400 on 15% alpha perceptually washed out against the dark
	   bg — bump to cyan-300 text with slightly denser background so
	   NaiveProxy matches the contrast of the other protocol badges. */
	.b-naive { background: rgba(34, 211, 238, 0.22); color: #67e8f9; }
	.b-reality { background: rgba(236, 72, 153, 0.15); color: #f472b6; }
	.b-tls { background: rgba(139, 92, 246, 0.15); color: #a78bfa; }
	.b-transport { background: rgba(100, 100, 100, 0.3); color: var(--text-muted); }

	.row {
		display: flex;
		align-items: center;
		margin: 0;
	}
	.row .label {
		color: var(--color-text-muted);
		font-size: var(--sbx-card-label);
		text-transform: uppercase;
		letter-spacing: 0.04em;
		width: 60px;
		flex-shrink: 0;
	}
	.row .value {
		font-size: var(--sbx-card-value);
		color: var(--color-text-secondary);
		font-family: var(--font-mono, monospace);
	}
	.server-row {
		display: flex;
		align-items: center;
		gap: 6px;
		flex: 1;
	}
	.server-hidden { color: var(--text-muted); letter-spacing: 2px; }
	.server-text { font-family: var(--font-mono, monospace); }
	.icon-btn {
		background: none;
		border: none;
		color: var(--text-muted);
		cursor: pointer;
		padding: 2px;
		display: inline-flex;
	}
	.icon-btn:hover { color: var(--text); }

	.port { color: var(--text); margin-left: auto; font-variant-numeric: tabular-nums; }

	.divider {
		height: 0;
		border: none;
		margin: 0;
		background: none;
	}
	.divider-dashed {
		border-top: 1px dashed var(--color-border);
	}

	.chart-block { margin-bottom: 10px; }
	.chart-head {
		display: flex;
		justify-content: space-between;
		color: var(--color-text-muted);
		font-size: var(--sbx-card-label);
		text-transform: uppercase;
		letter-spacing: 0.04em;
		margin-bottom: 4px;
	}
	.chart-head .stats {
		color: var(--color-text-muted);
		font-size: var(--sbx-card-value);
		text-transform: none;
		letter-spacing: normal;
	}
	.chart-head.traffic-head .stats {
		font-size: 0.6875rem;
	}
	.traffic-head { margin-top: 8px; }

	.list-cell-ping-mini {
		justify-content: flex-start;
		padding-right: 0.6rem;
	}
	.traffic-row-list {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		min-width: 0;
	}
	.traffic-mini-col {
		display: flex;
		flex-direction: column;
		gap: 0.1rem;
		font-size: var(--sbx-card-note);
		line-height: 1.15;
		flex-shrink: 0;
	}
	.traffic-mini-click {
		display: inline-flex;
		border-radius: 4px;
		cursor: pointer;
		transition: background var(--t-fast) ease;
	}
	.traffic-mini-click:hover {
		background: rgba(96, 165, 250, 0.06);
	}
	.traffic-mini-click:focus-visible {
		outline: 1px solid var(--color-accent, #58a6ff);
		outline-offset: 1px;
	}

	.chart-section {
		margin: 0 -14px -12px;
		border-radius: 0 0 var(--radius) var(--radius);
		background: var(--color-bg-secondary);
		overflow: hidden;
	}
	.chart-body {
		padding: 8px 12px 8px;
	}

	.chart-body :global(.tunnel-delay-spark--compact) {
		height: 36px;
	}

	/* List row (grid columns set on parent .singbox-tunnel-list-table) */
	.sbx-tunnel-list-row {
		min-width: 0;
	}
	.sbx-tunnel-list-row .list-cell {
		min-width: 0;
		vertical-align: middle;
	}
	.sbx-tunnel-list-row .list-cell-delay {
		display: flex;
		align-items: center;
		gap: 0.4rem;
	}
	.sbx-tunnel-list-row .list-title-row {
		display: flex;
		align-items: center;
		gap: 0.4rem;
		min-width: 0;
		max-width: 100%;
	}
	.sbx-tunnel-list-row .name-btn {
		font: inherit;
		font-size: var(--sbx-card-title);
		font-weight: 600;
		color: var(--text);
		background: none;
		border: none;
		padding: 0;
		cursor: pointer;
		text-align: left;
	}
	.sbx-tunnel-list-row .name-btn:hover {
		color: var(--color-accent, #58a6ff);
	}
	.sbx-tunnel-list-row .list-sub {
		margin-top: 0;
		font-size: var(--sbx-card-meta);
		color: var(--color-text-muted);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.sbx-tunnel-list-row .traffic-row-list {
		display: flex;
		min-width: 0;
		width: 100%;
	}

	.sbx-tunnel-list-row .traffic-row-list--stack {
		flex-direction: column;
		align-items: stretch;
		gap: 0.05rem;
		border-radius: 4px;
		cursor: pointer;
		font-size: var(--sbx-card-note);
		line-height: 1.1;
		transition: background var(--t-fast) ease;
	}

	.sbx-tunnel-list-row .traffic-row-list--stack :global(svg.responsive) {
		width: 100%;
		min-width: 0;
		max-width: 100%;
		flex: 1 1 auto;
	}

	.sbx-tunnel-list-row .traffic-row-list--stack:hover {
		background: rgba(96, 165, 250, 0.06);
	}

	.sbx-tunnel-list-row .traffic-row-list--stack:focus-visible {
		outline: 1px solid var(--color-accent, #58a6ff);
		outline-offset: 1px;
	}
	.badges-inline {
		display: flex;
		flex-wrap: wrap;
		gap: 0.25rem;
	}

	.sbx-tunnel-list-row .list-cell-badges {
		display: flex;
		align-items: center;
		justify-content: center;
		align-self: stretch;
	}

	.sbx-tunnel-list-row .badges-inline {
		display: inline-flex;
		flex-direction: column;
		flex-wrap: nowrap;
		align-items: center;
		justify-content: center;
		gap: 0.25rem;
		min-width: 0;
	}

	.sbx-tunnel-list-row .badges-inline .badge {
		width: max-content;
		max-width: 100%;
		text-align: center;
	}
	.muted {
		color: var(--text-muted);
	}
	.run-pill {
		font-size: var(--sbx-card-badge);
		font-weight: 600;
		padding: 0.15rem 0.45rem;
		border-radius: 999px;
		background: var(--bg-tertiary, rgba(100, 100, 100, 0.25));
		color: var(--text-muted);
		text-transform: uppercase;
		letter-spacing: 0.04em;
	}
	.run-pill.run-on {
		background: rgba(16, 185, 129, 0.2);
		color: #10b981;
	}
	.traffic-mini {
		font-size: var(--sbx-card-note);
		color: var(--color-text-muted);
	}
	.list-actions {
		display: flex;
		flex-wrap: nowrap;
		gap: 0.375rem;
		justify-content: center;
		align-items: center;
		white-space: nowrap;
	}
</style>
