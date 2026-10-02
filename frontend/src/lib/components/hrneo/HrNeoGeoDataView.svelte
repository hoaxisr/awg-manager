<script lang="ts">
	import { onMount } from 'svelte';
	import { api } from '$lib/api/client';
	import type { GeoFileEntry } from '$lib/types';
	import { settings as appSettings, reloadSettings } from '$lib/stores/settings';
	import {
		downloadOutbounds,
		downloadOutboundsLoaded,
		downloadOutboundsLoading,
		downloadOutboundsError,
		downloadOutboundsStatus,
		ensureDownloadOutboundsLoaded,
		resolveDownloadRouteLabel,
	} from '$lib/stores/downloadRoute';
	import { ConfirmModal, Button, Dropdown, IconButton, Modal } from '$lib/components/ui';
	import { formatRelativeTime } from '$lib/utils/format';
	import { copyToClipboard } from '$lib/utils/clipboard';
	import { geoDownloadProgress } from '$lib/stores/geoDownload';
	import CreateIcon from '$lib/components/ui/icons/CreateIcon.svelte';
	import DownloadErrorNotice from '$lib/components/downloads/DownloadErrorNotice.svelte';
	import { Link } from 'lucide-svelte';
	import { m } from '$lib/i18n';

	interface Props {
		files: GeoFileEntry[];
		onrefresh: () => void;
	}

	let { files, onrefresh }: Props = $props();

	let addUrl = $state('');
	let addType = $state<'geoip' | 'geosite'>('geosite');
	let busy = $state<string | null>(null);
	let err = $state('');
	// URL of the in-flight add — captured at submit time so the progress
	// bar in the Add section keeps tracking the original download even
	// if the user starts typing a different URL into the input or the
	// field gets cleared after success. Without this, $geoDownloadProgress
	// lookup keyed by the live `addUrl` value would lose the bar
	// mid-download.
	let inFlightAddUrl = $state<string | null>(null);
	const downloadRouteLabel = $derived(resolveDownloadRouteLabel($appSettings, $downloadOutbounds));
	const routeSettingsReady = $derived(
		$appSettings !== null && $downloadOutboundsLoaded && !$downloadOutboundsLoading,
	);
	const routeSettingsWarning = $derived(
		$downloadOutboundsStatus === 'stale' ? $downloadOutboundsError : '',
	);
	const routeSettingsError = $derived(
		$downloadOutboundsStatus === 'error' ? $downloadOutboundsError : '',
	);
	const routeActionsDisabled = $derived(busy !== null || !routeSettingsReady || !!routeSettingsError);
	const GROUND_ZERRO_GEOIP_URL =
		'https://raw.githubusercontent.com/Ground-Zerro/Geo-Aggregator/main/geodat/geoip_GA.dat';
	const GROUND_ZERRO_GEOSITE_URL =
		'https://raw.githubusercontent.com/Ground-Zerro/Geo-Aggregator/main/geodat/geosite_GA.dat';

	// Progress for the currently in-flight add. Keyed by the submitted
	// URL captured at submit time, not the live input value.
	let progress = $derived(inFlightAddUrl ? ($geoDownloadProgress[inFlightAddUrl] ?? null) : null);
	let progressByPath = $derived($geoDownloadProgress);

	type DownloadOperation = {
		kind: 'add' | 'preset' | 'update' | 'sync';
		target: string;
		routeTag: string;
		routeKind?: 'direct' | 'awg' | 'singbox' | 'subscription';
		routeLabel: string;
	};

	type LastDownload = {
		ok: boolean;
		// Код операции: перевод подписи и текста успеха выводится при рендере.
		action: DownloadOperation['kind'];
		file?: string;
		routeLabel: string;
		// On failure the raw caught error is kept in `error`
		// and humanized at render time via DownloadErrorNotice.
		error?: unknown;
	};

	let activeDownload = $state<DownloadOperation | null>(null);
	let lastDownload = $state<LastDownload | null>(null);

	function currentRoute(): { tag: string; kind?: 'direct' | 'awg' | 'singbox' | 'subscription' } {
		const tag = $appSettings?.download?.routeTag?.trim() || 'direct';
		const savedKind = $appSettings?.download?.routeKind?.trim();
		if (tag === 'direct') {
			return { tag: 'direct', kind: 'direct' };
		}
		const match = $downloadOutbounds.find((ob) => ob.tag === tag && (!savedKind || ob.kind === savedKind));
		return { tag, kind: (savedKind || match?.kind) as 'direct' | 'awg' | 'singbox' | 'subscription' | undefined };
	}

	async function loadRouteDisplayState() {
		if (!$appSettings) {
			await reloadSettings();
		}
		await ensureDownloadOutboundsLoaded();
	}

	function captureDownloadOperation(kind: DownloadOperation['kind'], target: string): DownloadOperation {
		const route = currentRoute();
		return {
			kind,
			target,
			routeTag: route.tag,
			routeKind: route.kind,
			routeLabel: downloadRouteLabel,
		};
	}

	onMount(() => {
		void loadRouteDisplayState();
	});

	function progressFor(url: string) {
		// Progress events are keyed by the source URL; we look up by the
		// entry's stored URL (not the on-disk filename, which may have a
		// '_N' suffix from resolveConflict).
		return progressByPath[url] ?? null;
	}

	function downloadActionLabel(d: LastDownload): string {
		switch (d.action) {
			case 'add':
				return m.hrneo_geo_action_add();
			case 'preset':
				return m.hrneo_geo_action_preset();
			case 'update':
				return m.hrneo_geo_action_update({ file: d.file ?? '' });
			default:
				return m.hrneo_geo_action_sync();
		}
	}

	function downloadSuccessMessage(d: LastDownload): string {
		switch (d.action) {
			case 'add':
				return m.hrneo_geo_msg_added();
			case 'preset':
				return m.hrneo_geo_msg_preset();
			case 'update':
				return m.hrneo_geo_msg_updated();
			default:
				return m.hrneo_geo_msg_synced();
		}
	}

	function fmtPercent(p: { downloaded: number; total: number }): string {
		if (p.total <= 0) return '';
		return `${Math.min(100, Math.round((p.downloaded / p.total) * 100))}%`;
	}

	async function add() {
		if (!routeSettingsReady || routeSettingsError) return;
		const submitted = addUrl.trim();
		if (!submitted) return;
		const op = captureDownloadOperation('add', submitted);
		busy = 'add';
		err = '';
		lastDownload = null;
		inFlightAddUrl = submitted;
		activeDownload = op;
		try {
			await api.addGeoFile(addType, submitted, { tag: op.routeTag, kind: op.routeKind });
			addUrl = '';
			lastDownload = {
				ok: true,
				action: 'add',
				routeLabel: op.routeLabel,
			};
			onrefresh();
		} catch (e: unknown) {
			lastDownload = {
				ok: false,
				action: 'add',
				routeLabel: op.routeLabel,
				error: e,
			};
		} finally {
			busy = null;
			inFlightAddUrl = null;
			activeDownload = null;
		}
	}

	async function addPreset(type: 'geoip' | 'geosite', url: string) {
		if (!routeSettingsReady || routeSettingsError) return;
		const op = captureDownloadOperation('preset', url);
		busy = 'add';
		err = '';
		lastDownload = null;
		inFlightAddUrl = url;
		activeDownload = op;
		try {
			await api.addGeoFile(type, url, { tag: op.routeTag, kind: op.routeKind });
			lastDownload = {
				ok: true,
				action: 'preset',
				routeLabel: op.routeLabel,
			};
			onrefresh();
		} catch (e: unknown) {
			lastDownload = {
				ok: false,
				action: 'preset',
				routeLabel: op.routeLabel,
				error: e,
			};
		} finally {
			busy = null;
			inFlightAddUrl = null;
			activeDownload = null;
		}
	}

	async function update(path: string) {
		if (!routeSettingsReady || routeSettingsError) return;
		const op = captureDownloadOperation('update', path);
		busy = path;
		err = '';
		lastDownload = null;
		activeDownload = op;
		try {
			await api.updateGeoFile(path, { tag: op.routeTag, kind: op.routeKind });
			lastDownload = {
				ok: true,
				action: 'update',
				file: fileName(path),
				routeLabel: op.routeLabel,
			};
			onrefresh();
		} catch (e: unknown) {
			lastDownload = {
				ok: false,
				action: 'update',
				file: fileName(path),
				routeLabel: op.routeLabel,
				error: e,
			};
		} finally {
			busy = null;
			activeDownload = null;
		}
	}

	let pendingDelete = $state<GeoFileEntry | null>(null);
	let pendingTakeControl = $state<GeoFileEntry | null>(null);
	let sourceModalFile = $state<GeoFileEntry | null>(null);
	let copiedSource = $state(false);

	async function copySource() {
		if (!sourceModalFile) return;
		copiedSource = await copyToClipboard(sourceModalFile.url);
	}
	let expandedPaths = $state<Set<string>>(new Set());

	function requestRemove(f: GeoFileEntry) {
		pendingDelete = f;
	}

	async function syncFromHR() {
		if (!routeSettingsReady || routeSettingsError) return;
		const op = captureDownloadOperation('sync', 'all');
		busy = 'sync';
		err = '';
		lastDownload = null;
		activeDownload = op;
		const notes: string[] = [];
		try {
			try {
				await api.rescanGeoFiles();
			} catch (e: unknown) {
				// Нет HR / hrneo.conf — всё равно обновляем уже известные файлы.
				notes.push(e instanceof Error ? e.message : String(e));
			}
			// Список после rescan — HR External видны даже если update упадёт.
			await onrefresh();

			try {
				const upd = await api.updateGeoFile('', { tag: op.routeTag, kind: op.routeKind });
				await onrefresh();
				if (upd.partial && upd.error) {
					notes.push(
						upd.updated > 0
							? m.hrneo_geo_sync_partial({ updated: upd.updated, error: upd.error })
							: upd.error,
					);
				}
			} catch (e: unknown) {
				await onrefresh();
				notes.push(e instanceof Error ? e.message : String(e));
			}

			if (notes.length > 0) {
				lastDownload = {
					ok: false,
					action: 'sync',
					routeLabel: op.routeLabel,
					error: notes.join('; '),
				};
			} else {
				lastDownload = {
					ok: true,
					action: 'sync',
					routeLabel: op.routeLabel,
				};
			}
		} finally {
			busy = null;
			activeDownload = null;
		}
	}

	async function confirmTakeControl() {
		if (!pendingTakeControl) return;
		const f = pendingTakeControl;
		busy = f.path;
		err = '';
		try {
			await api.takeGeoFileControl(f.path);
			pendingTakeControl = null;
			onrefresh();
		} catch (e: unknown) {
			err = e instanceof Error ? e.message : String(e);
		} finally {
			busy = null;
		}
	}

	function canUpdate(f: GeoFileEntry): boolean {
		// external — управляется HR Neo; «локальный» (без url) обновлять неоткуда
		// (нет источника), иначе «Обновить» затёр бы файл дефолтным Ground-Zerro.
		if (f.external) return false;
		return !!f.url;
	}

	async function confirmRemove() {
		if (!pendingDelete) return;
		const f = pendingDelete;
		busy = f.path;
		err = '';
		try {
			await api.deleteGeoFile(f.path);
			pendingDelete = null;
			onrefresh();
		} catch (e: unknown) {
			err = e instanceof Error ? e.message : String(e);
		} finally {
			busy = null;
		}
	}

	function humanSize(n: number): string {
		if (n < 1024) return `${n} B`;
		if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
		return `${(n / 1024 / 1024).toFixed(1)} MB`;
	}

	function fileName(p: string): string {
		return p.split('/').pop() ?? p;
	}

	function fileDir(p: string): string {
		const base = fileName(p);
		if (!base || p === base) return '';
		return p.slice(0, p.length - base.length);
	}

	function togglePathExpanded(path: string) {
		const next = new Set(expandedPaths);
		if (next.has(path)) {
			next.delete(path);
		} else {
			next.add(path);
		}
		expandedPaths = next;
	}
</script>

{#snippet createIcon()}
	<CreateIcon />
{/snippet}

<div class="geo-pane">
	<header class="pane-header">
		<div class="pane-title">
			<h2>{m.hrneo_sidebar_geodata()}</h2>
			<span class="pane-meta">{m.hrneo_geo_files_count({ count: files.length })}</span>
		</div>

		<div class="pane-actions">
			<Button
				variant="secondary"
				size="sm"
				fullWidth
				disabled={routeActionsDisabled}
				loading={busy === 'sync'}
				onclick={syncFromHR}
				title={m.hrneo_geo_sync_title()}
			>
				{m.hrneo_geo_sync()}
			</Button>
		</div>
	</header>

	{#if err}<div class="error-banner">{err}</div>{/if}

    <div>
		{#if routeSettingsError}
		<div class="route-status route-status-error">
			{m.hrneo_geo_route_error({ error: routeSettingsError })}
		</div>
		{:else if !routeSettingsReady}
		<div class="route-status route-status-live">
			{m.hrneo_geo_route_loading()}
		</div>
		{:else if routeSettingsWarning}
		<div class="route-status route-status-warn">
			{m.hrneo_geo_route_warn_prefix()}<strong>{downloadRouteLabel}</strong>{m.hrneo_geo_route_warn_suffix({ warning: routeSettingsWarning })}
		</div>
	{/if}
	{#if !activeDownload && lastDownload}
		{#if lastDownload.ok}
			<div class="route-status route-status-ok">
				{m.hrneo_geo_last_ok({ action: downloadActionLabel(lastDownload), route: lastDownload.routeLabel, message: downloadSuccessMessage(lastDownload) })}
			</div>
		{:else}
			<div class="route-status route-status-error">
				<span class="route-status-head">
					{m.hrneo_geo_last_failed({ action: downloadActionLabel(lastDownload), route: lastDownload.routeLabel })}
				</span>
				<DownloadErrorNotice error={lastDownload.error} />
			</div>
		{/if}
	{/if}
	</div>
	
	{#if files.length === 0}
		<div class="empty">{m.hrneo_geo_empty()}</div>
	{:else}
		<div class="files">
			{#each files as f (f.path)}
				{@const fp = progressFor(f.url)}
				<div class="file-row">
					<div class="file-info">
						<span class="file-type type-{f.type}">{f.type}</span>
						<button
							type="button"
							class="file-name"
							title={expandedPaths.has(f.path) ? m.hrneo_geo_hide_path() : f.path}
							onclick={() => togglePathExpanded(f.path)}
						>
							{#if expandedPaths.has(f.path)}
								<span class="file-path">{fileDir(f.path)}</span><span
									class="file-basename">{fileName(f.path)}</span
								>
							{:else}
								{fileName(f.path)}
							{/if}
						</button>
						{#if f.external}
							<span
								class="file-external"
								title={m.hrneo_geo_external_title()}
							>External</span>
						{/if}
						<span class="file-meta">{humanSize(f.size)} · {m.routing_singbox_geo_tags_count({ count: f.tagCount })} · {formatRelativeTime(f.updated)}</span>
						{#if f.external}
							<!-- External (HR Neo) — источник нам неизвестен (бэкенд лишь
							     подставляет догадочный default-URL), показываем только бейдж External -->
						{:else if f.url}
							<IconButton
								ariaLabel={m.hrneo_geo_source_aria()}
								title={m.hrneo_geo_source_title()}
								onclick={() => { sourceModalFile = f; copiedSource = false; }}
							>
								<Link size={14} />
							</IconButton>
						{:else}
							<span class="file-local" title={m.hrneo_geo_local_title()}>{m.hrneo_geo_local()}</span>
						{/if}
						{#if busy === f.path && fp}
							<span class="row-progress">
								{#if fp.phase === 'download'}
									{fmtPercent(fp)} {humanSize(fp.downloaded)}
								{:else if fp.phase === 'validate'}
									{m.hrneo_geo_validating()}
								{/if}
							</span>
						{/if}
					</div>
					<div class="file-actions">
						{#if f.external}
							<Button
								variant="secondary"
								size="sm"
								disabled={busy !== null}
								onclick={() => (pendingTakeControl = f)}
							>
								{m.hrneo_geo_take_control()}
							</Button>
						{/if}
						{#if canUpdate(f)}
							<Button
								variant="secondary"
								size="sm"
								disabled={routeActionsDisabled}
								loading={busy === f.path}
								onclick={() => update(f.path)}
							>
								{m.routing_page_refresh()}
							</Button>
						{/if}
						<Button
							variant="secondary"
							size="sm"
							disabled={busy !== null}
							onclick={() => requestRemove(f)}
						>
							{m.common_delete()}
						</Button>
					</div>
				</div>
			{/each}
		</div>
	{/if}

	<div class="add-form">
		<div class="form-label">{m.hrneo_geo_presets()}</div>
		<div class="preset-row">
			<Button
				variant="secondary"
				size="sm"
				disabled={routeActionsDisabled}
				onclick={() => addPreset('geoip', GROUND_ZERRO_GEOIP_URL)}
			>
				+ geoip_GA.dat
			</Button>
			<Button
				variant="secondary"
				size="sm"
				disabled={routeActionsDisabled}
				onclick={() => addPreset('geosite', GROUND_ZERRO_GEOSITE_URL)}
			>
				+ geosite_GA.dat
			</Button>
			<span class="preset-hint">{m.hrneo_geo_preset_hint()}</span>
		</div>
		<div class="form-label form-label-spaced">{m.hrneo_geo_add_by_url()}</div>
		<div class="add-row">
			<div class="add-type-select">
				<Dropdown
					bind:value={addType}
					options={[
						{ value: 'geosite' as const, label: 'geosite' },
						{ value: 'geoip' as const, label: 'geoip' },
					]}
					disabled={routeActionsDisabled}
					fullWidth
				/>
			</div>
			<input
				class="form-input"
				type="url"
				placeholder="https://.../{addType}.dat"
				bind:value={addUrl}
				disabled={routeActionsDisabled}
			/>
			<Button
				variant="primary"
				size="sm"
				onclick={add}
				disabled={!addUrl.trim() || routeActionsDisabled}
				iconBefore={createIcon}
				loading={busy === 'add'}
			>
				{m.routing_add()}
			</Button>
		</div>
		{#if busy === 'add'}
			<div class="busy-hint">
				{#if progress?.phase === 'download'}
					{progress.total > 0
						? m.hrneo_geo_downloading_of({
								route: activeDownload?.routeLabel ?? downloadRouteLabel,
								percent: fmtPercent(progress),
								downloaded: humanSize(progress.downloaded),
								total: humanSize(progress.total),
							})
						: m.hrneo_geo_downloading({
								route: activeDownload?.routeLabel ?? downloadRouteLabel,
								percent: fmtPercent(progress),
								downloaded: humanSize(progress.downloaded),
							})}
				{:else if progress?.phase === 'validate'}
					{m.hrneo_geo_validating_file()}
				{:else}
					{m.hrneo_geo_connecting({ route: activeDownload?.routeLabel ?? downloadRouteLabel })}
				{/if}
				<div class="progress-bar">
					{#if progress && progress.total > 0}
						<div
							class="progress-fill"
							style="width: {Math.min(100, (progress.downloaded / progress.total) * 100)}%"
						></div>
					{:else}
						<div class="progress-fill indeterminate"></div>
					{/if}
				</div>
			</div>
		{/if}
		<div class="form-hint">
			{m.hrneo_geo_type_hint_prefix()}<code>{addType}</code>{m.hrneo_geo_type_hint_suffix()}
		</div>
	</div>
</div>

{#if pendingTakeControl}
	{@const pt = pendingTakeControl}
	<ConfirmModal
		open={true}
		title={m.hrneo_geo_take_control()}
		message={m.hrneo_geo_take_control_message({ file: fileName(pt.path) })}
		secondary={m.hrneo_geo_take_control_secondary()}
		confirmLabel={m.hrneo_geo_take_control_confirm()}
		variant="primary"
		busy={busy === pt.path}
		onConfirm={confirmTakeControl}
		onClose={() => (pendingTakeControl = null)}
	/>
{/if}

{#if pendingDelete}
	{@const pd = pendingDelete}
	{@const hrKey = pd.type === 'geosite' ? 'GeoSiteFile' : 'GeoIPFile'}
	<ConfirmModal
		open={true}
		title={m.hrneo_geo_delete_title()}
		message={pd.external
			? m.hrneo_geo_delete_external_message({ file: fileName(pd.path) })
			: m.hrneo_geo_delete_message({ file: fileName(pd.path) })}
		filePath={pd.path}
		secondary={m.hrneo_geo_delete_secondary({ key: hrKey, path: pd.path })}
		busy={busy === pd.path}
		onConfirm={confirmRemove}
		onClose={() => (pendingDelete = null)}
	/>
{/if}

{#if sourceModalFile}
	<Modal open title={m.hrneo_geo_source_modal_title()} size="md" onclose={() => (sourceModalFile = null)}>
		<div class="source-modal">
			<code class="source-url">{sourceModalFile.url}</code>
			<Button variant="secondary" size="sm" onclick={copySource}>
				{copiedSource ? m.servers_conf_copied() : m.diag_logs_copy()}
			</Button>
		</div>
	</Modal>
{/if}

<style>
	.geo-pane {
		--geo-block-gap: 0.875rem;
		display: flex;
		flex-direction: column;
		gap: var(--geo-block-gap);
	}

	.pane-header {
		display: grid;
		grid-template-columns: minmax(0, 1fr) auto;
		align-items: center;
		gap: 10px;
		padding-bottom: 10px;
		border-bottom: 1px solid var(--border);
	}

	.pane-title {
		display: flex;
		align-items: baseline;
		gap: 10px;
		min-width: 0;
	}

	.pane-actions {
		display: flex;
		justify-content: flex-end;
		min-width: 0;
	}

	.pane-actions :global(.btn) {
		min-width: 150px;
	}
	.pane-header h2 {
		margin: 0;
		font-size: 1.0625rem;
		color: var(--text-primary);
	}
	.pane-meta {
		color: var(--text-muted);
		font-size: 0.8125rem;
	}

	.error-banner {
		background: rgba(247, 118, 142, 0.1);
		border-left: 3px solid var(--error);
		color: var(--error);
		padding: 8px 12px;
		border-radius: 4px;
		font-size: 0.8125rem;
	}

	.empty {
		padding: 24px;
		text-align: center;
		color: var(--text-muted);
		font-style: italic;
		background: var(--bg-secondary);
		border: 1px dashed var(--border);
		border-radius: 8px;
	}

	.files {
		display: flex;
		flex-direction: column;
		gap: 6px;
	}

	.file-row {
		display: flex;
		justify-content: space-between;
		align-items: center;
		gap: 12px;
		padding: 10px 12px;
		background: var(--bg-secondary);
		border: 1px solid var(--border);
		border-radius: 8px;
	}

	.file-info {
		display: flex;
		align-items: center;
		gap: 10px;
		min-width: 0;
		flex: 1;
	}

	.file-type {
		font-size: 0.6875rem;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		font-weight: 600;
		padding: 2px 8px;
		border-radius: 10px;
	}
	.type-geosite {
		background: rgba(122, 162, 247, 0.15);
		color: var(--accent);
	}
	.type-geoip {
		background: rgba(125, 207, 255, 0.15);
		color: var(--info);
	}

	.file-name {
		font-family: ui-monospace, monospace;
		color: var(--text-primary);
		font-size: 0.875rem;
		cursor: pointer;
		padding: 0;
		border: none;
		background: none;
		text-align: left;
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.file-name:hover .file-basename {
		text-decoration: underline;
	}

	.file-path {
		color: var(--text-muted);
	}

	.file-basename {
		color: var(--text-primary);
	}

	.file-meta {
		color: var(--text-muted);
		font-size: 0.75rem;
	}

	.file-local { font-size: 0.75rem; color: var(--text-secondary); }
	.source-modal { display: flex; flex-direction: column; gap: 0.75rem; }
	.source-url { word-break: break-all; font-size: 0.8125rem; padding: 0.5rem; background: var(--color-bg-secondary); border-radius: var(--radius-sm); }

	.file-external {
		font-size: 0.6875rem;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		font-weight: 600;
		padding: 2px 8px;
		border-radius: 10px;
		background: rgba(245, 158, 11, 0.15);
		color: var(--warning, #f59e0b);
		cursor: help;
	}

	.file-actions {
		display: flex;
		gap: 6px;
		flex-shrink: 0;
		align-items: center;
		justify-content: flex-end;
	}

	.file-actions :global(.btn) {
		width: auto;
		min-width: auto;
	}

	.add-form {
		padding: 12px;
		background: var(--bg-secondary);
		border: 1px solid var(--border);
		border-radius: 8px;
	}

	.form-label {
		display: block;
		font-size: 0.8125rem;
		font-weight: 500;
		color: var(--text-primary);
		margin-bottom: 6px;
	}

	.form-label-spaced {
		margin-top: var(--geo-block-gap);
	}

	.preset-row {
		display: flex;
		align-items: center;
		gap: 8px;
		flex-wrap: wrap;
	}

	.preset-hint {
		color: var(--text-muted);
		font-size: 0.75rem;
	}

	.add-row {
		display: grid;
		grid-template-columns: auto 1fr auto;
		gap: 6px;
		align-items: center;
	}

	.route-box {
		padding: 12px;
		border: 1px solid var(--border);
		border-radius: 8px;
		background: var(--bg-primary);
	}

	.route-status {
		margin-top: 0.75rem;
		padding: 8px 10px;
		border-radius: 6px;
		font-size: 0.8125rem;
	}

	.route-status-live {
		background: rgba(122, 162, 247, 0.1);
		color: var(--text-primary);
		border-left: 3px solid var(--accent);
	}

	.route-status-ok {
		background: rgba(74, 222, 128, 0.1);
		color: var(--text-primary);
		border-left: 3px solid var(--success, #4ade80);
	}

	.route-status-error {
		background: rgba(247, 118, 142, 0.1);
		color: var(--error);
		border-left: 3px solid var(--error);
	}

	.route-status-warn {
		background: rgba(245, 158, 11, 0.12);
		color: var(--warning, #f59e0b);
		border-left: 3px solid var(--warning, #f59e0b);
	}

	.route-status-error {
		display: flex;
		flex-direction: column;
		gap: 0.35rem;
	}

	.route-status-head {
		font-weight: 500;
	}

	.add-type-select {
		min-width: 110px;
	}

	.busy-hint {
		margin-top: 0.75rem;
		padding: 8px 10px;
		background: rgba(122, 162, 247, 0.1);
		border-left: 3px solid var(--accent);
		color: var(--text-primary);
		font-size: 0.8125rem;
		border-radius: 4px;
	}

	.form-hint {
		margin-top: 0.75rem;
		color: var(--text-muted);
		font-size: 0.75rem;
	}
	.form-hint code {
		background: var(--bg-tertiary);
		padding: 0 4px;
		border-radius: 3px;
		font-family: ui-monospace, monospace;
	}

	.progress-bar {
		margin-top: 6px;
		height: 6px;
		background: var(--bg-tertiary);
		border-radius: 3px;
		overflow: hidden;
	}
	.progress-fill {
		height: 100%;
		background: var(--accent);
		border-radius: 3px;
		transition: width 0.2s ease-out;
	}
	.progress-fill.indeterminate {
		width: 30%;
		animation: indeterminate 1.4s linear infinite;
	}
	@keyframes indeterminate {
		0% {
			margin-left: -30%;
		}
		100% {
			margin-left: 100%;
		}
	}

	.row-progress {
		color: var(--accent);
		font-size: 0.75rem;
		font-family: ui-monospace, monospace;
	}

	@media (max-width: 640px) {
		.pane-header {
			grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
			align-items: center;
		}

		.pane-title {
			min-width: 0;
		}

		.pane-actions {
			width: 100%;
		}

		.pane-actions :global(.btn) {
			width: 100%;
			min-width: 0;
		}

		.file-row {
			flex-direction: column;
			align-items: stretch;
			gap: 8px;
		}
		.file-info {
			flex-wrap: wrap;
			row-gap: 4px;
		}
		.file-actions {
			display: grid;
			grid-template-columns: repeat(2, minmax(0, 1fr));
			gap: 6px;
			width: 100%;
			justify-content: stretch;
		}

		.file-actions :global(.btn) {
			width: 100%;
			min-width: 0;
			height: 28px;
			min-height: 28px;
			max-height: 28px;
		}

		.file-actions :global(.btn):only-child {
			grid-column: 1 / -1;
		}

		.file-actions :global(.btn:first-child:nth-last-child(3)) {
			grid-column: 1 / -1;
		}

		.preset-row {
			display: grid;
			grid-template-columns: repeat(2, minmax(0, 1fr));
			gap: 6px;
			align-items: stretch;
		}

		.preset-row :global(.btn) {
			width: 100%;
			min-width: 0;
			height: 28px;
			min-height: 28px;
			max-height: 28px;
		}

		.preset-hint {
			grid-column: 1 / -1;
		}

		.add-row {
			grid-template-columns: 1fr;
		}
	}
</style>
