<script lang="ts" module>
	import type { IpInspectResult } from '$lib/types';

	const MAX_INSPECT_CACHE = 128;
	const inspectCache = new Map<string, IpInspectResult>();

	function readInspectCache(ip: string): IpInspectResult | undefined {
		const hit = inspectCache.get(ip);
		if (!hit) return undefined;
		// Refresh insertion order for basic LRU eviction.
		inspectCache.delete(ip);
		inspectCache.set(ip, hit);
		return hit;
	}

	function writeInspectCache(ip: string, value: IpInspectResult): void {
		if (inspectCache.has(ip)) {
			inspectCache.delete(ip);
		} else if (inspectCache.size >= MAX_INSPECT_CACHE) {
			const oldest = inspectCache.keys().next().value;
			if (oldest !== undefined) inspectCache.delete(oldest);
		}
		inspectCache.set(ip, value);
	}

	/** Clears the module-level IP inspect cache (for deterministic unit tests). */
	export function clearIpInspectCache(): void {
		inspectCache.clear();
	}
</script>

<script lang="ts">
	import { m } from '$lib/i18n';
	import { api } from '$lib/api/client';
	import type { IpInspectScope } from '$lib/types';
	import { Badge, Button } from '$lib/components/ui';
	import { notifications } from '$lib/stores/notifications';
	import { copyToClipboard } from '$lib/utils/clipboard';
	import {
		buildExternalLookupLinks,
		detectIpFamily,
		snipLongDomain,
	} from '$lib/utils/domainUtils';
	import { ExternalLink, Copy, RefreshCw, Plus } from 'lucide-svelte';

	interface Props {
		ip?: string;
		domain?: string;
		compact?: boolean;
		onOpenQuickAdd?: (target: { ip?: string; domain?: string; prefix?: string }) => void;
	}

	let { ip = '', domain = '', compact = false, onOpenQuickAdd }: Props = $props();

	let cleanIp = $derived(ip.trim());
	let cleanDomain = $derived(domain.trim());
	let detectedFamily = $derived(cleanIp ? detectIpFamily(cleanIp) : 'unknown');

	let loading = $state(false);
	let hasError = $state(false);
	let result = $state<IpInspectResult | null>(null);
	let seqCounter = 0;

	async function runInspect(forceRefresh = false): Promise<void> {
		const targetIp = cleanIp;
		const family = detectIpFamily(targetIp);
		if (!targetIp || family === 'unknown') {
			loading = false;
			hasError = false;
			result = null;
			return;
		}

		if (!forceRefresh) {
			const cached = readInspectCache(targetIp);
			if (cached) {
				loading = false;
				hasError = false;
				result = cached;
				return;
			}
		}

		if (typeof api.inspectIp !== 'function') {
			loading = false;
			hasError = false;
			result = null;
			return;
		}

		const seq = ++seqCounter;
		loading = true;
		hasError = false;
		result = null;

		try {
			const res = await api.inspectIp(targetIp);
			if (seq !== seqCounter) return;
			if (!res || typeof res !== 'object') {
				hasError = true;
				return;
			}
			writeInspectCache(targetIp, res);
			result = res;
		} catch {
			if (seq !== seqCounter) return;
			hasError = true;
		} finally {
			if (seq === seqCounter) {
				loading = false;
			}
		}
	}

	$effect(() => {
		const _ip = cleanIp;
		if (!_ip || detectedFamily === 'unknown') {
			seqCounter += 1;
			loading = false;
			hasError = false;
			result = null;
			return;
		}
		void runInspect(false);
	});

	function familyLabel(fam: string): string {
		if (fam === 'nat64') return 'NAT64';
		if (fam === 'ipv6') return 'IPv6';
		if (fam === 'ipv4') return 'IPv4';
		return fam.toUpperCase();
	}

	function scopeLabel(scope: IpInspectScope): string {
		switch (scope) {
			case 'private':
				return m.ip_inspect_scope_private();
			case 'loopback':
				return m.ip_inspect_scope_loopback();
			case 'link-local':
				return m.ip_inspect_scope_link_local();
			case 'cgnat':
				return m.ip_inspect_scope_cgnat();
			case 'benchmark':
				return m.ip_inspect_scope_benchmark();
			case 'multicast':
				return m.ip_inspect_scope_multicast();
			case 'reserved':
				return m.ip_inspect_scope_reserved();
			default:
				return scope;
		}
	}

	async function copyText(val: string): Promise<void> {
		if (!val) return;
		const ok = await copyToClipboard(val);
		if (ok) {
			notifications.success(m.ip_inspect_copied({ value: val }));
		} else {
			notifications.error(m.ip_inspect_copy_failed());
		}
	}

	let externalLinks = $derived(
		buildExternalLookupLinks({
			ip: cleanIp,
			scope: result?.scope,
			domain: cleanDomain,
		}),
	);

	let canQuickAdd = $derived(
		Boolean(onOpenQuickAdd) && Boolean(cleanDomain || (cleanIp && detectedFamily !== 'unknown')),
	);

	let ptrParts = $derived(result?.ptr ? snipLongDomain(result.ptr, compact ? 32 : 40) : null);
	let activeFamily = $derived(result?.family ?? (detectedFamily !== 'unknown' ? detectedFamily : ''));
</script>

{#if cleanIp || cleanDomain}
	<section class="ip-inspect" class:compact aria-label={m.ip_inspect_title()}>
		<div class="inspect-head">
			<div class="inspect-title-row">
				<span class="inspect-title">{m.ip_inspect_title()}</span>
				{#if activeFamily}
					<Badge
						variant={activeFamily === 'nat64' ? 'purple' : activeFamily === 'ipv6' ? 'info' : 'muted'}
						size="xs"
						mono
					>
						{familyLabel(activeFamily)}
					</Badge>
				{/if}
				{#if result && result.scope !== 'public'}
					<Badge
						variant={result.scope === 'benchmark' ? 'warning' : 'muted'}
						size="xs"
					>
						{scopeLabel(result.scope)}
					</Badge>
				{/if}
			</div>

			{#if cleanIp && detectedFamily !== 'unknown' && (result?.scope === 'public' || result?.scope === 'private')}
				<button
					type="button"
					class="icon-btn"
					title={m.ip_inspect_refresh()}
					aria-label={m.ip_inspect_refresh()}
					disabled={loading}
					onclick={() => void runInspect(true)}
				>
					<RefreshCw size={12} aria-hidden="true" />
				</button>
			{/if}
		</div>

		{#if loading}
			<div class="inspect-status">{m.ip_inspect_loading()}</div>
		{:else if hasError}
			<div class="inspect-fail-row">
				<span class="inspect-fail-msg">{m.ip_inspect_error()}</span>
				<Button variant="ghost" size="sm" onclick={() => void runInspect(true)}>
					{m.ip_inspect_retry()}
				</Button>
			</div>
		{:else if result}
			{#if result.ptr || result.asn || result.prefix || result.organization}
				<div class="inspect-kv">
					{#if result.ptr && ptrParts}
						<span class="ik">{m.ip_inspect_ptr_label()}</span>
						<button
							type="button"
							class="copy-chip mono"
							title={result.ptr}
							onclick={() => void copyText(result!.ptr!)}
						>
							<span class="snip-wrap">
								{#if ptrParts.snipped}
									{ptrParts.prefix}<span class="snip-mid">{ptrParts.middle}</span><span class="snip-dots" aria-hidden="true">…</span>{ptrParts.suffix}
								{:else}
									{ptrParts.prefix}
								{/if}
							</span>
							<Copy size={11} aria-hidden="true" />
						</button>
					{/if}

					{#if result.asn || result.organization}
						<span class="ik">{m.ip_inspect_asn_label()}</span>
						<div class="asn-val">
							{#if result.asn}
								<Badge variant="info" size="xs" mono>AS{result.asn}</Badge>
							{/if}
							{#if result.organization}
								<span class="org-text">{result.organization}</span>
							{/if}
							{#if result.country}
								<Badge variant="default" size="xs" mono>{result.country}</Badge>
							{/if}
						</div>
					{/if}

					{#if result.prefix}
						<span class="ik">{m.ip_inspect_prefix_label()}</span>
						<button
							type="button"
							class="copy-chip mono"
							title={result.prefix}
							onclick={() => void copyText(result!.prefix!)}
						>
							<span>{result.prefix}</span>
							<Copy size={11} aria-hidden="true" />
						</button>
					{/if}
				</div>
			{:else if result.scope === 'public'}
				<div class="inspect-status">{m.ip_inspect_no_data()}</div>
			{/if}
		{/if}

		{#if externalLinks.length > 0 || canQuickAdd}
			<div class="inspect-footer">
				{#if externalLinks.length > 0}
					<div class="ext-links">
						{#each externalLinks as link (link.id)}
							<a
								class="ext-link"
								href={link.url}
								target="_blank"
								rel="noopener noreferrer"
								title={link.title}
							>
								<span>{link.label}</span>
								<ExternalLink size={10} aria-hidden="true" />
							</a>
						{/each}
					</div>
				{/if}

				{#if canQuickAdd}
					<Button
						variant="secondary"
						size="sm"
						onclick={() =>
							onOpenQuickAdd?.({
								ip: cleanIp || undefined,
								domain: cleanDomain || undefined,
								prefix: result?.prefix || undefined,
							})}
					>
						<Plus size={12} aria-hidden="true" />
						<span>{m.ip_inspect_quick_add_btn().replace(/^\+\s*/, '')}</span>
					</Button>
				{/if}
			</div>
		{/if}
	</section>
{/if}

<style>
	.ip-inspect {
		display: flex;
		flex-direction: column;
		gap: 8px;
		padding: 10px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-primary);
	}

	.ip-inspect.compact {
		padding: 8px 10px;
		gap: 6px;
		margin-top: 6px;
	}

	.inspect-head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 8px;
	}

	.inspect-title-row {
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: 6px;
		min-width: 0;
	}

	.inspect-title {
		font-size: 10.5px;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		color: var(--color-text-secondary);
	}

	.icon-btn {
		all: unset;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: 22px;
		height: 22px;
		border-radius: var(--radius-sm);
		color: var(--color-text-muted);
		cursor: pointer;
	}

	.icon-btn:hover:not(:disabled) {
		background: var(--color-bg-hover);
		color: var(--color-text-primary);
	}

	.icon-btn:disabled {
		opacity: 0.45;
		cursor: default;
	}

	.inspect-status {
		font-size: 11.5px;
		color: var(--color-text-muted);
	}

	.inspect-fail-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 8px;
		font-size: 11.5px;
		color: var(--color-error);
	}

	.inspect-fail-msg {
		min-width: 0;
	}

	.inspect-kv {
		display: grid;
		grid-template-columns: auto 1fr;
		gap: 5px 8px;
		align-items: center;
		font-size: 11.5px;
	}

	.ik {
		color: var(--color-text-muted);
		white-space: nowrap;
	}

	.mono {
		font-family: var(--font-mono);
		font-size: 11px;
	}

	.copy-chip {
		all: unset;
		display: inline-flex;
		align-items: center;
		justify-content: space-between;
		gap: 6px;
		max-width: 100%;
		min-width: 0;
		padding: 2px 6px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-secondary);
		color: var(--color-text-primary);
		cursor: pointer;
		justify-self: start;
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

	.asn-val {
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: 5px;
		min-width: 0;
	}

	.org-text {
		color: var(--color-text-primary);
		font-size: 11.5px;
		overflow-wrap: anywhere;
	}

	.inspect-footer {
		display: flex;
		align-items: center;
		justify-content: space-between;
		flex-wrap: wrap;
		gap: 6px;
		padding-top: 2px;
	}

	.ext-links {
		display: flex;
		flex-wrap: wrap;
		gap: 4px;
	}

	.ext-link {
		display: inline-flex;
		align-items: center;
		gap: 3px;
		padding: 2px 6px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-secondary);
		color: var(--color-text-secondary);
		font-size: 10.5px;
		font-weight: 500;
		text-decoration: none;
		transition: border-color var(--t-fast), color var(--t-fast);
	}

	.ext-link:hover {
		color: var(--color-text-primary);
		border-color: var(--color-accent-border);
	}
</style>
