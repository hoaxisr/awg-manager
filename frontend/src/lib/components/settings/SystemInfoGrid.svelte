<script lang="ts">
	import { browser } from '$app/environment';
	import type { SystemInfo } from '$lib/types';
	import type { UsageLevel } from '$lib/types/usageLevel';
	import SettingsSectionLabel from './SettingsSectionLabel.svelte';
	import { Router, ChevronDown } from 'lucide-svelte';

	interface Props {
		systemInfo: SystemInfo;
		usageLevel: UsageLevel;
		onrefresh?: () => void;
		refreshing?: boolean;
		lastUpdated?: string | null;
		autoRefreshMs?: number;
	}

	let { systemInfo, usageLevel, onrefresh, refreshing = false, lastUpdated = null, autoRefreshMs = 0 }: Props = $props();
	let detailsOpen = $state(false);
	const DETAILS_KEY = 'awgm.settings.system.detailsOpen';
	const COLLAPSED_KEY = 'awgm.settings.system.collapsed';
	const details = $derived(systemInfo.routerDetails);
	const routerMainTitle = $derived.by(() => {
		const base = details?.modelDisplay || details?.model || systemInfo.kernelModuleModel || '—';
		const region = details?.region?.trim();
		if (!region) return base;
		return base.replace(new RegExp(`\\s*\\(${region.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}\\)\\s*$`), '').trim();
	});
	const routerRegionTitle = $derived.by(() => {
		const region = details?.region?.trim();
		return region ? `(${region})` : '';
	});
	const osTitle = $derived(details?.firmwareRelease || systemInfo.firmwareVersion || systemInfo.keeneticOS || '—');
	const cpuModelLine = $derived.by(() => {
		if (!details?.cpuModel) return '—';
		return details.architecture ? `${details.cpuModel} (${details.architecture})` : details.cpuModel;
	});
	const cpuTempLine = $derived.by(() => {
		const chunks: string[] = [];
		if (details?.wifi24TempC != null) chunks.push(`2.4G ${details.wifi24TempC}°C`);
		if (details?.wifi5TempC != null) chunks.push(`5G ${details.wifi5TempC}°C`);
		if (details?.cpuTempC != null) chunks.push(`CPU ${details.cpuTempC}°C`);
		return chunks.join(' · ') || '—';
	});
	const memoryMain = $derived.by(() => {
		if (details?.memoryTotalMB == null || details.memoryUsedMB == null) return '—';
		return `${details.memoryUsedMB}/${details.memoryTotalMB} MB`;
	});
	const memoryPercent = $derived.by(() => {
		const pct = details?.memoryUsedPercent;
		if (pct === undefined || pct === null || Number.isNaN(pct)) return '';
		return `${pct}%`;
	});
	const osMainTitle = $derived(osTitle);
	const osPortTitle = $derived(details?.portedBuild ? '[Port]' : '');
	const vpnLine = $derived((details?.vpnComponents || []).join(' ') || '—');
	const storageLine = $derived((details?.storageComponents || []).join(' ') || '—');
	const featureLine = $derived((details?.featureComponents || []).join(' ') || '—');
	const updatedLabel = $derived.by(() => {
		if (!lastUpdated) return '';
		const d = new Date(lastUpdated);
		if (Number.isNaN(d.getTime())) return '';
		const now = new Date();
		const sameDay = d.getDate() === now.getDate() && d.getMonth() === now.getMonth() && d.getFullYear() === now.getFullYear();
		if (sameDay) {
			return d.toLocaleTimeString('ru-RU', { hour: '2-digit', minute: '2-digit', second: '2-digit' });
		}
		return d.toLocaleString('ru-RU', {
			day: '2-digit',
			month: '2-digit',
			hour: '2-digit',
			minute: '2-digit',
		});
	});

	let collapsed = $state(false);

	if (browser) {
		const savedDetails = localStorage.getItem(DETAILS_KEY);
		detailsOpen = savedDetails === '1';

		const savedCollapsed = localStorage.getItem(COLLAPSED_KEY);
		collapsed = savedCollapsed === null ? false : savedCollapsed === '1';
	}

	$effect(() => {
		if (!browser) return;
		localStorage.setItem(DETAILS_KEY, detailsOpen ? '1' : '0');
	});

	$effect(() => {
		if (!browser) return;
		localStorage.setItem(COLLAPSED_KEY, collapsed ? '1' : '0');
	});

	const isBasic = $derived(usageLevel === 'basic');
	const isExpert = $derived(usageLevel === 'expert');
</script>

<div class="settings-block sysinfo-block">
	<div class="card sysinfo-card">
		<div class="head-row settings-card-head">
			<button
				type="button"
				class="section-collapse-btn"
				onclick={() => (collapsed = !collapsed)}
				aria-expanded={!collapsed}
				aria-label={collapsed ? 'Развернуть информацию о системе' : 'Свернуть информацию о системе'}
			>
				<SettingsSectionLabel label="Система" icon={Router} tone="blue" inline />
				<span class="section-chevron system-collapse-marker" class:open={!collapsed} aria-hidden="true"><ChevronDown size={14} strokeWidth={2} /></span>
			</button>
			{#if !isBasic}
				<div class="head-actions">
					{#if updatedLabel}
						<span class="updated-at" title="Последнее обновление">
							<span class="live-dot" class:live-dot-loading={refreshing}></span>
							{updatedLabel}
						</span>
					{/if}
				</div>
			{/if}
		</div>

		<div class="collapsible-body" class:body-hidden={collapsed}>
	<div class="setting-row">
		<span class="info-key">AWGM</span>
		<span class="info-val">{systemInfo.version}</span>
	</div>
	<div class="setting-row">
		<span class="info-key">Роутер</span>
		<span class="info-val">
			{routerMainTitle}
			{#if routerRegionTitle}
				<span class="muted-inline"> {routerRegionTitle}</span>
			{/if}
		</span>
	</div>
	<div class="setting-row">
		<span class="info-key">ОС</span>
		<span class="info-val">
			{osMainTitle}
			{#if osPortTitle}
				<span class="muted-inline"> {osPortTitle}</span>
			{/if}
		</span>
	</div>
	{#if !isBasic}
		<div class="setting-row">
			<span class="info-key">CPU / Темп.</span>
			<span class="info-val info-val-stack">
				<span>{cpuModelLine}</span>
				<span class="sub-line">{cpuTempLine}</span>
			</span>
		</div>
		<div class="setting-row">
			<span class="info-key">Память</span>
			<span class="info-val">
				{memoryMain}
				{#if memoryPercent}
					<span class="muted-inline"> ({memoryPercent})</span>
				{/if}
			</span>
		</div>
	{/if}
	<div class="setting-row">
		<span class="info-key">Uptime</span>
		<span class="info-val">{details?.uptimeHuman || '—'}</span>
	</div>
	{#if !isBasic}
		<div class="setting-row">
			<span class="info-key">Load Avg</span>
			<span class="info-val">{details?.loadAverage || '—'}</span>
		</div>
		<div class="setting-row">
			<span class="info-key">OPKG</span>
			<span class="info-val">{details?.opkgStorage || '—'}</span>
		</div>
	{/if}
	<div class="setting-row">
		<span class="info-key">Сообщество</span>
		<a class="info-link" href="https://t.me/awgmanager" target="_blank" rel="noopener noreferrer">Telegram →</a>
	</div>
	{#if isExpert && details}
		<details class="more-box" bind:open={detailsOpen}>
			<summary class="more-summary">
				<span>Подробнее</span>
				<span class="more-chevron" class:open={detailsOpen} aria-hidden="true"><ChevronDown size={14} strokeWidth={2} /></span>
			</summary>
			<div class="more-grid">
				<div class="setting-row"><span class="info-key">Build Date</span><span class="info-val">{details.firmwareBuildDate || '—'}</span></div>
				<div class="setting-row"><span class="info-key">Канал</span><span class="info-val">{details.firmwareSandbox || '—'}</span></div>
				<div class="setting-row"><span class="info-key">Слот</span><span class="info-val">{details.bootSlot || '—'}</span></div>
				<div class="setting-row detail-row">
					<span class="info-key">VPN</span>
					<span class="detail-muted-block">{vpnLine}</span>
				</div>
				<div class="setting-row detail-row">
					<span class="info-key">Storage</span>
					<span class="detail-muted-block">{storageLine}</span>
				</div>
				<div class="setting-row detail-row">
					<span class="info-key">Features</span>
					<span class="detail-muted-block">{featureLine}</span>
				</div>
				{#if details.meshMembers?.length}
					<div class="mesh-list detail-row">
						<div class="info-key">Mesh Nodes</div>
						<div class="mesh-items-muted">
							{#each details.meshMembers as node}
							<div class="mesh-item">{node}</div>
							{/each}
						</div>
					</div>
				{/if}
			</div>
		</details>
	{/if}
	</div>
	</div>
</div>

<style>
	.sysinfo-block > .card {
		padding: 1rem;
	}

	.settings-card-head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.75rem;
		margin-bottom: 0.5rem;
	}

	.section-collapse-btn {
		display: inline-flex;
		align-items: center;
		gap: 0.35rem;
		background: none;
		border: none;
		padding: 0;
		cursor: default;
		color: inherit;
		pointer-events: none;
	}

	.section-chevron,
	.more-chevron {
		display: inline-flex;
		width: 14px;
		height: 14px;
		flex-shrink: 0;
		color: var(--color-text-muted);
		transition: transform var(--t-fast) ease, color var(--t-fast) ease;
	}

	.section-chevron.open,
	.more-chevron.open {
		transform: rotate(180deg);
	}

	.system-collapse-marker {
		display: none;
	}

	.section-collapse-btn:hover .section-chevron {
		color: var(--color-text-primary);
	}

	.collapsible-body {
		display: contents;
	}

	@media (max-width: 900px) {
		.section-collapse-btn {
			cursor: pointer;
			pointer-events: auto;
			border-radius: var(--radius-sm);
			margin: -0.125rem;
			padding: 0.125rem;
		}

		.section-collapse-btn:hover {
			color: var(--color-text-primary);
		}

		.system-collapse-marker {
			display: inline-flex;
		}

		.collapsible-body {
			display: flex;
			flex-direction: column;
			overflow: hidden;
			transition: max-height 0.25s ease, opacity 0.2s ease;
			max-height: 1000px;
			opacity: 1;
		}

		.collapsible-body.body-hidden {
			max-height: 0;
			opacity: 0;
			pointer-events: none;
		}
	}

	.collapsible-body > .setting-row {
		display: grid;
		grid-template-columns: minmax(0, 1fr) auto;
		align-items: center;
		column-gap: 0.75rem;
	}

	.collapsible-body > .setting-row .info-val {
		min-width: 0;
		justify-self: end;
		word-break: break-word;
	}

	.head-actions {
		display: inline-flex;
		align-items: center;
		gap: 0.45rem;
	}

	.updated-at {
		display: inline-flex;
		align-items: center;
		gap: 0.375rem;
		font-family: var(--font-mono);
		font-size: 0.6875rem;
		color: var(--color-text-muted);
		white-space: nowrap;
	}

	.live-dot {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		background: var(--color-success);
		box-shadow: 0 0 0 3px var(--color-success-tint);
		transition: background 0.2s ease;
	}

	.live-dot-loading {
		background: var(--color-warning, var(--color-accent));
		animation: pulse 1s ease-in-out infinite;
	}

	@keyframes pulse {
		0%, 100% { opacity: 1; }
		50% { opacity: 0.4; }
	}

	.info-key {
		font-size: 0.6875rem;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		color: var(--color-text-muted);
	}

	.info-val {
		font-size: 0.8125rem;
		font-family: var(--font-mono);
		color: var(--color-text-secondary);
		text-align: right;
		word-break: break-word;
	}

	.info-val-stack {
		display: inline-flex;
		flex-direction: column;
		align-items: flex-end;
		gap: 0.2rem;
	}

	.info-val-stack .sub-line {
		font-size: 0.76rem;
		color: var(--color-text-muted);
	}

	.muted-inline {
		color: var(--color-text-muted);
	}

	.info-link {
		font-size: 0.8125rem;
		color: var(--color-accent);
		text-decoration: none;
	}

	.info-link:hover {
		text-decoration: underline;
	}

	.more-box {
		margin-top: 0.35rem;
		border-top: 1px dashed var(--color-border);
		padding-top: 0.35rem;
	}

	.more-summary {
		display: inline-flex;
		align-items: center;
		gap: 0.35rem;
		padding: 0.125rem 0;
		cursor: pointer;
		font-size: 0.6875rem;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		color: var(--color-text-muted);
		list-style: none;
		transition: color var(--t-fast) ease;
	}

	.more-summary:hover,
	.more-summary:hover .more-chevron {
		color: var(--color-text-secondary);
	}

	.more-summary::marker {
		content: '';
	}

	.more-summary::-webkit-details-marker {
		display: none;
	}

	.more-grid {
		display: flex;
		flex-direction: column;
		margin-top: 0.5rem;
	}

	.more-grid > .setting-row:not(.detail-row):first-child {
		margin-top: 0;
		border-top: 1px solid var(--color-border);
		padding-top: 0.5rem;
	}

	.more-grid > .setting-row:not(.detail-row) {
		display: grid;
		grid-template-columns: minmax(0, 1fr) auto;
		align-items: center;
		column-gap: 0.75rem;
	}

	.more-grid > .setting-row:not(.detail-row) .info-val {
		min-width: 0;
		justify-self: end;
		text-align: right;
		word-break: break-word;
	}

	.mesh-list {
		padding: 0.35rem 0;
	}

	.detail-row {
		display: block;
	}

	.detail-muted-block {
		display: block;
		margin-top: 0.3rem;
		font-size: 0.8125rem;
		font-family: var(--font-mono);
		color: var(--color-text-muted);
		word-break: break-word;
	}

	.mesh-items-muted {
		margin-top: 0.3rem;
	}

	.mesh-item {
		font-family: var(--font-mono);
		font-size: 0.75rem;
		color: var(--color-text-muted);
		margin-top: 0.2rem;
		word-break: break-word;
	}
</style>
