<script lang="ts">
	import { m } from '$lib/i18n';
	import type { PageScanOptions } from '$lib/types/diagnostics';
	import { Dropdown, IconButton, Input, type DropdownOption } from '$lib/components/ui';
	import { RefreshCw } from 'lucide-svelte';

	interface Props {
		pageScanExit: string;
		customProxyUrl: string;
		pageScanOptions: PageScanOptions | null;
		loadingOptions: boolean;
		disabled: boolean;
		onRefresh: () => void;
		onEdited: () => void;
	}

	let {
		pageScanExit = $bindable(),
		customProxyUrl = $bindable(),
		pageScanOptions,
		loadingOptions,
		disabled,
		onRefresh,
		onEdited,
	}: Props = $props();

	const exitOptions = $derived.by<DropdownOption[]>(() => [
		{
			value: 'auto',
			label: pageScanOptions?.defaultProxy
				? m.page_scan_exit_auto_with_proxy({
						proxy: pageScanOptions.defaultProxyLabel || pageScanOptions.defaultProxy,
					})
				: m.page_scan_exit_auto(),
		},
		{ value: 'direct', label: m.page_scan_exit_direct() },
		...(pageScanOptions?.interfaces ?? []).map((opt) => ({
			value: `iface:${opt.name}`,
			label: opt.label,
			group: m.page_scan_exit_group_tunnels(),
		})),
		...(pageScanOptions?.proxies ?? []).map((opt) => ({
			value: `proxy:${opt.url}`,
			label: opt.label,
			group: m.page_scan_exit_group_proxies(),
		})),
		{ value: 'custom', label: m.page_scan_exit_custom_proxy() },
	]);
</script>

<div class="scan-exit">
	<label class="scan-exit-label" for="firc-scan-exit-select">
		{m.page_scan_exit_label()}
	</label>
	<div class="scan-exit-select">
		<Dropdown
			id="firc-scan-exit-select"
			bind:value={pageScanExit}
			options={exitOptions}
			{disabled}
			fullWidth
			onchange={onEdited}
		/>
	</div>
	<IconButton
		ariaLabel={m.page_scan_refresh_options_title()}
		title={m.page_scan_refresh_options_title()}
		disabled={loadingOptions || disabled}
		onclick={onRefresh}
	>
		<RefreshCw size={13} aria-hidden="true" />
	</IconButton>
	{#if pageScanExit === 'custom'}
		<div class="scan-exit-proxy">
			<Input
				ariaLabel={m.page_scan_exit_custom_proxy()}
				placeholder={m.page_scan_custom_proxy_placeholder()}
				autocomplete="off"
				bind:value={customProxyUrl}
				oninput={onEdited}
				{disabled}
				fullWidth
			/>
		</div>
	{/if}
</div>

<style>
	.scan-exit {
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: 0.45rem;
		min-width: 0;
	}

	.scan-exit-label {
		font-size: 0.75rem;
		color: var(--text-secondary);
		white-space: nowrap;
	}

	.scan-exit-select {
		flex: 0 1 280px;
		min-width: 0;
	}

	.scan-exit-proxy {
		flex: 1 1 220px;
		min-width: 0;
	}
</style>
