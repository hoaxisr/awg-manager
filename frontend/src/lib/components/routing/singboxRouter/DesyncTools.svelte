<script lang="ts">
	import { m } from '$lib/i18n';
	import { Button, Dropdown, type DropdownOption } from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import { formatRelativeTime } from '$lib/utils/format';
	import { shortStrategy } from './desyncDraft';
	import type { SingboxDesyncDiagnostics, SingboxDesyncProbeResult, SingboxDesyncScanRow, SingboxRouterDesyncFields, SingboxRouterOutbound } from '$lib/types';

	interface Props {
		/** Outbound из текущей формы (несохранённой); бросает на неверном вводе. */
		build: () => SingboxRouterOutbound;
		/** Тег сохранённого outbound'а — для диагностики обучения в работающем sing-box. */
		savedTag?: string;
		/** Подставить в форму режим, выбранный по итогам «Подобрать». */
		onApply: (fields: SingboxRouterDesyncFields) => void;
	}
	let { build, savedTag, onApply }: Props = $props();

	let target = $state('');
	let protocol = $state<'tls' | 'http'>('tls');
	const protocolOptions: DropdownOption[] = [
		{ value: 'tls', label: 'TLS' },
		{ value: 'http', label: 'HTTP' },
	];
	let probing = $state(false);
	let probeError = $state('');
	let probe = $state<SingboxDesyncProbeResult | null>(null);

	async function runProbe(): Promise<void> {
		probing = true;
		probeError = '';
		probe = null;
		try {
			probe = await api.singboxRouterDesyncProbe(build(), target.trim(), protocol);
		} catch (e) {
			probeError = (e as Error).message;
		} finally {
			probing = false;
		}
	}

	let scanning = $state(false);
	let scan = $state<SingboxDesyncScanRow[] | null>(null);

	async function runScan(): Promise<void> {
		scanning = true;
		probeError = '';
		scan = null;
		try {
			scan = await api.singboxRouterDesyncScan(build(), target.trim(), protocol);
		} catch (e) {
			probeError = (e as Error).message;
		} finally {
			scanning = false;
		}
	}

	let diag = $state<SingboxDesyncDiagnostics | null>(null);
	let diagLoaded = $state(false);
	let diagBusy = $state(false);
	let diagError = $state('');

	async function loadDiag(clear = false): Promise<void> {
		if (!savedTag) return;
		diagBusy = true;
		diagError = '';
		try {
			diag = await api.singboxDesyncDiagnostics(savedTag, clear);
		} catch (e) {
			diagError = (e as Error).message;
		} finally {
			diagBusy = false;
			diagLoaded = true;
		}
	}
	$effect(() => {
		void loadDiag();
	});

	const entries = $derived(
		Object.entries(diag?.entries ?? {}).sort((a, b) => b[1].updated.localeCompare(a[1].updated)),
	);

	// Лучшая стратегия записи — больше успехов, затем меньше отказов.
	function best(strategies: Record<string, { success: number; failure: number }>): string {
		const list = Object.entries(strategies).sort((a, b) => b[1].success - a[1].success || a[1].failure - b[1].failure);
		return list.length ? `${shortStrategy(list[0][0])} (${list[0][1].success}/${list[0][1].failure})` : '—';
	}
</script>

<div class="form-section form-section-divided">
	<div class="section-label">{m.routing_desync_probe_title()}</div>
	<div class="probe-row">
		<label class="field">
			<div class="lbl">{m.routing_desync_probe_target()}</div>
			<input bind:value={target} placeholder={protocol === 'tls' ? 'example.org:443' : 'https://example.org/'} />
		</label>
		<div class="field">
			<div class="lbl">{m.routing_desync_probe_protocol()}</div>
			<Dropdown bind:value={protocol} options={protocolOptions} fullWidth />
		</div>
	</div>
	<div class="probe-actions">
		<Button variant="secondary" size="sm" onclick={runProbe} disabled={probing || scanning || !target.trim()} loading={probing} type="button">
			{m.routing_desync_probe_run()}
		</Button>
		<Button variant="secondary" size="sm" onclick={runScan} disabled={probing || scanning || !target.trim()} loading={scanning} type="button">
			{m.routing_desync_scan_run()}
		</Button>
	</div>
	{#if probeError}<div class="error">{probeError}</div>{/if}
	{#if probe}
		<div class="result" class:ok={probe.success} class:fail={!probe.success}>
			{#if probe.success}
				{m.routing_desync_probe_ok({ latency: probe.latency_ms.toFixed(0), duration: probe.duration_ms.toFixed(0) })}
				{#if probe.status}· HTTP {probe.status}{/if}
			{:else}
				{m.routing_desync_probe_fail({ error: probe.error ?? '' })}
			{/if}
		</div>
	{/if}
	{#if scan}
		<table class="diag-table">
			<thead>
				<tr><th>{m.routing_desync_scan_mode()}</th><th>{m.routing_desync_scan_result()}</th><th></th></tr>
			</thead>
			<tbody>
				{#each scan as row (row.label)}
					<tr>
						<td class="mono">{row.label}</td>
						<td class:ok-text={row.result.success} class:fail-text={!row.result.success}>
							{row.result.success
								? `${row.result.latency_ms.toFixed(0)} ${m.routing_desync_ms()}${row.result.status ? ` · HTTP ${row.result.status}` : ''}`
								: row.result.error}
						</td>
						<td class="apply-cell">
							{#if row.result.success}
								<Button variant="ghost" size="sm" type="button" onclick={() => onApply({ preset: row.preset, ...(row.mode ? { mode: row.mode } : {}) })}>
									{m.routing_desync_scan_apply()}
								</Button>
							{/if}
						</td>
					</tr>
				{/each}
			</tbody>
		</table>
		<div class="type-hint">{m.routing_desync_scan_hint()}</div>
	{/if}
	<div class="type-hint">{m.routing_desync_probe_hint()}</div>
</div>

{#if savedTag}
	<div class="form-section form-section-divided">
		<div class="section-label">{m.routing_desync_diag_title()}</div>
		{#if diagError}
			<div class="error">{diagError}</div>
		{:else if diagLoaded && !diag}
			<div class="type-hint">{m.routing_desync_diag_absent()}</div>
		{:else if diag}
			<div class="diag-summary">
				{m.routing_desync_diag_summary({ count: diag.entry_count, active: diag.active })}
				{#if diag.current}· {shortStrategy(diag.current)}{/if}
			</div>
			{#if diag.reason}<div class="type-hint">{diag.reason}</div>{/if}
			{#if diag.persistence_error}<div class="error">{diag.persistence_error}</div>{/if}
			{#if entries.length}
				<table class="diag-table">
					<thead>
						<tr><th>{m.routing_desync_diag_key()}</th><th>{m.routing_desync_diag_best()}</th><th>{m.routing_desync_diag_updated()}</th></tr>
					</thead>
					<tbody>
						{#each entries as [key, e] (key)}
							<tr>
								<!-- Хвост «|route:<хеш>» — разбиение по маршруту, читать его незачем. -->
								<td class="mono" title={key}>{key.split('|route:')[0]}</td>
								<td class="mono">{best(e.strategies)}</td>
								<td>{formatRelativeTime(e.updated)}</td>
							</tr>
						{/each}
					</tbody>
				</table>
				{#if diag.entries_truncated}<div class="type-hint">{m.routing_desync_diag_truncated()}</div>{/if}
			{/if}
		{/if}
		<div class="diag-actions">
			<Button variant="ghost" size="sm" onclick={() => loadDiag()} disabled={diagBusy} type="button">{m.routing_desync_diag_refresh()}</Button>
			{#if diag}
				<Button variant="ghost" size="sm" onclick={() => loadDiag(true)} disabled={diagBusy} type="button">{m.routing_desync_diag_clear()}</Button>
			{/if}
		</div>
	</div>
{/if}

<style>
	.probe-row {
		display: grid;
		grid-template-columns: minmax(0, 2fr) minmax(0, 1fr);
		gap: 0.5rem;
	}

	.result {
		font-size: 0.85rem;
		padding: 0.5rem 0.65rem;
		border-radius: 4px;
		word-break: break-word;
	}

	.result.ok {
		color: var(--color-success);
		background: color-mix(in srgb, var(--color-success) 10%, transparent);
	}

	.result.fail {
		color: var(--color-error);
		background: color-mix(in srgb, var(--color-error) 10%, transparent);
	}

	.probe-actions {
		display: flex;
		gap: 0.5rem;
		flex-wrap: wrap;
	}

	.ok-text {
		color: var(--color-success);
	}

	.fail-text {
		color: var(--color-error);
	}

	.apply-cell {
		width: 7rem;
		text-align: right;
	}

	.diag-summary {
		font-size: 0.85rem;
	}

	.diag-table {
		width: 100%;
		border-collapse: collapse;
		font-size: 0.78rem;
		table-layout: fixed;
	}

	.diag-table th,
	.diag-table td {
		text-align: left;
		padding: 0.3rem 0.4rem;
		border-bottom: 1px solid var(--border);
		overflow-wrap: anywhere;
	}

	.diag-table th {
		color: var(--text-muted, var(--muted-text));
		font-weight: 500;
	}

	.mono {
		font-family: var(--font-mono, monospace);
	}

	.diag-actions {
		display: flex;
		gap: 0.5rem;
	}
</style>
