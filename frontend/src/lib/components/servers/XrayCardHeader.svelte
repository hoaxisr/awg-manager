<script lang="ts">
	import { m } from '$lib/i18n';
	import { Button, IconButton } from '$lib/components/ui';
	import type { XrayStatus } from '$lib/types';
	import { EXTERNAL_ACCESS_LABELS } from './external-access/connectionLabels';
	import { AlertTriangle, Power, RotateCw, Settings } from 'lucide-svelte';

	// Шапка карточки VLESS: статус, версия демона, управление и баннеры недоступности.
	interface Props {
		status: XrayStatus | null;
		loading: boolean;
		isConfigured: boolean;
		needsCheck: boolean;
		actionLoading: boolean;
		onsettings: () => void;
		ontogglepower: () => void;
		onrestart: () => void;
	}

	let { status, loading, isConfigured, needsCheck, actionLoading, onsettings, ontogglepower, onrestart }: Props =
		$props();
</script>

<header class="card-header">
	<div class="header-left">
		<div class="title-row">
			<span
				class="led"
				class:led-running={status?.running && !needsCheck}
				class:led-warning={needsCheck}
				class:led-stopped={!status?.running && !needsCheck}
			></span>
			<h2 class="title">{EXTERNAL_ACCESS_LABELS.vlessTitle}</h2>
			{#if loading && !status}
				<span class="badge badge-neutral">{m.servers_xray_loading()}</span>
			{:else if !isConfigured && !loading}
				<span class="badge badge-neutral">{m.servers_xray_not_configured()}</span>
			{:else if needsCheck}
				<span class="badge badge-warning">{EXTERNAL_ACCESS_LABELS.statusNeedsCheck}</span>
			{:else if status?.running}
				<span class="badge badge-success">{EXTERNAL_ACCESS_LABELS.statusRunning}</span>
			{:else}
				<span class="badge badge-neutral">{EXTERNAL_ACCESS_LABELS.statusStopped}</span>
			{/if}
			{#if status}
				<span
					class="badge font-mono"
					class:badge-info={status.installed && !status.unavailable_reason}
					class:badge-warning={!status.installed || Boolean(status.unavailable_reason)}
				>
					{status.backend || 'awgm-xhttpd'}{status.version ? ` v${status.version.replace(/^awgm-xhttpd\s+/i, '')}` : ''}{status.running && status.pid ? ` · PID ${status.pid}` : ''}
				</span>
			{/if}
		</div>
		<div class="subtitle">
			{EXTERNAL_ACCESS_LABELS.vlessSubtitle}
		</div>
	</div>

	{#if isConfigured}
		<div class="header-actions">
			<Button
				variant="secondary"
				size="sm"
				onclick={onsettings}
			>
				<Settings size={14} class="mr-1" />
				{m.servers_xray_settings()}
			</Button>
			<Button
				variant="secondary"
				size="sm"
				onclick={ontogglepower}
				disabled={actionLoading}
			>
				<Power size={14} class="mr-1" />
				{status?.running ? m.common_stop() : m.common_start()}
			</Button>
			<IconButton
				ariaLabel={m.servers_xray_restart_title()}
				title={m.servers_xray_restart_title()}
				onclick={onrestart}
				disabled={actionLoading}
			>
				<RotateCw size={15} class={actionLoading ? 'spin' : ''} />
			</IconButton>
		</div>
	{/if}
</header>

{#if status && (!status.installed || status.unavailable_reason)}
	<div class="alert-banner error">
		<AlertTriangle size={18} class="banner-icon banner-icon-err" />
		<div class="banner-content">
			<div class="banner-title">{m.servers_xray_daemon_unavailable()}</div>
			<div class="banner-desc">
				{status.unavailable_reason || status.error || m.servers_xray_daemon_unavailable_desc()}
			</div>
		</div>
	</div>
{/if}

{#if status?.recovery_required}
	<div class="alert-banner warning">
		<AlertTriangle size={18} class="banner-icon banner-icon-warn" />
		<div class="banner-content">
			<div class="banner-title">{m.servers_xray_recovery_title()}</div>
			<div class="banner-desc">{status.recovery_reason || m.servers_xray_recovery_desc()}</div>
		</div>
	</div>
{/if}

<style>
	.card-header {
		display: flex;
		justify-content: space-between;
		align-items: flex-start;
		gap: 0.875rem;
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
		font-size: 16px;
		font-weight: 600;
		color: var(--color-text-primary);
		margin: 0;
	}

	.subtitle {
		font-size: 12px;
		color: var(--color-text-muted);
	}

	.badge {
		font-size: 11px;
		padding: 2px 8px;
		border-radius: var(--radius-sm);
		font-weight: 500;
		border: 1px solid transparent;
	}
	.badge-success {
		background: var(--color-success-tint);
		border-color: var(--color-success-border);
		color: var(--color-success);
	}
	.badge-warning {
		background: var(--color-warning-tint);
		border-color: var(--color-warning-border);
		color: var(--color-warning);
	}
	.badge-neutral {
		background: var(--color-bg-hover);
		border-color: var(--color-border);
		color: var(--color-text-muted);
	}
	.badge-info {
		background: var(--color-accent-tint);
		border-color: var(--color-accent-border);
		color: var(--color-accent);
	}

	.led {
		width: 9px;
		height: 9px;
		border-radius: 50%;
		flex-shrink: 0;
	}
	.led-running {
		background: var(--color-success);
		box-shadow: 0 0 4px var(--color-success-border);
	}
	.led-warning {
		background: var(--color-warning);
		box-shadow: 0 0 4px var(--color-warning-border);
	}
	.led-stopped {
		background: var(--color-text-muted);
	}

	.header-actions {
		display: flex;
		align-items: center;
		gap: 6px;
		flex-wrap: wrap;
	}

	.alert-banner {
		display: flex;
		align-items: flex-start;
		gap: 10px;
		padding: 10px 14px;
		border-radius: var(--radius-sm);
		font-size: 12.5px;
	}
	.alert-banner.warning {
		background: var(--color-warning-tint);
		border: 1px solid var(--color-warning-border);
		color: var(--color-text-primary);
	}
	.alert-banner.error {
		background: var(--color-error-tint);
		border: 1px solid var(--color-error-border);
		color: var(--color-text-primary);
	}
	.banner-content {
		display: flex;
		flex-direction: column;
		gap: 4px;
		flex: 1;
	}
	.banner-title {
		font-weight: 600;
	}
	.banner-desc {
		color: var(--color-text-muted);
		font-size: 12px;
		line-height: 1.4;
	}

	.font-mono {
		font-family: var(--font-mono);
	}
</style>
