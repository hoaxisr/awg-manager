<script lang="ts">
	import { untrack } from 'svelte';
	import { m } from '$lib/i18n';
	import { api } from '$lib/api/client';
	import type { DnsRoute } from '$lib/types';
	import type { FircGroup, FircRule } from '$lib/types/firc';
	import type { PageScanFetchMode, PageScanResource, PageScanResult } from '$lib/types/diagnostics';
	import { Badge, Button } from '$lib/components/ui';
	import { notifications } from '$lib/stores/notifications';
	import { fircSyncNonce } from '$lib/stores/firc';
	import { dnsRoutesStore } from '$lib/stores/routing';
	import { copyToClipboard } from '$lib/utils/clipboard';
	import {
		detectIpFamily,
		extractRootDomain,
		snipLongDomain,
	} from '$lib/utils/domainUtils';
	import { ArrowRight, CheckCircle2, Copy, Layers, Plus, RefreshCw } from 'lucide-svelte';

	interface Props {
		loading?: boolean;
		error?: string;
		result?: PageScanResult | null;
		onOpenQuickAdd?: (target: { ip?: string; domain?: string; prefix?: string }) => void;
		onRescan?: (mode: PageScanFetchMode) => void;
		onRetry?: () => void;
		onRoutesChanged?: () => void;
	}

	interface BulkDestOption {
		key: string;
		label: string;
	}

	let {
		loading = false,
		error = '',
		result = null,
		onOpenQuickAdd,
		onRescan,
		onRetry,
		onRoutesChanged,
	}: Props = $props();

	let selectedHosts = $state<Record<string, boolean>>({});
	let collapseRoots = $state(true);

	let destsLoaded = $state(false);
	let loadingDests = $state(false);
	let fircGroups = $state<FircGroup[]>([]);
	let dnsRoutes = $state<DnsRoute[]>([]);
	let selectedDestKey = $state('');
	let bulkSaving = $state(false);

	let resources = $derived(result?.resources ?? []);

	let selectableDomains = $derived(
		resources.filter((r) => detectIpFamily(r.host) === 'unknown'),
	);

	let unroutedDomains = $derived(
		selectableDomains.filter((r) => !r.routed && !r.routeUnknown),
	);

	let checkedDomainList = $derived(
		selectableDomains
			.filter((r) => Boolean(selectedHosts[r.host]))
			.map((r) => r.host),
	);

	let effectiveBulkDomains = $derived.by((): string[] => {
		const out: string[] = [];
		const seen = new Set<string>();
		for (const host of checkedDomainList) {
			const normalized = (collapseRoots ? extractRootDomain(host) : host).trim().toLowerCase();
			if (!normalized || seen.has(normalized)) continue;
			seen.add(normalized);
			out.push(normalized);
		}
		return out;
	});

	let compatibleFirc = $derived<BulkDestOption[]>(
		fircGroups.map((g) => ({ key: `firc:${g.id}`, label: g.name || g.id })),
	);

	let compatibleDns = $derived<BulkDestOption[]>(
		dnsRoutes.map((r) => ({ key: `dns:${r.id}`, label: r.name || r.id })),
	);

	let allDestKeys = $derived([
		...compatibleFirc.map((o) => o.key),
		...compatibleDns.map((o) => o.key),
	]);

	$effect(() => {
		const _res = result;
		untrack(() => {
			selectedHosts = {};
			destsLoaded = false;
			if (!_res) {
				fircGroups = [];
				dnsRoutes = [];
			}
		});
	});

	$effect(() => {
		if (checkedDomainList.length > 0 && result && !destsLoaded && !loadingDests) {
			untrack(() => {
				void loadBulkDestinations();
			});
		}
	});

	$effect(() => {
		if (allDestKeys.length === 0) {
			selectedDestKey = '';
			return;
		}
		if (!allDestKeys.includes(selectedDestKey)) {
			selectedDestKey = allDestKeys[0];
		}
	});

	async function loadBulkDestinations(): Promise<void> {
		loadingDests = true;
		try {
			const [fgRes, dnsRes] = await Promise.allSettled([
				typeof api.fircGetGroups === 'function' ? api.fircGetGroups() : Promise.resolve([]),
				typeof api.listDnsRoutes === 'function' ? api.listDnsRoutes() : Promise.resolve([]),
			]);
			fircGroups = fgRes.status === 'fulfilled' && Array.isArray(fgRes.value) ? fgRes.value : [];
			dnsRoutes = dnsRes.status === 'fulfilled' && Array.isArray(dnsRes.value) ? dnsRes.value : [];
			destsLoaded = true;
		} finally {
			loadingDests = false;
		}
	}

	function toggleHost(host: string): void {
		selectedHosts = {
			...selectedHosts,
			[host]: !selectedHosts[host],
		};
	}

	function selectAllUnrouted(): void {
		const next: Record<string, boolean> = {};
		for (const item of unroutedDomains) {
			next[item.host] = true;
		}
		selectedHosts = next;
	}

	function clearSelection(): void {
		selectedHosts = {};
	}

	function sourceLabel(kind: string): string {
		switch (kind) {
			case 'document':
				return m.page_scan_kind_document();
			case 'redirect':
				return m.page_scan_kind_redirect();
			case 'script':
				return m.page_scan_kind_script();
			case 'style':
				return m.page_scan_kind_style();
			case 'image':
				return m.page_scan_kind_image();
			case 'iframe':
				return m.page_scan_kind_iframe();
			case 'media':
				return m.page_scan_kind_media();
			case 'embed':
				return m.page_scan_kind_embed();
			case 'preconnect':
				return m.page_scan_kind_preconnect();
			case 'csp':
				return m.page_scan_kind_csp();
			case 'link':
				return m.page_scan_kind_link();
			default:
				return kind;
		}
	}

	function formatOutbound(r: PageScanResource): string {
		if (r.outboundLabel && r.outbound && r.outboundLabel !== r.outbound) {
			return `${r.outboundLabel} (${r.outbound})`;
		}
		return r.outboundLabel || r.outbound || '';
	}

	function buildClipboardReport(scan: PageScanResult): string {
		const lines: string[] = [];
		for (const r of scan.resources) {
			const head = r.ip ? `${r.host} ${r.ip}` : r.host;
			lines.push(head);
			if (r.inspect?.ptr) {
				lines.push(`→ ${r.inspect.ptr}`);
			}
			if (r.inspect?.asn || r.inspect?.organization) {
				const asnPart = r.inspect.asn ? `AS${r.inspect.asn}` : '';
				const orgPart = r.inspect.organization || '';
				lines.push(`→ ${[asnPart, orgPart].filter(Boolean).join(' ')}`);
			}
		}
		return lines.join('\n');
	}

	async function copyText(val: string, customToast?: string): Promise<void> {
		if (!val) return;
		const ok = await copyToClipboard(val);
		if (ok) {
			notifications.success(customToast ?? m.ip_inspect_copied({ value: val }));
		} else {
			notifications.error(m.ip_inspect_copy_failed());
		}
	}

	function makeRuleId(): string {
		const rand = Math.floor(Math.random() * 0xfffffff)
			.toString(36)
			.padStart(5, '0');
		return `rule_${Date.now().toString(36)}_${rand}`;
	}

	async function applyBulkAdd(): Promise<void> {
		if (bulkSaving || effectiveBulkDomains.length === 0 || !selectedDestKey) return;
		bulkSaving = true;
		try {
			if (selectedDestKey.startsWith('firc:')) {
				const groupId = selectedDestKey.slice(5);
				const idx = fircGroups.findIndex((g) => g.id === groupId);
				if (idx < 0) return;
				const group = fircGroups[idx];
				const destName = group.name || group.id;
				const existing = new Set(
					(group.rules ?? [])
						.filter((r) => r.type === 'namespace' || r.type === 'domain')
						.map((r) => r.rule.trim().toLowerCase()),
				);

				const toAdd: FircRule[] = [];
				let dupes = 0;
				for (const dom of effectiveBulkDomains) {
					const low = dom.toLowerCase();
					if (existing.has(low)) {
						dupes++;
						continue;
					}
					existing.add(low);
					toAdd.push({
						id: makeRuleId(),
						type: 'namespace',
						rule: low,
						enable: true,
					});
				}

				if (toAdd.length === 0) {
					notifications.info(m.page_scan_bulk_all_dupes({ dest: destName }));
					selectedHosts = {};
					return;
				}

				const nextGroups = fircGroups.map((g, i) =>
					i === idx ? { ...g, rules: [...toAdd, ...(g.rules ?? [])] } : g,
				);
				await api.fircSaveGroups(nextGroups, true);
				fircGroups = nextGroups;
				fircSyncNonce.update((n) => n + 1);

				if (dupes > 0) {
					notifications.success(
						m.page_scan_bulk_added_dupes({ added: toAdd.length, dupes, dest: destName }),
					);
				} else {
					notifications.success(
						m.page_scan_bulk_added({ added: toAdd.length, dest: destName }),
					);
				}
				selectedHosts = {};
				onRoutesChanged?.();
				return;
			}

			if (selectedDestKey.startsWith('dns:')) {
				const routeId = selectedDestKey.slice(4);
				const route = dnsRoutes.find((r) => r.id === routeId);
				if (!route) return;
				const destName = route.name || route.id;
				const existingAll = new Set(
					[...(route.manualDomains ?? []), ...(route.domains ?? [])].map((d) =>
						d.trim().toLowerCase(),
					),
				);

				const toAdd: string[] = [];
				let dupes = 0;
				for (const dom of effectiveBulkDomains) {
					const low = dom.toLowerCase();
					if (existingAll.has(low)) {
						dupes++;
						continue;
					}
					existingAll.add(low);
					toAdd.push(low);
				}

				if (toAdd.length === 0) {
					notifications.info(m.page_scan_bulk_all_dupes({ dest: destName }));
					selectedHosts = {};
					return;
				}

				const nextManualDomains = [...(route.manualDomains ?? []), ...toAdd];
				const baseText = route.manualText ?? (route.manualDomains ?? []).join('\n');
				const nextManualText = baseText.trim()
					? `${baseText.replace(/\s+$/, '')}\n${toAdd.join('\n')}`
					: toAdd.join('\n');

				await api.updateDnsRoute(route.id, {
					...route,
					manualDomains: nextManualDomains,
					manualText: nextManualText,
				});
				dnsRoutesStore.refetch().catch(() => {});

				if (dupes > 0) {
					notifications.success(
						m.page_scan_bulk_added_dupes({ added: toAdd.length, dupes, dest: destName }),
					);
				} else {
					notifications.success(
						m.page_scan_bulk_added({ added: toAdd.length, dest: destName }),
					);
				}
				selectedHosts = {};
				onRoutesChanged?.();
			}
		} catch (e) {
			const msg = (e as Error).message || m.page_scan_bulk_failed();
			notifications.error(msg);
		} finally {
			bulkSaving = false;
		}
	}
</script>

{#if loading || error || result}
	<section class="page-scan" aria-label={m.page_scan_title()}>
		{#if loading}
			<div class="scan-status">{m.page_scan_loading()}</div>
		{/if}

		{#if error && !loading}
			<div class="scan-error" role="status">
				<Badge variant="error" size="xs">{m.page_scan_error_prefix()}</Badge>
				<span class="scan-error-msg">{error}</span>
				{#if onRetry}
					<Button variant="ghost" size="sm" disabled={loading} onclick={() => onRetry()}>
						{#snippet iconBefore()}<RefreshCw size={12} aria-hidden="true" />{/snippet}
						{m.page_scan_retry_btn()}
					</Button>
				{/if}
			</div>
		{/if}

		{#if result}
			<div class="scan-head">
				<div class="scan-title-row">
					<Layers size={14} aria-hidden="true" />
					<span class="scan-title">{m.page_scan_title()}</span>
					<Badge variant={result.statusCode >= 400 ? 'warning' : 'success'} size="xs" mono>
						HTTP {result.statusCode}
					</Badge>
					<Badge
						variant={result.fetchVia === 'interface'
							? 'info'
							: result.fetchVia === 'proxy'
								? 'accent'
								: 'muted'}
						size="xs"
						title={result.fetchTarget || undefined}
					>
						{#if result.fetchVia === 'interface'}
							{m.page_scan_via_interface({
								target: result.fetchTargetLabel || result.fetchTarget || '',
							})}
						{:else if result.fetchVia === 'proxy'}
							{result.fetchTargetLabel || result.fetchTarget
								? m.page_scan_via_proxy_target({
										target: result.fetchTargetLabel || result.fetchTarget || '',
									})
								: m.page_scan_via_proxy()}
						{:else}
							{m.page_scan_via_direct()}
						{/if}
					</Badge>
					<Badge variant="default" size="xs">
						{m.page_scan_hosts_count({ count: resources.length })}
					</Badge>
					{#if result.hostsTruncated}
						<Badge variant="warning" size="xs">
							{m.page_scan_hosts_truncated({ shown: resources.length, total: result.totalHosts })}
						</Badge>
					{/if}
					{#if result.bodyTruncated}
						<Badge variant="warning" size="xs">{m.page_scan_body_truncated()}</Badge>
					{/if}
					<span class="muted-meta">{m.page_scan_duration_ms({ ms: result.durationMs })}</span>
				</div>

				<div class="scan-actions">
					{#if result.proxyAvailable && onRescan}
						<Button
							variant="ghost"
							size="sm"
							disabled={loading}
							title={result.fetchVia !== 'proxy' && result.defaultProxy
								? m.page_scan_rescan_proxy_title({ proxy: result.defaultProxy })
								: undefined}
							onclick={() => onRescan(result!.fetchVia === 'proxy' ? 'direct' : 'proxy')}
						>
							{#snippet iconBefore()}<RefreshCw size={12} aria-hidden="true" />{/snippet}
							{result.fetchVia === 'proxy'
								? m.page_scan_rescan_direct()
								: m.page_scan_rescan_proxy()}
						</Button>
					{/if}
					{#if resources.length > 0}
						<Button
							variant="secondary"
							size="sm"
							onclick={() =>
								void copyText(buildClipboardReport(result!), m.page_scan_report_copied())}
						>
							{#snippet iconBefore()}<Copy size={12} aria-hidden="true" />{/snippet}
							{m.page_scan_copy_report()}
						</Button>
					{/if}
				</div>
			</div>

			<div class="scan-submeta">
				<span class="mono final-url" title={result.finalUrl}>{result.finalUrl}</span>
				<span class="muted-meta">{m.page_scan_js_hint()}</span>
			</div>

			{#if resources.length === 0}
				<div class="scan-status">{m.page_scan_empty()}</div>
			{:else}
				<div class="bulk-bar">
					<div class="bulk-select-actions">
						{#if unroutedDomains.length > 0}
							<Button variant="ghost" size="sm" onclick={selectAllUnrouted}>
								{m.page_scan_select_unrouted({ count: unroutedDomains.length })}
							</Button>
						{/if}
						{#if checkedDomainList.length > 0}
							<Button variant="ghost" size="sm" onclick={clearSelection}>
								{m.page_scan_clear_selection()}
							</Button>
						{/if}
						<label class="collapse-toggle">
							<input type="checkbox" bind:checked={collapseRoots} />
							<span>{m.page_scan_collapse_root()}</span>
						</label>
					</div>

					{#if checkedDomainList.length > 0}
						<div class="bulk-apply-controls">
							<span class="muted-meta">
								{m.page_scan_selected_count({ count: effectiveBulkDomains.length })}
							</span>
							{#if allDestKeys.length > 0}
								<select
									class="bulk-select"
									aria-label={m.page_scan_bulk_dest_aria()}
									bind:value={selectedDestKey}
									disabled={bulkSaving}
								>
									{#if compatibleFirc.length > 0}
										<optgroup label={m.ip_inspect_quick_add_group_firc()}>
											{#each compatibleFirc as opt (opt.key)}
												<option value={opt.key}>{opt.label}</option>
											{/each}
										</optgroup>
									{/if}
									{#if compatibleDns.length > 0}
										<optgroup label={m.ip_inspect_quick_add_group_dns()}>
											{#each compatibleDns as opt (opt.key)}
												<option value={opt.key}>{opt.label}</option>
											{/each}
										</optgroup>
									{/if}
								</select>
								<Button
									variant="primary"
									size="sm"
									loading={bulkSaving}
									disabled={effectiveBulkDomains.length === 0 || !selectedDestKey}
									onclick={() => void applyBulkAdd()}
								>
									{m.page_scan_bulk_add_btn({ count: effectiveBulkDomains.length })}
								</Button>
							{:else if !loadingDests}
								<span class="muted-meta">{m.page_scan_bulk_no_dest()}</span>
							{/if}
						</div>
					{/if}
				</div>

				<div class="resource-list">
					{#each resources as item (item.host)}
						{@const isDomain = detectIpFamily(item.host) === 'unknown'}
						{@const hostSnip = snipLongDomain(item.host, 42)}
						{@const ptrSnip = item.inspect?.ptr ? snipLongDomain(item.inspect.ptr, 40) : null}
						<div class="res-row" class:is-routed={item.routed}>
							<div class="res-main">
								<div class="res-left">
									{#if isDomain}
										<input
											type="checkbox"
											class="res-check"
											aria-label={m.page_scan_select_host_aria({ host: item.host })}
											checked={Boolean(selectedHosts[item.host])}
											onchange={() => toggleHost(item.host)}
										/>
									{/if}

									<button
										type="button"
										class="copy-chip mono"
										title={item.host}
										onclick={() => void copyText(item.host)}
									>
										<span class="snip-wrap">
											{#if hostSnip.snipped}
												{hostSnip.prefix}<span class="snip-mid">{hostSnip.middle}</span><span class="snip-dots" aria-hidden="true">…</span>{hostSnip.suffix}
											{:else}
												{hostSnip.prefix}
											{/if}
										</span>
										<Copy size={10} aria-hidden="true" />
									</button>

									{#if item.ip}
										<button
											type="button"
											class="copy-chip mono"
											title={item.ip}
											onclick={() => void copyText(item.ip!)}
										>
											<span>{item.ip}</span>
											<Copy size={10} aria-hidden="true" />
										</button>
									{:else if item.resolveError}
										<span class="err-meta" title={item.resolveError}>
											{m.page_scan_resolve_error()}
										</span>
									{/if}

									<div class="kind-badges">
										{#each item.sources as src (src)}
											<Badge variant="muted" size="xs">{sourceLabel(src)}</Badge>
										{/each}
									</div>
								</div>

								<div class="res-right">
									{#if item.routed}
										<div class="route-pill">
											<CheckCircle2 size={12} aria-hidden="true" />
											{#if item.groupName || item.groupId}
												<Badge variant="accent" size="xs">
													{item.groupName || item.groupId}
												</Badge>
											{/if}
											{#if item.outbound || item.outboundLabel}
												<ArrowRight size={11} aria-hidden="true" />
												<Badge variant="info" size="xs" mono>
													{formatOutbound(item)}
												</Badge>
											{/if}
											{#if item.fakeIp}
												<span class="muted-meta mono">
													{m.page_scan_fakeip_label({ ip: item.fakeIp })}
												</span>
											{/if}
										</div>
									{:else if !item.routeUnknown}
										<Badge variant="warning" size="xs">{m.page_scan_route_direct()}</Badge>
									{/if}

									{#if onOpenQuickAdd}
										<Button
											variant="secondary"
											size="sm"
											onclick={() =>
												onOpenQuickAdd({
													domain: isDomain ? item.host : undefined,
													ip: item.ip || undefined,
													prefix: item.inspect?.prefix || undefined,
												})}
										>
											<Plus size={12} aria-hidden="true" />
											<span>{m.ip_inspect_quick_add_btn().replace(/^\+\s*/, '')}</span>
										</Button>
									{/if}
								</div>
							</div>

							{#if item.inspect && (item.inspect.ptr || item.inspect.asn || item.inspect.organization || item.inspect.prefix)}
								<div class="res-net">
									{#if ptrSnip}
										<span class="net-item mono" title={item.inspect.ptr}>
											→
											{#if ptrSnip.snipped}
												{ptrSnip.prefix}…{ptrSnip.suffix}
											{:else}
												{ptrSnip.prefix}
											{/if}
										</span>
									{/if}
									{#if item.inspect.asn}
										<Badge variant="info" size="xs" mono>AS{item.inspect.asn}</Badge>
									{/if}
									{#if item.inspect.organization}
										<span class="net-org">{item.inspect.organization}</span>
									{/if}
									{#if item.inspect.country}
										<Badge variant="default" size="xs" mono>{item.inspect.country}</Badge>
									{/if}
									{#if item.inspect.prefix}
										<span class="net-prefix mono">{item.inspect.prefix}</span>
									{/if}
								</div>
							{/if}
						</div>
					{/each}
				</div>
			{/if}
		{/if}
	</section>
{/if}

<style>
	.page-scan {
		display: flex;
		flex-direction: column;
		gap: 8px;
		margin-top: 6px;
		padding: 10px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-primary);
		min-width: 0;
	}

	.scan-head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		flex-wrap: wrap;
		gap: 8px;
	}

	.scan-title-row {
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: 6px;
		min-width: 0;
		color: var(--color-text-secondary);
	}

	.scan-title {
		font-size: 10.5px;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.04em;
	}

	.scan-actions {
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: 6px;
	}

	.scan-submeta {
		display: flex;
		flex-direction: column;
		gap: 2px;
		min-width: 0;
	}

	.final-url {
		font-size: 11px;
		color: var(--color-text-secondary);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.scan-status {
		font-size: 11.5px;
		color: var(--color-text-muted);
	}

	.scan-error {
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: 6px;
		font-size: 11.5px;
		color: var(--color-error);
	}

	.scan-error-msg {
		min-width: 0;
		overflow-wrap: anywhere;
	}

	.bulk-bar {
		display: flex;
		align-items: center;
		justify-content: space-between;
		flex-wrap: wrap;
		gap: 8px;
		padding: 6px 8px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-secondary);
	}

	.bulk-select-actions,
	.bulk-apply-controls {
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: 6px;
	}

	.collapse-toggle {
		display: inline-flex;
		align-items: center;
		gap: 5px;
		font-size: 11.5px;
		color: var(--color-text-secondary);
		cursor: pointer;
	}

	.bulk-select {
		height: 28px;
		max-width: 240px;
		padding: 0 8px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-primary);
		color: var(--color-text-primary);
		font-size: 11.5px;
	}

	.resource-list {
		display: flex;
		flex-direction: column;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		overflow: hidden;
	}

	.res-row {
		display: flex;
		flex-direction: column;
		gap: 4px;
		padding: 7px 9px;
		border-bottom: 1px solid var(--color-border);
		background: var(--color-bg-primary);
	}

	.res-row:last-child {
		border-bottom: none;
	}

	.res-row.is-routed {
		background: color-mix(in srgb, var(--color-success, #9ece6a) 5%, var(--color-bg-primary));
	}

	.res-main {
		display: flex;
		align-items: center;
		justify-content: space-between;
		flex-wrap: wrap;
		gap: 6px;
	}

	.res-left,
	.res-right {
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: 6px;
		min-width: 0;
	}

	.res-check {
		cursor: pointer;
	}

	.kind-badges {
		display: inline-flex;
		flex-wrap: wrap;
		gap: 4px;
	}

	.route-pill {
		display: inline-flex;
		align-items: center;
		flex-wrap: wrap;
		gap: 4px;
		color: var(--color-success, #9ece6a);
	}

	.res-net {
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: 6px;
		padding-left: 20px;
		font-size: 11px;
		color: var(--color-text-secondary);
	}

	.net-item,
	.net-prefix {
		color: var(--color-text-muted);
	}

	.net-org {
		color: var(--color-text-primary);
		overflow-wrap: anywhere;
	}

	.err-meta {
		font-size: 11px;
		color: var(--color-warning, var(--color-text-muted));
	}

	.muted-meta {
		font-size: 11px;
		color: var(--color-text-muted);
	}

	.mono {
		font-family: var(--font-mono);
		font-size: 11px;
	}

	.copy-chip {
		all: unset;
		display: inline-flex;
		align-items: center;
		gap: 5px;
		max-width: 100%;
		min-width: 0;
		padding: 2px 6px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-secondary);
		color: var(--color-text-primary);
		cursor: pointer;
	}

	.copy-chip:hover {
		border-color: var(--color-accent-border);
		background: var(--color-bg-hover);
	}

	.copy-chip:focus-visible {
		outline: 2px solid var(--color-accent);
		outline-offset: 1px;
	}

	.snip-wrap {
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.snip-mid {
		position: absolute;
		width: 1px;
		height: 1px;
		overflow: hidden;
		opacity: 0;
	}

	.snip-dots {
		user-select: none;
		color: var(--color-text-muted);
	}
</style>
