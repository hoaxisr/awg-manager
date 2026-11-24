<script lang="ts">
	import { untrack } from 'svelte';
	import { m } from '$lib/i18n';
	import { api } from '$lib/api/client';
	import type { DnsRoute, StaticRouteList } from '$lib/types';
	import type { FircGroup, FircRule } from '$lib/types/firc';
	import { Badge, Button, Modal } from '$lib/components/ui';
	import { notifications } from '$lib/stores/notifications';
	import { fircSyncNonce } from '$lib/stores/firc';
	import { dnsRoutesStore, staticRoutesStore } from '$lib/stores/routing';
	import { isDnsOnlyGroup } from '$lib/utils/fircResolve';
	import { detectFircRuleType } from '$lib/utils/fircBulkRules';
	import { detectIpFamily, extractRootDomain, ipToHostCidr } from '$lib/utils/domainUtils';

	interface Props {
		open: boolean;
		ip?: string;
		domain?: string;
		prefix?: string;
		onclose: () => void;
		onadded?: (destName: string, value: string) => void;
	}

	interface TargetCandidate {
		id: 'root' | 'exact' | 'prefix' | 'host';
		label: string;
		value: string;
		kind: 'domain' | 'cidr';
	}

	interface DestOption {
		key: string;
		label: string;
	}

	let { open, ip = '', domain = '', prefix = '', onclose, onadded }: Props = $props();

	let loadingTargets = $state(false);
	let saving = $state(false);
	let fircGroups = $state<FircGroup[]>([]);
	let dnsRoutes = $state<DnsRoute[]>([]);
	let staticRoutes = $state<StaticRouteList[]>([]);

	let selectedTargetId = $state<'root' | 'exact' | 'prefix' | 'host' | ''>('');
	let selectedDestKey = $state('');

	let candidates = $derived.by((): TargetCandidate[] => {
		const items: TargetCandidate[] = [];
		const cleanDom = domain.trim().replace(/\.+$/, '');
		if (cleanDom && detectIpFamily(cleanDom) === 'unknown') {
			const root = extractRootDomain(cleanDom);
			if (root) {
				items.push({
					id: 'root',
					label: m.ip_inspect_quick_add_target_root(),
					value: root,
					kind: 'domain',
				});
			}
			if (cleanDom.toLowerCase() !== root.toLowerCase()) {
				items.push({
					id: 'exact',
					label: m.ip_inspect_quick_add_target_exact(),
					value: cleanDom.toLowerCase(),
					kind: 'domain',
				});
			}
		}

		const cleanPrefix = prefix.trim();
		if (cleanPrefix) {
			items.push({
				id: 'prefix',
				label: m.ip_inspect_quick_add_target_prefix(),
				value: cleanPrefix,
				kind: 'cidr',
			});
		}

		const cleanIp = ip.trim();
		if (cleanIp && detectIpFamily(cleanIp) !== 'unknown') {
			const hostCidr = ipToHostCidr(cleanIp);
			if (hostCidr && hostCidr !== cleanPrefix) {
				items.push({
					id: 'host',
					label: m.ip_inspect_quick_add_target_host(),
					value: hostCidr,
					kind: 'cidr',
				});
			}
		}

		return items;
	});

	let activeTarget = $derived(
		candidates.find((c) => c.id === selectedTargetId) ?? candidates[0] ?? null,
	);

	let compatibleFirc = $derived<DestOption[]>(
		// Подсети группе «только DNS» не положены: выхода у неё нет.
		fircGroups.filter((g) => activeTarget?.kind === 'domain' || !isDnsOnlyGroup(g)).map((g) => ({
			key: `firc:${g.id}`,
			label: g.name || g.id,
		})),
	);

	let compatibleDns = $derived<DestOption[]>(
		activeTarget?.kind === 'domain'
			? dnsRoutes.map((r) => ({ key: `dns:${r.id}`, label: r.name || r.id }))
			: [],
	);

	let compatibleStatic = $derived<DestOption[]>(
		activeTarget?.kind === 'cidr'
			? staticRoutes.map((r) => ({ key: `static:${r.id}`, label: r.name || r.id }))
			: [],
	);

	let allCompatibleKeys = $derived([
		...compatibleFirc.map((o) => o.key),
		...compatibleDns.map((o) => o.key),
		...compatibleStatic.map((o) => o.key),
	]);

	let wasOpen = false;

	async function loadDestinations(): Promise<void> {
		loadingTargets = true;
		try {
			const [fgRes, dnsRes, staticRes] = await Promise.allSettled([
				typeof api.fircGetGroups === 'function' ? api.fircGetGroups() : Promise.resolve([]),
				typeof api.listDnsRoutes === 'function' ? api.listDnsRoutes() : Promise.resolve([]),
				typeof api.listStaticRoutes === 'function'
					? api.listStaticRoutes()
					: Promise.resolve([]),
			]);
			fircGroups = fgRes.status === 'fulfilled' && Array.isArray(fgRes.value) ? fgRes.value : [];
			dnsRoutes = dnsRes.status === 'fulfilled' && Array.isArray(dnsRes.value) ? dnsRes.value : [];
			staticRoutes =
				staticRes.status === 'fulfilled' && Array.isArray(staticRes.value)
					? staticRes.value
					: [];
		} finally {
			loadingTargets = false;
		}
	}

	$effect(() => {
		if (open && !wasOpen) {
			untrack(() => {
				selectedTargetId = candidates[0]?.id ?? '';
				selectedDestKey = '';
				void loadDestinations();
			});
		}
		wasOpen = open;
	});

	$effect(() => {
		if (!open) return;
		if (allCompatibleKeys.length === 0) {
			selectedDestKey = '';
			return;
		}
		if (!allCompatibleKeys.includes(selectedDestKey)) {
			selectedDestKey = allCompatibleKeys[0];
		}
	});

	function makeRuleId(): string {
		const rand = Math.floor(Math.random() * 0xfffffff)
			.toString(36)
			.padStart(5, '0');
		return `rule_${Date.now().toString(36)}_${rand}`;
	}

	async function handleSubmit(): Promise<void> {
		if (!activeTarget || !selectedDestKey || saving) return;
		const value = activeTarget.value.trim();
		if (!value) return;

		saving = true;
		try {
			if (selectedDestKey.startsWith('firc:')) {
				const groupId = selectedDestKey.slice(5);
				const idx = fircGroups.findIndex((g) => g.id === groupId);
				if (idx < 0) return;
				const group = fircGroups[idx];
				const destName = group.name || group.id;
				const ruleType =
					activeTarget.kind === 'domain' ? 'namespace' : detectFircRuleType(value);
				const valLower = value.toLowerCase();
				const exists = (group.rules ?? []).some(
					(r) => r.type === ruleType && r.rule.trim().toLowerCase() === valLower,
				);
				if (exists) {
					notifications.info(
						m.ip_inspect_quick_add_duplicate({ value, dest: destName }),
					);
					onclose();
					return;
				}
				const newRule: FircRule = {
					id: makeRuleId(),
					type: ruleType,
					rule: value,
					enable: true,
				};
				const nextGroups = fircGroups.map((g, i) =>
					i === idx ? { ...g, rules: [newRule, ...(g.rules ?? [])] } : g,
				);
				await api.fircSaveGroups(nextGroups, true);
				fircGroups = nextGroups;
				fircSyncNonce.update((n) => n + 1);
				notifications.success(m.ip_inspect_quick_add_added({ value, dest: destName }));
				onadded?.(destName, value);
				onclose();
				return;
			}

			if (selectedDestKey.startsWith('dns:')) {
				const routeId = selectedDestKey.slice(4);
				const route = dnsRoutes.find((r) => r.id === routeId);
				if (!route) return;
				const destName = route.name || route.id;
				const valLower = value.toLowerCase();
				const existingAll = [...(route.manualDomains ?? []), ...(route.domains ?? [])];
				if (existingAll.some((d) => d.trim().toLowerCase() === valLower)) {
					notifications.info(
						m.ip_inspect_quick_add_duplicate({ value, dest: destName }),
					);
					onclose();
					return;
				}
				const nextManualDomains = [...(route.manualDomains ?? []), value];
				const baseText = route.manualText ?? (route.manualDomains ?? []).join('\n');
				const nextManualText = baseText.trim()
					? `${baseText.replace(/\s+$/, '')}\n${value}`
					: value;
				const patch: Partial<DnsRoute> = {
					...route,
					manualDomains: nextManualDomains,
					manualText: nextManualText,
				};
				await api.updateDnsRoute(route.id, patch);
				dnsRoutesStore.refetch().catch(() => {});
				notifications.success(m.ip_inspect_quick_add_added({ value, dest: destName }));
				onadded?.(destName, value);
				onclose();
				return;
			}

			if (selectedDestKey.startsWith('static:')) {
				const routeId = selectedDestKey.slice(7);
				const route = staticRoutes.find((r) => r.id === routeId);
				if (!route) return;
				const destName = route.name || route.id;
				const valLower = value.toLowerCase();
				if ((route.subnets ?? []).some((s) => s.trim().toLowerCase() === valLower)) {
					notifications.info(
						m.ip_inspect_quick_add_duplicate({ value, dest: destName }),
					);
					onclose();
					return;
				}
				const patch: StaticRouteList = {
					...route,
					subnets: [...(route.subnets ?? []), value],
				};
				await api.updateStaticRoute(patch);
				staticRoutesStore.refetch().catch(() => {});
				notifications.success(m.ip_inspect_quick_add_added({ value, dest: destName }));
				onadded?.(destName, value);
				onclose();
			}
		} catch (e) {
			notifications.error(
				e instanceof Error && e.message ? e.message : m.ip_inspect_quick_add_failed(),
			);
		} finally {
			saving = false;
		}
	}
</script>

<Modal
	{open}
	{onclose}
	title={m.ip_inspect_quick_add_title()}
	size="md"
	width="520px"
>
	<div class="qa-body">
		{#if candidates.length > 0}
			<div class="qa-field">
				<span class="qa-label">{m.ip_inspect_quick_add_target_label()}</span>
				<div class="qa-chips" role="radiogroup" aria-label={m.ip_inspect_quick_add_target_label()}>
					{#each candidates as cand (cand.id)}
						<button
							type="button"
							role="radio"
							aria-checked={activeTarget?.id === cand.id}
							class="qa-chip"
							class:active={activeTarget?.id === cand.id}
							onclick={() => (selectedTargetId = cand.id)}
						>
							<span class="qa-chip-kind">{cand.label}</span>
							<Badge variant={activeTarget?.id === cand.id ? 'accent' : 'muted'} size="xs" mono>
								{cand.value}
							</Badge>
						</button>
					{/each}
				</div>
			</div>
		{/if}

		<div class="qa-field">
			<label class="qa-label" for="qa-dest-select">
				{m.ip_inspect_quick_add_dest_label()}
			</label>
			{#if loadingTargets}
				<div class="qa-hint">{m.ip_inspect_quick_add_loading()}</div>
			{:else if allCompatibleKeys.length === 0}
				<div class="qa-hint">{m.ip_inspect_quick_add_empty()}</div>
			{:else}
				<select
					id="qa-dest-select"
					class="qa-select"
					bind:value={selectedDestKey}
					disabled={saving}
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
					{#if compatibleStatic.length > 0}
						<optgroup label={m.ip_inspect_quick_add_group_static()}>
							{#each compatibleStatic as opt (opt.key)}
								<option value={opt.key}>{opt.label}</option>
							{/each}
						</optgroup>
					{/if}
				</select>
			{/if}
		</div>
	</div>

	{#snippet actions()}
		<div class="qa-actions">
			<Button variant="ghost" size="sm" onclick={onclose} disabled={saving}>
				{m.common_cancel()}
			</Button>
			<Button
				variant="primary"
				size="sm"
				loading={saving}
				disabled={loadingTargets || !activeTarget || !selectedDestKey}
				onclick={() => void handleSubmit()}
			>
				{m.ip_inspect_quick_add_submit()}
			</Button>
		</div>
	{/snippet}
</Modal>

<style>
	.qa-body {
		display: flex;
		flex-direction: column;
		gap: 14px;
	}

	.qa-field {
		display: flex;
		flex-direction: column;
		gap: 6px;
	}

	.qa-label {
		font-size: 11.5px;
		font-weight: 600;
		color: var(--color-text-secondary);
	}

	.qa-chips {
		display: flex;
		flex-direction: column;
		gap: 6px;
	}

	.qa-chip {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 8px;
		width: 100%;
		padding: 7px 10px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-primary);
		color: var(--color-text-primary);
		font: inherit;
		font-size: 12px;
		cursor: pointer;
		text-align: left;
	}

	.qa-chip:hover {
		border-color: var(--color-accent-border);
		background: var(--color-bg-hover);
	}

	.qa-chip.active {
		border-color: var(--color-accent);
		background: var(--color-accent-tint);
	}

	.qa-chip-kind {
		color: var(--color-text-secondary);
		font-size: 12px;
	}

	.qa-select {
		width: 100%;
		padding: 7px 10px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-primary);
		color: var(--color-text-primary);
		font: inherit;
		font-size: 12.5px;
		outline: none;
	}

	.qa-select:focus {
		border-color: var(--color-accent);
	}

	.qa-hint {
		font-size: 12px;
		color: var(--color-text-muted);
		padding: 6px 0;
	}

	.qa-actions {
		display: flex;
		justify-content: flex-end;
		gap: 8px;
		width: 100%;
	}
</style>
