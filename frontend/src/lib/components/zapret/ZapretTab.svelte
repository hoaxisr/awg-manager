<script lang="ts">
	import { untrack } from 'svelte';
	import { formatLocale, m, uiText, type UiText } from '$lib/i18n';
	import { api } from '$lib/api/client';
	import { runZapretControl } from './control';
	import { notifications } from '$lib/stores/notifications';
	import { zapretStatus } from '$lib/stores/zapret';
	import type {
		ZapretControlAction,
		ZapretHostlistContent,
		ZapretHostlistId,
		ZapretHostlistInfo,
		ZapretStrategies
	} from '$lib/types';
	import { Button, IconButton } from '$lib/components/ui';
	import { RotateCw, TriangleAlert } from 'lucide-svelte';
	import ZapretStatusCard from './ZapretStatusCard.svelte';
	import ZapretStrategyList from './ZapretStrategyList.svelte';
	import ZapretHostlistPanel from './ZapretHostlistPanel.svelte';
	import { zapretWarningText } from './warnings';

	let status = $derived($zapretStatus.data);
	let statusLoading = $derived($zapretStatus.lastFetchedAt === 0 && $zapretStatus.status !== 'error');
	let strategies = $state<ZapretStrategies | null>(null);
	let stratLoading = $state(false);
	let stratError = $state<UiText | null>(null);
	let stratBusy = $state(false);
	let hostlists = $state<ZapretHostlistInfo[]>([]);
	let activeHost = $state<ZapretHostlistId>('user');
	let hostContent = $state<ZapretHostlistContent | null>(null);
	let hostLoading = $state(false);
	let hostContentLoadError = $state(false);
	let hostlistsLoading = $state(false);
	let hostlistsLoadError = $state(false);
	let editorGeneration = 0;
	let hostRequest = 0;
	let strategyRequest = 0;
	let hostSaving = $state(false);
	let controlBusy = $state(false);
	let restartRequired = $state(false);
	let installed = $derived(status?.installed === true);
	let editorBase = $derived(installed ? status?.base : undefined);
	let activeHostInfo = $derived(hostlists.find((host) => host.id === activeHost) ?? null);

	async function loadStrategies() {
		if (!installed) return;
		const generation = editorGeneration;
		const request = ++strategyRequest;
		stratLoading = true;
		stratError = null;
		try {
			const result = await api.getZapretStrategies();
			if (generation === editorGeneration && request === strategyRequest) strategies = result;
		} catch (error) {
			if (generation === editorGeneration && request === strategyRequest) {
				stratError = error instanceof Error ? error.message : () => m.zapret_error_read_strategies();
			}
		} finally {
			if (generation === editorGeneration && request === strategyRequest) stratLoading = false;
		}
	}

	async function loadHostContent() {
		const requested = activeHost;
		const generation = editorGeneration;
		const request = ++hostRequest;
		const info = hostlists.find((host) => host.id === requested);
		hostContent = null;
		hostContentLoadError = false;
		hostLoading = !!info?.editable;
		if (!info?.editable) return;
		try {
			const result = await api.getZapretHostlist(requested);
			if (generation === editorGeneration && request === hostRequest) hostContent = result;
		} catch (error) {
			if (generation === editorGeneration && request === hostRequest) {
				hostContentLoadError = true;
				notifications.error(error instanceof Error ? error.message : m.zapret_error_read_hostlist());
			}
		} finally {
			if (generation === editorGeneration && request === hostRequest) hostLoading = false;
		}
	}

	async function loadHostlists() {
		if (!installed) return;
		const generation = editorGeneration;
		hostlistsLoading = true;
		hostlistsLoadError = false;
		try {
			const result = await api.getZapretHostlists();
			if (generation === editorGeneration) hostlists = result;
		} catch (error) {
			if (generation === editorGeneration) {
				hostlistsLoadError = true;
				notifications.error(error instanceof Error ? error.message : m.zapret_error_read_hostlists());
			}
		} finally {
			if (generation === editorGeneration) hostlistsLoading = false;
		}
	}

	async function saveHostlist(content: string): Promise<boolean> {
		if (status?.conflict) return false;
		const generation = editorGeneration;
		const requested = activeHost;
		hostSaving = true;
		try {
			if (!hostContent || hostContent.id !== activeHost) return false;
			const saved = await api.saveZapretHostlist(activeHost, content, hostContent.revision);
			if (generation !== editorGeneration) return false;
			restartRequired = !!saved.restartError;
			zapretStatus.invalidate();
			if (saved.restartError) {
				notifications.error(m.zapret_hostlist_restart_failed({ error: saved.restartError }));
			} else {
				notifications.success(m.zapret_hostlist_saved());
			}
			if (activeHost === requested) {
				await Promise.all([loadHostContent(), loadHostlists()]);
			}
			return true;
		} catch (error) {
			if (generation !== editorGeneration) return false;
			notifications.error(error instanceof Error ? error.message : m.zapret_error_save_hostlist());
			return false;
		} finally {
			if (generation === editorGeneration) hostSaving = false;
		}
	}

	async function move(from: number, to: number) {
		const items = strategies?.items ?? [];
		if (stratBusy || status?.conflict || !strategies || to < 0 || to >= items.length) return;
		const generation = editorGeneration;
		stratBusy = true;
		try {
			const result = await api.reorderZapretStrategy(from, to, strategies.revision);
			if (generation !== editorGeneration) return;
			strategies = result;
			restartRequired = true;
			notifications.success(m.zapret_strategy_reordered({
				from: (from + 1).toLocaleString(formatLocale()),
				to: (to + 1).toLocaleString(formatLocale())
			}));
		} catch (error) {
			notifications.error(error instanceof Error ? error.message : m.zapret_error_reorder_strategies());
		} finally {
			if (generation === editorGeneration) stratBusy = false;
		}
	}

	async function oncontrol(action: ZapretControlAction) {
		if (controlBusy || status?.conflict || !status?.initScript) return;
		controlBusy = true;
		try {
			const fresh = await runZapretControl(action, status.base ?? '');
			if (fresh && (action === 'start' || action === 'restart' || action === 'restart-daemons') && fresh.running) {
				restartRequired = false;
			}
		} finally {
			controlBusy = false;
		}
	}

	$effect(() => {
		const base = editorBase;
		const available = installed;
		untrack(() => {
			editorGeneration++;
			strategyRequest++;
			hostRequest++;
			strategies = null;
			hostlists = [];
			hostlistsLoadError = false;
			hostContent = null;
			hostContentLoadError = false;
			stratError = null;
			stratLoading = false;
			hostLoading = false;
			hostlistsLoading = false;
			stratBusy = false;
			hostSaving = false;
			restartRequired = false;
			if (available && base) {
				void loadStrategies();
				void loadHostlists();
			}
		});
	});

	$effect(() => {
		const selected = activeHost;
		const editable = activeHostInfo?.editable;
		if (installed && selected && editable !== undefined) {
			untrack(() => void loadHostContent());
		}
	});

	const HOST_LABELS = $derived<Record<ZapretHostlistId, string>>({
		user: m.zapret_hostlist_label_user(),
		exclude: m.zapret_hostlist_label_exclude(),
		auto: m.zapret_hostlist_label_auto(),
		main: m.zapret_hostlist_label_main()
	});
	let restartLabel = $derived(status?.running ? m.zapret_apply_restart_aria() : m.zapret_apply_start_aria());
	let restartTitle = $derived(status?.running ? m.zapret_apply_restart_title() : m.zapret_apply_start_title());
</script>

<section class="zap-tab">
	<ZapretStatusCard {status} loading={statusLoading} busy={controlBusy} {oncontrol} />

	{#if !statusLoading && !installed}
		<div class="empty-state-rich">
			<p>{m.zapret_not_installed_description()}</p>
			<p class="hint">{m.zapret_not_installed_detection_hint()}</p>
		</div>
	{:else}
		<div class="workspace">
			<section class="column" aria-label={m.zapret_tab_strategies()}>
				<header class="column-header">
					<h3>{m.zapret_tab_strategies()}</h3>
					{#if restartRequired}
						<span class="restart-hint">{m.zapret_restart_required()}</span>
						<IconButton
							ariaLabel={restartLabel}
							title={restartTitle}
							size="md"
							loading={controlBusy}
							disabled={!status?.initScript || status?.conflict}
							onclick={() => void oncontrol(status?.running ? 'restart-daemons' : 'start')}
						>
							<RotateCw aria-hidden="true" />
						</IconButton>
					{/if}
				</header>
				{#if stratError}
					<p class="err">{uiText(stratError)}</p>
				{:else if stratLoading && !strategies}
					<p class="loading">{m.zapret_loading_strategies()}</p>
				{:else if (strategies?.items.length ?? 0) === 0}
					<div class="empty-state-rich">
						<p>{m.zapret_no_strategies({ optVar: strategies?.optVar ?? 'NFQWS2_OPT', config: strategies?.config ?? '' })}</p>
						<p class="hint">{m.zapret_no_strategies_hint()}</p>
					</div>
				{:else}
					{#each strategies?.parseWarnings ?? [] as warning, index (`${warning.code}-${index}`)}
						<p class="warn"><TriangleAlert size={14} /><span>{zapretWarningText(warning)}</span></p>
					{/each}
					<p class="hint-block">{m.zapret_order_hint({ config: strategies?.config ?? '' })}</p>
					<ZapretStrategyList
						items={strategies?.items ?? []}
						busy={stratBusy}
						conflict={status?.conflict ?? false}
						onmove={move}
					/>
				{/if}
			</section>

			<section class="column host-column" aria-label={m.zapret_tab_hostlists()}>
				<header class="column-header"><h3>{m.zapret_tab_hostlists()}</h3></header>
				<div class="host-picker">
					{#each hostlists as host (host.id)}
						<button
							type="button"
							class="chip"
							class:chip-active={host.id === activeHost}
							aria-pressed={host.id === activeHost}
							onclick={() => (activeHost = host.id)}
						>
							{HOST_LABELS[host.id]}
						</button>
					{/each}
				</div>
				{#if activeHostInfo}
					{#key `${status?.base}:${activeHost}`}
					<ZapretHostlistPanel
						info={activeHostInfo}
						content={hostContent?.content ?? ''}
						loading={hostLoading}
						loadError={hostContentLoadError}
						saving={hostSaving || !!status?.conflict}
						onload={() => void loadHostContent()}
						onsave={saveHostlist}
					/>
					{/key}
				{:else if hostlistsLoading || statusLoading}
					<p class="loading">{m.zapret_loading_hostlists()}</p>
				{:else if hostlistsLoadError}
					<div class="empty-state-rich" role="alert">
						<p>{m.zapret_error_read_hostlists()}</p>
						<Button variant="secondary" size="sm" onclick={() => void loadHostlists()}>{m.common_refresh()}</Button>
					</div>
				{:else}
					<div class="empty-state-rich">
						<p>{m.zapret_no_hostlists()}</p>
						<Button variant="secondary" size="sm" onclick={() => void loadHostlists()}>{m.common_refresh()}</Button>
					</div>
				{/if}
			</section>
		</div>
	{/if}
</section>

<style>
	.zap-tab { display: flex; flex-direction: column; gap: 12px; }
	.workspace { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); align-items: start; gap: 1rem; }
	.column { display: flex; flex-direction: column; gap: 0.625rem; min-width: 0; }
	.column-header { display: flex; align-items: center; gap: 0.5rem; min-height: 2rem; }
	.column-header h3 { margin: 0; font-size: 0.8125rem; font-weight: 600; color: var(--text-primary); }
	.restart-hint { margin-left: auto; font-size: 0.6875rem; color: var(--warning, #f59e0b); }
	.err, .loading { margin: 0; font-size: 0.75rem; color: var(--text-muted); }
	.err { color: var(--error, #ef4444); }
	.warn { display: flex; align-items: flex-start; gap: 6px; margin: 0; padding: 8px 10px; border-radius: 6px; background: color-mix(in srgb, var(--warning, #f59e0b) 12%, transparent); font-size: 0.75rem; line-height: 1.4; color: var(--text-primary); }
	.warn :global(svg) { flex-shrink: 0; margin-top: 2px; color: var(--warning, #f59e0b); }
	.hint-block, .hint { margin: 0; font-size: 0.6875rem; line-height: 1.45; color: var(--text-muted); }
	.host-picker { display: flex; flex-wrap: wrap; gap: 6px; }
	@media (max-width: 860px) { .workspace { grid-template-columns: minmax(0, 1fr); } }
</style>
