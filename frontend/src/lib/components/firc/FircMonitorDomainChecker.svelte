<script lang="ts">
	import { m, uiText } from '$lib/i18n';
	import type { PageScanFetchMode, PageScanOptions, PageScanResult } from '$lib/types/diagnostics';
	import { Button, Badge } from '$lib/components/ui';
	import { IpInspectSection, PageScanSection } from '$lib/components/diagnostics';
	import { ArrowRight, CheckCircle2, Globe, RefreshCw } from 'lucide-svelte';
	import { formatOutboundLabel, type FircDomainCheckView } from './fircMonitorFormat';

	interface Props {
		testDomain: string;
		checkingDomain: boolean;
		checkResult: FircDomainCheckView | null;
		scanningPage: boolean;
		pageScanResult: PageScanResult | null;
		pageScanError: string;
		pageScanExit: string;
		customProxyUrl: string;
		pageScanMissingCustomProxy: boolean;
		pageScanOptions: PageScanOptions | null;
		loadingPageScanOptions: boolean;
		onLoadOptions: (force?: boolean) => void;
		onCheck: (domain?: string) => void;
		onScan: (mode?: PageScanFetchMode) => void;
		onRetryScan: () => void;
		onOpenQuickAdd: (target: { ip?: string; domain?: string; prefix?: string }) => void;
		onRoutesChanged: () => void;
	}

	let {
		testDomain = $bindable(),
		checkingDomain,
		checkResult,
		scanningPage,
		pageScanResult,
		pageScanError,
		pageScanExit = $bindable(),
		customProxyUrl = $bindable(),
		pageScanMissingCustomProxy = $bindable(),
		pageScanOptions,
		loadingPageScanOptions,
		onLoadOptions,
		onCheck,
		onScan,
		onRetryScan,
		onOpenQuickAdd,
		onRoutesChanged,
	}: Props = $props();

	const quickDomains = [
		'gemini.google.com',
		'generativelanguage.googleapis.com',
		'chatgpt.com',
		't.me',
		'youtube.com',
		'2ip.io',
	];
</script>

<!-- Instant Domain Routing Checker -->
<details
	class="domain-checker"
	ontoggle={(e) => {
		if (e.currentTarget.open && !pageScanOptions && !loadingPageScanOptions) {
			onLoadOptions();
		}
	}}
>
	<summary>{m.firc_monitor_checker_summary()}</summary>
<div class="checker-box">
	<div class="checker-top">
		<div class="checker-label">
			<Globe size={14} />
			<span>{m.firc_monitor_checker_label()}</span>
		</div>
		<div class="checker-quick-chips">
			{#each quickDomains as qd (qd)}
				<button
					type="button"
					class="quick-chip"
					class:active={testDomain === qd}
					disabled={checkingDomain}
					onclick={() => onCheck(qd)}
				>
					{qd}
				</button>
			{/each}
		</div>
	</div>

	<form
		class="checker-form"
		onsubmit={(e) => {
			e.preventDefault();
			onCheck();
		}}
	>
		<input
			type="text"
			class="checker-input"
			aria-label={m.firc_monitor_checker_input_aria()}
			placeholder={m.firc_monitor_checker_placeholder()}
			bind:value={testDomain}
		/>
		<Button variant="primary" size="sm" type="submit" loading={checkingDomain} disabled={scanningPage}>
			{m.common_check()}
		</Button>
		<Button
			variant="secondary"
			size="sm"
			type="button"
			loading={scanningPage}
			disabled={!testDomain.trim() || checkingDomain}
			onclick={() => onScan()}
		>
			{m.page_scan_btn()}
		</Button>
	</form>

	<div class="checker-scan-exit">
		<label class="checker-scan-exit-label" for="firc-scan-exit-select">
			{m.page_scan_exit_label()}
		</label>
		<div class="checker-scan-exit-controls">
			<select
				id="firc-scan-exit-select"
				class="checker-scan-select"
				bind:value={pageScanExit}
				onchange={() => (pageScanMissingCustomProxy = false)}
				disabled={scanningPage}
			>
				<option value="auto">
					{pageScanOptions?.defaultProxy
						? m.page_scan_exit_auto_with_proxy({
								proxy: pageScanOptions.defaultProxyLabel || pageScanOptions.defaultProxy,
							})
						: m.page_scan_exit_auto()}
				</option>
				<option value="direct">{m.page_scan_exit_direct()}</option>
				{#if (pageScanOptions?.interfaces.length ?? 0) > 0}
					<optgroup label={m.page_scan_exit_group_tunnels()}>
						{#each pageScanOptions?.interfaces ?? [] as ifaceOpt (ifaceOpt.name)}
							<option value={`iface:${ifaceOpt.name}`}>{ifaceOpt.label}</option>
						{/each}
					</optgroup>
				{/if}
				{#if (pageScanOptions?.proxies.length ?? 0) > 0}
					<optgroup label={m.page_scan_exit_group_proxies()}>
						{#each pageScanOptions?.proxies ?? [] as proxyOpt (proxyOpt.url)}
							<option value={`proxy:${proxyOpt.url}`}>{proxyOpt.label}</option>
						{/each}
					</optgroup>
				{/if}
				<option value="custom">{m.page_scan_exit_custom_proxy()}</option>
			</select>
			<button
				type="button"
				class="checker-scan-refresh"
				title={m.page_scan_refresh_options_title()}
				aria-label={m.page_scan_refresh_options_title()}
				disabled={loadingPageScanOptions || scanningPage}
				onclick={() => onLoadOptions(true)}
			>
				<RefreshCw size={13} aria-hidden="true" />
			</button>
		</div>
		{#if pageScanExit === 'custom'}
			<input
				type="text"
				class="checker-scan-proxy-input"
				aria-label={m.page_scan_exit_custom_proxy()}
				placeholder={m.page_scan_custom_proxy_placeholder()}
				bind:value={customProxyUrl}
				oninput={() => (pageScanMissingCustomProxy = false)}
				disabled={scanningPage}
			/>
		{/if}
	</div>

	{#if checkResult}
		<div class="checker-res" class:is-routed={checkResult.routed} class:is-error={Boolean(checkResult.error)}>
			{#if checkResult.error}
				<Badge variant="error" size="sm">{m.common_error()}</Badge>
				<span class="mono">{checkResult.domain}</span>
				<span>— {uiText(checkResult.error)}</span>
			{:else if checkResult.routed}
				<CheckCircle2 size={15} />
				<Badge variant="success" size="sm">{m.firc_monitor_check_routed()}</Badge>
				<span class="mono"><strong>{checkResult.domain}</strong></span>
				<ArrowRight size={13} />
				<span class="mono">FakeIP {checkResult.fakeIp}</span>
				{#if checkResult.realIp}
					<span class="muted-meta">{m.firc_monitor_check_real_ip({ ip: checkResult.realIp })}</span>
				{/if}
				<ArrowRight size={13} />
				<Badge variant="accent" size="sm">{checkResult.groupName || checkResult.groupId}</Badge>
				<ArrowRight size={13} />
				<Badge variant="info" size="sm" mono>{m.firc_monitor_check_tunnel({ outbound: formatOutboundLabel(checkResult.outbound, checkResult.outboundLabel) })}</Badge>
				<span class="muted-meta">{m.firc_monitor_latency({ ms: checkResult.latencyMs })}</span>
			{:else}
				<Badge variant="warning" size="sm">{m.firc_monitor_check_direct()}</Badge>
				<span class="mono"><strong>{checkResult.domain}</strong></span>
				{#if checkResult.realIp}
					<span class="mono">→ {checkResult.realIp}</span>
				{/if}
				<span class="muted-meta">{m.firc_monitor_direct_latency({ outbound: checkResult.outbound, ms: checkResult.latencyMs })}</span>
			{/if}
		</div>
		{#if !checkResult.error}
			<IpInspectSection
				ip={checkResult.realIp || ''}
				domain={checkResult.domain}
				compact
				{onOpenQuickAdd}
			/>
		{/if}
	{/if}

	{#if scanningPage || pageScanResult || pageScanError || pageScanMissingCustomProxy}
		<PageScanSection
			loading={scanningPage}
			error={pageScanMissingCustomProxy ? m.page_scan_custom_proxy_required() : pageScanError}
			result={pageScanResult}
			{onOpenQuickAdd}
			onRescan={(mode) => onScan(mode)}
			onRetry={onRetryScan}
			{onRoutesChanged}
		/>
	{/if}
</div>
</details>

<style>
	.domain-checker summary { cursor: pointer; font-size: .8rem; color: var(--text-secondary); padding: .3rem 0; }
	.domain-checker[open] summary { margin-bottom: .5rem; }

	.checker-box {
		display: flex;
		flex-direction: column;
		gap: 0.55rem;
		padding: 0.75rem;
		border-radius: 8px;
		background: var(--bg-tertiary, rgba(0, 0, 0, 0.16));
		border: 1px solid var(--border);
	}

	.checker-top {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.5rem;
		flex-wrap: wrap;
	}

	.checker-label {
		display: inline-flex;
		align-items: center;
		gap: 0.4rem;
		font-size: 0.8rem;
		font-weight: 500;
		color: var(--text-secondary);
	}

	.checker-quick-chips {
		display: flex;
		align-items: center;
		gap: 0.35rem;
		flex-wrap: wrap;
	}

	.quick-chip {
		padding: 2px 8px;
		border-radius: 999px;
		font-family: var(--font-mono);
		font-size: 0.72rem;
		background: var(--bg-secondary);
		border: 1px solid var(--border);
		color: var(--text-secondary);
		cursor: pointer;
		transition: all 0.15s ease;
		max-width: 100%;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.quick-chip:hover,
	.quick-chip.active {
		border-color: var(--accent);
		color: var(--text-primary);
		background: color-mix(in srgb, var(--accent) 14%, var(--bg-secondary));
	}

	.checker-form {
		display: flex;
		gap: 0.5rem;
		flex-wrap: wrap;
		min-width: 0;
	}

	.checker-input {
		flex: 1 1 180px;
		min-width: 0;
		height: 32px;
		padding: 0 0.65rem;
		border-radius: 6px;
		border: 1px solid var(--border);
		background: var(--bg-primary, var(--bg-secondary));
		color: var(--text-primary);
		font-family: var(--font-mono);
		font-size: 0.8rem;
	}

	.checker-scan-exit {
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: 0.45rem;
		min-width: 0;
	}

	.checker-scan-exit-label {
		font-size: 0.75rem;
		color: var(--text-secondary);
		white-space: nowrap;
	}

	.checker-scan-exit-controls {
		display: inline-flex;
		align-items: center;
		gap: 0.3rem;
		min-width: 0;
		max-width: 100%;
	}

	.checker-scan-select {
		height: 28px;
		max-width: 100%;
		padding: 0 0.5rem;
		border-radius: 6px;
		border: 1px solid var(--border);
		background: var(--bg-primary, var(--bg-secondary));
		color: var(--text-primary);
		font-size: 0.76rem;
		font-family: var(--font-mono);
	}

	.checker-scan-refresh {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: 28px;
		height: 28px;
		border-radius: 6px;
		border: 1px solid var(--border);
		background: var(--bg-secondary);
		color: var(--text-secondary);
		cursor: pointer;
	}

	.checker-scan-refresh:hover:not(:disabled) {
		border-color: var(--accent);
		color: var(--text-primary);
	}

	.checker-scan-proxy-input {
		flex: 1 1 220px;
		min-width: 0;
		height: 28px;
		padding: 0 0.55rem;
		border-radius: 6px;
		border: 1px solid var(--border);
		background: var(--bg-primary, var(--bg-secondary));
		color: var(--text-primary);
		font-family: var(--font-mono);
		font-size: 0.76rem;
	}

	.checker-res {
		display: flex;
		align-items: center;
		gap: 0.45rem;
		flex-wrap: wrap;
		padding: 0.5rem 0.65rem;
		border-radius: 6px;
		font-size: 0.8rem;
		background: rgba(224, 175, 104, 0.1);
		border: 1px solid rgba(224, 175, 104, 0.35);
	}

	.checker-res.is-routed {
		background: rgba(158, 206, 106, 0.1);
		border-color: rgba(158, 206, 106, 0.35);
	}

	.checker-res.is-error {
		background: rgba(247, 118, 142, 0.1);
		border-color: rgba(247, 118, 142, 0.35);
	}

	.mono {
		font-family: var(--font-mono);
	}

	.muted-meta {
		font-size: 0.71rem;
		color: var(--text-muted);
	}
</style>
