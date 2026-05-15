<script lang="ts">
	import { browser } from '$app/environment';
	import { onDestroy } from 'svelte';
	import type { SystemInfo } from '$lib/types';
	import type { UsageLevel } from '$lib/types/usageLevel';

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
	let nowTs = $state(Date.now());
	let progressTimer: ReturnType<typeof setInterval> | null = null;
	const DETAILS_KEY = 'awgm.settings.system.detailsOpen';
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

	if (browser) {
		const saved = localStorage.getItem(DETAILS_KEY);
		detailsOpen = saved === '1';
	}

	$effect(() => {
		if (!browser) return;
		localStorage.setItem(DETAILS_KEY, detailsOpen ? '1' : '0');
	});

	const isBasic = $derived(usageLevel === 'basic');
	const isExpert = $derived(usageLevel === 'expert');
	const refreshProgress = $derived.by(() => {
		if (!autoRefreshMs || autoRefreshMs <= 0 || !lastUpdated) return 0;
		const updatedAt = new Date(lastUpdated).getTime();
		if (!Number.isFinite(updatedAt)) return 0;
		const elapsed = Math.max(0, nowTs - updatedAt);
		const ratio = Math.min(1, elapsed / autoRefreshMs);
		return ratio;
	});

	if (browser) {
		progressTimer = setInterval(() => {
			nowTs = Date.now();
		}, 200);
	}

	onDestroy(() => {
		if (progressTimer) {
			clearInterval(progressTimer);
			progressTimer = null;
		}
	});
</script>

<div class="card">
	<div class="head-row">
		<div class="section-label">Система</div>
		{#if !isBasic}
			<div class="head-actions">
				{#if updatedLabel}
					<span class="updated-at" title="Последнее обновление">
						<span class="live-dot" class:live-dot-loading={refreshing}></span>
						{updatedLabel}
					</span>
				{/if}
				<button
					type="button"
					class="refresh-btn"
					class:timer-enabled={isExpert && autoRefreshMs > 0}
					onclick={() => onrefresh?.()}
					disabled={refreshing}
					aria-label="Обновить информацию о роутере"
					title="Обновить"
					style={`--refresh-progress:${refreshProgress * 360}deg;`}
				>
					<svg class="refresh-icon" viewBox="0 0 24 24" fill="none" aria-hidden="true">
						<path d="M21 12a9 9 0 1 1-2.64-6.36M21 4v6h-6" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" />
					</svg>
				</button>
			</div>
		{/if}
	</div>

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
		<span class="info-key">Связь</span>
		<a class="info-link" href="https://t.me/awgmanager" target="_blank" rel="noopener noreferrer">Telegram →</a>
	</div>
	{#if isExpert && details}
		<details class="more-box" bind:open={detailsOpen}>
			<summary>Подробнее</summary>
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

<style>
	.head-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
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

	.refresh-btn {
		position: relative;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: 28px;
		height: 28px;
		border-radius: 6px;
		border: 1px solid var(--color-border);
		background: transparent;
		color: var(--color-text-muted);
		cursor: pointer;
		transition: all var(--t-fast) ease;
	}

	.refresh-btn.timer-enabled::before {
		content: '';
		position: absolute;
		inset: -1px;
		border-radius: inherit;
		padding: 1px;
		background: conic-gradient(var(--color-accent) var(--refresh-progress), transparent 0deg);
		-webkit-mask: linear-gradient(#000 0 0) content-box, linear-gradient(#000 0 0);
		mask: linear-gradient(#000 0 0) content-box, linear-gradient(#000 0 0);
		-webkit-mask-composite: xor;
		mask-composite: exclude;
		pointer-events: none;
		opacity: 0.95;
	}

	.refresh-btn:hover:not(:disabled) {
		color: var(--color-accent);
		background: var(--color-bg-hover);
	}

	.refresh-btn:disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}

	.refresh-icon {
		position: relative;
		z-index: 1;
		width: 15px;
		height: 15px;
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
		padding-top: 0.4rem;
	}

	.more-box summary {
		cursor: pointer;
		font-size: 0.75rem;
		color: var(--color-text-muted);
	}

	.more-grid {
		display: flex;
		flex-direction: column;
		margin-top: 0.4rem;
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
