<script lang="ts">
	import { m, uiText } from '$lib/i18n';
	import type { PageScanFetchMode, PageScanOptions, PageScanResult } from '$lib/types/diagnostics';
	import { Button, Badge, Input } from '$lib/components/ui';
	import { IpInspectSection, PageScanSection } from '$lib/components/diagnostics';
	import { ArrowRight, CheckCircle2, Globe } from 'lucide-svelte';
	import FircPageScanExit from './FircPageScanExit.svelte';
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
		onClose: () => void;
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
		onClose,
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
		if (!e.currentTarget.open) {
			onClose();
		} else if (!pageScanOptions && !loadingPageScanOptions) {
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
				<Button
					variant={testDomain === qd ? 'outline-primary' : 'secondary'}
					disabled={checkingDomain}
					onclick={() => onCheck(qd)}
				>
					{qd}
				</Button>
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
		<div class="checker-input">
			<Input
				ariaLabel={m.firc_monitor_checker_input_aria()}
				placeholder={m.firc_monitor_checker_placeholder()}
				bind:value={testDomain}
				fullWidth
			/>
		</div>
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

	<FircPageScanExit
		bind:pageScanExit
		bind:customProxyUrl
		{pageScanOptions}
		loadingOptions={loadingPageScanOptions}
		disabled={scanningPage}
		onRefresh={() => onLoadOptions(true)}
		onEdited={() => (pageScanMissingCustomProxy = false)}
	/>

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

	.checker-form {
		display: flex;
		gap: 0.5rem;
		flex-wrap: wrap;
		min-width: 0;
	}

	.checker-input {
		flex: 1 1 180px;
		min-width: 0;
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
