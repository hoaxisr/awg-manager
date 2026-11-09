<script lang="ts">
	import { m } from '$lib/i18n';
	import { autolistStatus } from '$lib/stores/autolist';
	import { notifications } from '$lib/stores/notifications';
	import { api } from '$lib/api/client';
	import { buildAutolistConfigPayload } from './configPayload';
	import type {
		AutolistCandidate,
		AutolistConfig,
		AutolistHistoryEntry,
		SingboxRouterDNSServer,
		SingboxRouterRuleSet,
	} from '$lib/types';
	import { Button, IconButton, Modal, Toggle, Dropdown, Input, ChipMultiSelect, Tabs } from '$lib/components/ui';
	import { PageHeader } from '$lib/components/layout';
	import {
		RotateCw,
		EyeOff,
		Eye,
		CircleCheck,
		CircleAlert,
		CircleDashed,
		Settings2,
		TriangleAlert,
		ShieldPlus,
		Ban,
		Power,
	} from 'lucide-svelte';

	let status = $derived($autolistStatus.data);
	let loading = $derived($autolistStatus.lastFetchedAt === 0 && $autolistStatus.status !== 'error');
	let running = $derived(status?.running === true);
	let probeLimitExhausted = $derived(
		(status?.probesLimit ?? 0) > 0 && (status?.probesUsedHour ?? 0) >= (status?.probesLimit ?? 0),
	);
	let addLimitExhausted = $derived(
		(status?.addsLimit ?? 0) > 0 && (status?.addsUsedHour ?? 0) >= (status?.addsLimit ?? 0),
	);
	let candidates = $derived(status?.candidates ?? []);
	let history = $derived(status?.history ?? []);
	let view = $state<'candidates' | 'history'>('candidates');
	let viewTabs = $derived([
		{ id: 'candidates', label: m.autolist_view_candidates(), badge: candidates.length },
		{ id: 'history', label: m.autolist_view_history(), badge: history.length },
	]);

	let selected = $state<Set<string>>(new Set());
	let refreshing = $state(false);
	let busy = $state(false);
	let excludeBusy = $state(false);
	let lifecycleBusy = $state(false);
	let restoreBusy = $state(false);
	let probingHosts = $state<Set<string>>(new Set());
	let addingDomains = $state<Set<string>>(new Set());
	let settingsOpen = $state(false);
	let autoConfirmOpen = $state(false);

	// Локальная копия конфига для модалки настроек
	let cfgDraft = $state<AutolistConfig>({ autoAdd: false, targetInterface: '', targetOutbound: '' });
	let cfgLoading = $state(false);

	let allSelected = $derived(candidates.length > 0 && candidates.every((c) => selected.has(c.domain)));
	let selectedCount = $derived(selected.size);

	let dnsServers = $state<SingboxRouterDNSServer[]>([]);
	let probeInterfaces = $state<Array<{ name: string; label: string }>>([]);
	let exclusionRuleSets = $state<SingboxRouterRuleSet[]>([]);
	let probeResourcesFetched = $state(false);

	let targetInterfaceOptions = $derived.by(() => {
		const opts: { value: string; label: string }[] = [];
		for (const iface of probeInterfaces) {
			opts.push({ value: iface.name, label: `${iface.label} (${iface.name})` });
		}
		const current = cfgDraft.targetInterface ?? '';
		if (current && !opts.some((o) => o.value === current)) {
			opts.push({ value: current, label: current });
		}
		return opts;
	});
	let autoAddAvailable = $derived(
		cfgDraft.tunnelProbe === true && (cfgDraft.targetInterface?.trim() ?? '') !== '',
	);

	let dnsOptions = $derived.by(() => {
		const opts: { value: string; label: string; description?: string; disabled?: boolean }[] = [
			{ value: 'awgm', label: m.autolist_cfg_dns_awgm() },
			{ value: 'system', label: m.autolist_cfg_dns_system() },
		];
		for (const server of dnsServers) {
			const supportedTransport = server.type === 'udp' || server.type === 'https' || server.type === 'tls';
			const disabled = !supportedTransport || !!server.detour;
			const description = !supportedTransport
				? m.autolist_cfg_dns_unsupported()
				: server.detour
					? m.autolist_cfg_dns_detour()
					: `${server.type.toUpperCase()} · ${server.server}`;
			opts.push({ value: `sb:${server.tag}`, label: server.tag, description, disabled });
		}
		const current = cfgDraft.resolveVia ?? '';
		if (current && current !== 'awgm' && current !== 'system' && !opts.some((o) => o.value === current)) {
			opts.push({ value: current, label: current });
		}
		return opts;
	});

	let probeInterfaceOptions = $derived.by(() => {
		const opts: { value: string; label: string }[] = [{ value: '', label: m.autolist_cfg_probe_interface_auto() }];
		for (const iface of probeInterfaces) {
			opts.push({ value: iface.name, label: `${iface.label} (${iface.name})` });
		}
		const current = cfgDraft.probeInterface ?? '';
		if (current && !opts.some((o) => o.value === current)) {
			opts.push({ value: current, label: current });
		}
		return opts;
	});

	let exclusionRuleSetOptions = $derived(exclusionRuleSets.map((rs) => ({ value: rs.tag, label: rs.tag })));

	async function fetchProbeResources() {
		if (probeResourcesFetched) return;
		probeResourcesFetched = true;
		const [dns, interfaces, ruleSets] = await Promise.allSettled([
			api.singboxRouterListDNSServers(),
			api.getAllInterfaces(),
			api.singboxRouterListRuleSets(),
		]);
		if (dns.status === 'fulfilled') dnsServers = dns.value;
		if (interfaces.status === 'fulfilled') {
			probeInterfaces = interfaces.value
				.filter((iface) => iface.up && iface.securityLevel === 'public')
				.map((iface) => ({ name: iface.name, label: iface.label }));
		}
		if (ruleSets.status === 'fulfilled') exclusionRuleSets = ruleSets.value;
	}

	function toggle(domain: string) {
		if (addingDomains.has(domain)) return;
		const next = new Set(selected);
		if (next.has(domain)) next.delete(domain);
		else next.add(domain);
		selected = next;
	}

	function toggleAll() {
		if (addingDomains.size > 0) return;
		selected = allSelected ? new Set() : new Set(candidates.map((c) => c.domain));
	}

	async function refresh() {
		refreshing = true;
		try {
			await autolistStatus.refetch();
		} finally {
			refreshing = false;
		}
	}

	async function setSatelliteEnabled(enabled: boolean) {
		if (lifecycleBusy) return;
		lifecycleBusy = true;
		try {
			await api.controlAutolist(enabled ? 'enable' : 'disable');
			notifications.success(enabled ? m.autolist_enable_ok() : m.autolist_disable_ok());
			await autolistStatus.refetch();
		} catch {
			notifications.error(m.autolist_control_err());
		} finally {
			lifecycleBusy = false;
		}
	}

	async function addSelected() {
		if (selected.size === 0 || busy || addingDomains.size > 0) return;
		const domains = [...selected];
		busy = true;
		addingDomains = new Set(domains);
		try {
			const results = await api.addAutolistCandidates(domains);
			const failed = Object.entries(results).filter(([, err]) => !!err);
			if (failed.length === 0) {
				notifications.success(m.autolist_add_ok({ count: domains.length }));
			} else {
				notifications.error(m.autolist_add_partial({ failed: failed.length }));
			}
			selected = new Set([...selected].filter((d) => !domains.includes(d)));
			await autolistStatus.refetch();
		} catch {
			notifications.error(m.autolist_add_err());
		} finally {
			busy = false;
			addingDomains = new Set();
		}
	}
	async function excludeForever(domains: string[]) {
		if (domains.length === 0 || excludeBusy || domains.some((d) => addingDomains.has(d))) return;
		excludeBusy = true;
		try {
			await api.excludeAutolistCandidates(domains);
			notifications.success(m.autolist_exclude_ok());
			selected = new Set([...selected].filter((d) => !domains.includes(d)));
			await autolistStatus.refetch();
		} catch {
			notifications.error(m.autolist_exclude_err());
		} finally {
			excludeBusy = false;
		}
	}

	async function dismiss(c: AutolistCandidate) {
		if (addingDomains.has(c.domain)) return;
		try {
			await api.dismissAutolistCandidate(c.domain);
			notifications.success(m.autolist_dismiss_ok());
			await autolistStatus.refetch();
		} catch {
			notifications.error(m.autolist_dismiss_err());
		}
	}

	async function restoreHistory(h: AutolistHistoryEntry) {
		if (restoreBusy) return;
		restoreBusy = true;
		try {
			await api.restoreAutolistCandidate(h.domain);
			notifications.success(m.autolist_restore_ok());
			await autolistStatus.refetch();
		} catch {
			notifications.error(m.autolist_restore_err());
		} finally {
			restoreBusy = false;
		}
	}

	async function probeHost(c: AutolistCandidate) {
		if (probingHosts.has(c.domain) || addingDomains.has(c.domain)) return;
		probingHosts = new Set(probingHosts).add(c.domain);
		try {
			const result = await api.probeAutolistHost(c.host || c.domain);
			if (result === 'queued') {
				notifications.success(m.autolist_probe_queued());
			} else {
				notifications.error(m.autolist_probe_busy());
			}
			await autolistStatus.refetch();
		} catch {
			notifications.error(m.autolist_probe_err());
		} finally {
			const next = new Set(probingHosts);
			next.delete(c.domain);
			probingHosts = next;
		}
	}

	async function openSettings() {
		settingsOpen = true;
		cfgLoading = true;
		void fetchProbeResources();
		try {
			const cfgLoaded = await api.getAutolistConfig();
			cfgDraft = {
				autoAdd: cfgLoaded.autoAdd,
				targetInterface: cfgLoaded.targetInterface ?? '',
				targetOutbound: cfgLoaded.targetOutbound ?? '',
				resolveVia: cfgLoaded.resolveVia,
				resolveIPEnabled: cfgLoaded.resolveIPEnabled,
				probeInterface: cfgLoaded.probeInterface ?? '',
				verbose: cfgLoaded.verbose ?? false,
				packetEnabled: cfgLoaded.packetEnabled ?? false,
				instant: cfgLoaded.instant ?? false,
				tunnelProbe: cfgLoaded.tunnelProbe ?? false,
				wanCheckUrl: cfgLoaded.wanCheckUrl ?? 'https://cp.cloudflare.com/generate_204',
				excludeDomains: cfgLoaded.excludeDomains ?? [],
				excludeRuleSets: cfgLoaded.excludeRuleSets ?? [],
			};
		} catch {
			notifications.error(m.autolist_cfg_read_err());
		} finally {
			cfgLoading = false;
		}
	}

	function setExcludedDomains(raw: string) {
		const domains = raw
			.split(/[\s,]+/)
			.map((d) => d.trim().toLowerCase().replace(/\.$/, ''))
			.filter((d) => d !== '');
		cfgDraft = { ...cfgDraft, excludeDomains: [...new Set(domains)] };
	}

	function toggleAuto() {
		if (!autoAddAvailable) return;
		if (!cfgDraft.autoAdd) {
			// включаем — сначала предупреждение
			autoConfirmOpen = true;
		} else {
			cfgDraft = { ...cfgDraft, autoAdd: false };
		}
	}

	async function confirmAuto() {
		if (!autoAddAvailable) return;
		autoConfirmOpen = false;
		cfgDraft = { ...cfgDraft, autoAdd: true };
		await saveConfig();
	}

	async function saveConfig() {
		busy = true;
		try {
			await api.setAutolistConfig(buildAutolistConfigPayload(cfgDraft));
			notifications.success(m.autolist_cfg_saved());
			settingsOpen = false;
			await autolistStatus.refetch();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.autolist_cfg_save_err());
		} finally {
			busy = false;
		}
	}

	function whyText(why: string): string {
		switch (why) {
			case 'no-answer': return m.autolist_why_no_answer();
			case 'reset': return m.autolist_why_reset();
			case 'freeze': return m.autolist_why_freeze();
			case 'no-connect': return m.autolist_why_no_connect();
			default: return why;
		}
	}

	function stateText(state: string, stage?: string): string {
		if (stage === 'direct') return m.autolist_stage_direct();
		if (stage === 'tunnel') return m.autolist_stage_tunnel();
		switch (state) {
			case 'candidate': return m.autolist_state_candidate();
			case 'checking': return m.autolist_state_checking();
			default: return state;
		}
	}

	function historyActionText(action: AutolistHistoryEntry['action']): string {
		switch (action) {
			case 'dismissed': return m.autolist_history_dismissed();
			case 'excluded': return m.autolist_history_excluded();
		}
	}

	function candidateSourceText(source: string): string {
		switch (source) {
			case 'packet': return m.autolist_source_packet();
			case 'stall': return m.autolist_source_stall();
			case 'dial': return m.autolist_source_dial();
			case 'open': return m.autolist_source_open();
			default: return source;
		}
	}

	function historySourceText(source: AutolistHistoryEntry['source']): string {
		switch (source) {
			case 'user': return m.autolist_source_user();
			case 'auto': return m.autolist_source_auto();
			case 'reachable': return m.autolist_source_reachable();
			default: return source;
		}
	}

	function verdictText(v?: string): string {
		switch (v) {
			case 'blocked': return m.autolist_verdict_blocked();
			case 'reachable': return m.autolist_verdict_reachable();
			case 'unknown': return m.autolist_verdict_unknown();
			default: return '—';
		}
	}

	function tunnelVerdictText(v?: string): string {
		switch (v) {
			case 'reachable': return m.autolist_tunnel_reachable();
			case 'blocked': return m.autolist_tunnel_blocked();
			case 'unknown': return m.autolist_verdict_unknown();
			default: return m.autolist_tunnel_not_run();
		}
	}

	function timeText(iso?: string): string {
		if (!iso) return '—';
		try {
			return new Date(iso).toLocaleString();
		} catch {
			return iso;
		}
	}
</script>

<PageHeader title={m.autolist_tab_title()} />
<div class="flex flex-col gap-4">
	<div class="flex items-center gap-4 flex-wrap">
		<p class="autolist-intro text-sm opacity-70 max-w-3xl">{m.autolist_tab_desc()}</p>
		{#if running}
			<div class="autolist-controls flex items-center gap-2 flex-wrap justify-end">
				<span class="flex items-center gap-1.5 text-sm mr-2">
					<CircleCheck size={16} class="text-success" aria-hidden="true" />
					{m.autolist_running()}
				</span>
				{#if probeLimitExhausted}
					<span class="badge badge-warning badge-sm gap-1">
						<TriangleAlert size={13} aria-hidden="true" />
						{m.autolist_limit_probes_exhausted({ limit: status?.probesLimit ?? 0 })}
					</span>
				{/if}
				{#if addLimitExhausted}
					<span class="badge badge-warning badge-sm gap-1">
						<TriangleAlert size={13} aria-hidden="true" />
						{m.autolist_limit_adds_exhausted({ limit: status?.addsLimit ?? 0 })}
					</span>
				{/if}
				<Button variant="secondary" size="sm" onclick={() => void setSatelliteEnabled(false)} loading={lifecycleBusy}>
					{#snippet iconBefore()}<Power size={14} strokeWidth={2} aria-hidden="true" />{/snippet}
					{m.autolist_disable()}
				</Button>
				<Button variant="secondary" size="sm" onclick={openSettings}>
					{#snippet iconBefore()}<Settings2 size={14} strokeWidth={2} aria-hidden="true" />{/snippet}
					{m.autolist_settings()}
				</Button>
				<Button variant="secondary" size="sm" onclick={refresh} disabled={refreshing}>
					{#snippet iconBefore()}<RotateCw size={14} strokeWidth={2} aria-hidden="true" />{/snippet}
					{m.common_refresh()}
				</Button>
			</div>
		{/if}
	</div>

	{#if loading}
		<div class="card p-6 text-sm opacity-60">…</div>
	{:else if !running}
		<div class="card p-6 flex flex-col gap-3">
			<div class="flex items-center gap-2 font-medium">
				<CircleAlert size={18} class="text-warning" aria-hidden="true" />
				{status?.enabled === false ? m.autolist_disabled() : m.autolist_not_running()}
			</div>
			<p class="text-sm opacity-70 break-all">
				{status?.enabled === false ? m.autolist_disabled_hint() : m.autolist_not_running_hint()}
			</p>
			{#if status?.installed}
				<div>
					<Button variant="primary" size="sm" onclick={() => void setSatelliteEnabled(true)} loading={lifecycleBusy}>
						{#snippet iconBefore()}<Power size={14} strokeWidth={2} aria-hidden="true" />{/snippet}
						{status?.enabled === false ? m.autolist_enable() : m.common_start()}
					</Button>
				</div>
			{/if}
		</div>
	{:else}
		<Tabs tabs={viewTabs} active={view} onchange={(id) => (view = id as 'candidates' | 'history')} />

		{#if view === 'candidates'}
		{#if candidates.length === 0}
			<div class="card p-6 flex flex-col gap-2">
				<div class="flex items-center gap-2 font-medium">
					<CircleDashed size={18} aria-hidden="true" />
					{m.autolist_empty()}
				</div>
				<p class="text-sm opacity-70">{m.autolist_empty_hint()}</p>
			</div>
		{:else}
			<!-- панель массовых действий -->
			<div class="flex items-center gap-2 flex-wrap">
				<label class="flex items-center gap-2 text-sm cursor-pointer select-none">
					<input type="checkbox" checked={allSelected} onchange={toggleAll} disabled={addingDomains.size > 0} />
					{m.autolist_select_all()}
				</label>
				<span class="text-xs opacity-50">{m.autolist_selected({ count: selectedCount })}</span>
				<div class="grow"></div>
				<Button
					variant="primary"
					size="sm"
					onclick={addSelected}
					disabled={selectedCount === 0 || busy || addingDomains.size > 0}
					loading={busy}
				>
					{#snippet iconBefore()}<ShieldPlus size={14} strokeWidth={2} aria-hidden="true" />{/snippet}
					{m.autolist_add_selected()}
				</Button>
				<Button
					variant="secondary"
					size="sm"
					onclick={() => void excludeForever([...selected])}
					disabled={selectedCount === 0 || excludeBusy || addingDomains.size > 0}
					loading={excludeBusy}
				>
					{#snippet iconBefore()}<Ban size={14} strokeWidth={2} aria-hidden="true" />{/snippet}
					{m.autolist_exclude_selected()}
				</Button>
			</div>

			<div class="card overflow-x-auto">
				<table class="w-full text-sm">
					<thead>
						<tr class="text-left opacity-60 border-b border-base-300">
							<th class="px-3 py-2.5 w-8"></th>
							<th class="px-4 py-2.5 font-medium">{m.autolist_col_domain()}</th>
							<th class="px-4 py-2.5 font-medium">{m.autolist_col_why()}</th>
							<th class="px-4 py-2.5 font-medium">{m.autolist_col_fails()}</th>
							<th class="px-4 py-2.5 font-medium">{m.autolist_col_state()}</th>
							<th class="px-4 py-2.5 font-medium">{m.autolist_col_verdict()}</th>
							<th class="px-4 py-2.5 font-medium">{m.autolist_col_tunnel_probe()}</th>
							<th class="px-4 py-2.5 font-medium">{m.autolist_last_at()}</th>
							<th class="px-4 py-2.5"></th>
						</tr>
					</thead>
					<tbody>
						{#each candidates as c (c.domain)}
							<tr class="border-b border-base-200 last:border-0">
								<td class="px-3 py-2.5">
									<input
										type="checkbox"
										checked={selected.has(c.domain)}
										onchange={() => toggle(c.domain)}
										disabled={addingDomains.has(c.domain)}
										aria-label={c.domain}
									/>
								</td>
								<td class="px-4 py-2.5 font-mono">
									{c.domain}
									{#if c.sources.length > 0}
										<span class="ml-1.5 text-xs opacity-50">
											{c.sources.map(candidateSourceText).join(', ')}
										</span>
									{/if}
								</td>
								<td class="px-4 py-2.5">{whyText(c.why)}</td>
								<td class="px-4 py-2.5 tabular-nums">{c.fails}</td>
								<td class="px-4 py-2.5">
									<span
										class="badge badge-sm"
										class:badge-warning={addingDomains.has(c.domain) || c.state === 'candidate' || c.state === 'checking'}
									>
										{addingDomains.has(c.domain) ? m.autolist_adding() : stateText(c.state, c.stage)}
									</span>
								</td>
								<td class="px-4 py-2.5">
									<div class="flex flex-col gap-1">
										<span>{verdictText(c.verdict)}</span>
										{#if c.probeError}<span class="text-xs text-error break-all">{c.probeError}</span>{/if}
									</div>
								</td>
								<td class="px-4 py-2.5">
									<div class="flex flex-col gap-1">
										<span>{tunnelVerdictText(c.tunnelVerdict)}</span>
										{#if c.tunnelError}<span class="text-xs text-error break-all">{c.tunnelError}</span>{/if}
									</div>
								</td>
								<td class="px-4 py-2.5 opacity-70">{timeText(c.lastAt)}</td>
								<td class="px-4 py-2.5 text-right">
									<IconButton
										size="sm"
										variant="default"
										ariaLabel={m.autolist_action_probe()}
										title={m.autolist_action_probe_hint()}
										loading={probingHosts.has(c.domain)}
										disabled={addingDomains.has(c.domain)}
										onclick={() => void probeHost(c)}
									>
										<RotateCw size={14} strokeWidth={2} aria-hidden="true" />
									</IconButton>
									<IconButton
										size="sm"
										variant="default"
										ariaLabel={m.autolist_action_exclude()}
										title={m.autolist_action_exclude_hint()}
										disabled={excludeBusy || addingDomains.has(c.domain)}
										onclick={() => void excludeForever([c.domain])}
									>
										<Ban size={14} strokeWidth={2} aria-hidden="true" />
									</IconButton>
									<IconButton
										size="sm"
										variant="default"
										ariaLabel={m.autolist_action_dismiss()}
										title={m.autolist_action_dismiss_hint()}
										disabled={addingDomains.has(c.domain)}
										onclick={() => void dismiss(c)}
									>
										<EyeOff size={14} strokeWidth={2} aria-hidden="true" />
									</IconButton>
								</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
		{/if}
		{:else}
			{#if history.length === 0}
				<div class="card p-6 flex flex-col gap-2">
					<div class="flex items-center gap-2 font-medium">
						<CircleDashed size={18} aria-hidden="true" />
						{m.autolist_history_empty()}
					</div>
					<p class="text-sm opacity-70">{m.autolist_history_empty_hint()}</p>
				</div>
			{:else}
				<div class="card overflow-x-auto">
					<table class="w-full text-sm">
						<thead>
							<tr class="text-left opacity-60 border-b border-base-300">
								<th class="px-4 py-2.5 font-medium">{m.autolist_col_domain()}</th>
								<th class="px-4 py-2.5 font-medium">{m.autolist_col_action()}</th>
								<th class="px-4 py-2.5 font-medium">{m.autolist_col_why()}</th>
								<th class="px-4 py-2.5 font-medium">{m.autolist_col_source()}</th>
								<th class="px-4 py-2.5 font-medium">{m.autolist_history_at()}</th>
								<th class="px-4 py-2.5"></th>
							</tr>
						</thead>
						<tbody>
							{#each history as h (`${h.at}:${h.action}:${h.domain}`)}
								<tr class="border-b border-base-200 last:border-0">
									<td class="px-4 py-2.5 font-mono">
										{h.domain}
										{#if h.host && h.host !== h.domain}<div class="text-xs opacity-50">{h.host}</div>{/if}
									</td>
									<td class="px-4 py-2.5">
										<span
											class="badge badge-sm"
											class:badge-warning={h.action === 'dismissed'}
											class:badge-ghost={h.action === 'excluded'}
										>
											{historyActionText(h.action)}
										</span>
									</td>
									<td class="px-4 py-2.5">{h.why ? whyText(h.why) : '—'}</td>
									<td class="px-4 py-2.5 opacity-70">{historySourceText(h.source)}</td>
									<td class="px-4 py-2.5 opacity-70">{timeText(h.at)}</td>
									<td class="px-4 py-2.5 text-right">
										{#if h.action === 'dismissed' || h.action === 'excluded'}
											<IconButton
												size="sm"
												variant="default"
												ariaLabel={m.autolist_action_restore()}
												title={m.autolist_action_restore_hint()}
												disabled={restoreBusy}
												onclick={() => void restoreHistory(h)}
											>
												<Eye size={14} strokeWidth={2} aria-hidden="true" />
											</IconButton>
										{/if}
									</td>
								</tr>
							{/each}
						</tbody>
					</table>
				</div>
			{/if}
		{/if}

		{#if status?.lastError}
			<div class="text-xs text-error">{status.lastError}</div>
		{/if}
	{/if}
</div>

<!-- Настройки сателлита -->
<Modal open={settingsOpen} onclose={() => (settingsOpen = false)} title={m.autolist_settings()} size="wide">
	<div class="flex flex-col gap-4">
		{#if cfgLoading}
			<p class="text-sm opacity-60">…</p>
		{:else}
			<div class="autolist-settings-grid">
			<section class="flex flex-col gap-4">
				<h3 class="text-sm font-semibold opacity-70">{m.autolist_cfg_group_probe()}</h3>
			<div class="flex flex-col gap-1.5">
				<span class="text-sm font-medium">{m.autolist_cfg_outbound()}</span>
				<Dropdown
					value={cfgDraft.targetInterface || ''}
					options={targetInterfaceOptions}
					placeholder={m.autolist_cfg_outbound_placeholder()}
					onchange={(v) => (cfgDraft = { ...cfgDraft, targetInterface: v })}
					fullWidth
				/>
				<span class="text-xs opacity-60">{m.autolist_cfg_outbound_hint()}</span>
			</div>

			<div class="flex flex-col gap-1.5">
				<span class="text-sm font-medium">{m.autolist_cfg_dns()}</span>
				<Dropdown
					value={cfgDraft.resolveVia || 'awgm'}
					options={dnsOptions}
					onchange={(v) => (cfgDraft = { ...cfgDraft, resolveVia: v })}
					fullWidth
				/>
				<span class="text-xs opacity-60">{m.autolist_cfg_dns_hint()}</span>
			</div>

			<div class="flex flex-col gap-1.5">
				<span class="text-sm font-medium">{m.autolist_cfg_probe_interface()}</span>
				<Dropdown
					value={cfgDraft.probeInterface || ''}
					options={probeInterfaceOptions}
					onchange={(v) => (cfgDraft = { ...cfgDraft, probeInterface: v })}
					fullWidth
				/>
				<span class="text-xs opacity-60">{m.autolist_cfg_probe_interface_hint()}</span>
			</div>

			<div class="flex flex-col gap-1.5">
				<Input
					type="url"
					label={m.autolist_cfg_wan_url()}
					value={cfgDraft.wanCheckUrl ?? 'https://cp.cloudflare.com/generate_204'}
					oninput={(v) => (cfgDraft = { ...cfgDraft, wanCheckUrl: v })}
					hint={m.autolist_cfg_wan_url_hint()}
					fullWidth
				/>
			</div>
			<div class="flex items-start justify-between gap-3">
				<span class="flex flex-col gap-1">
					<span class="text-sm font-medium">{m.autolist_cfg_packet()}</span>
					<span class="text-xs opacity-70">{m.autolist_cfg_packet_hint()}</span>
				</span>
				<Toggle
					checked={cfgDraft.packetEnabled ?? false}
					controlled
					onchange={(v) => (cfgDraft = { ...cfgDraft, packetEnabled: v })}
					ariaLabel={m.autolist_cfg_packet()}
				/>
			</div>

			<div class="flex items-start justify-between gap-3">
				<span class="flex flex-col gap-1">
					<span class="text-sm font-medium">{m.autolist_cfg_instant()}</span>
					<span class="text-xs opacity-70">{m.autolist_cfg_instant_hint()}</span>
				</span>
				<Toggle
					checked={cfgDraft.instant ?? false}
					controlled
					onchange={(v) => (cfgDraft = { ...cfgDraft, instant: v })}
					ariaLabel={m.autolist_cfg_instant()}
				/>
			</div>

			<div class="flex items-start justify-between gap-3">
				<span class="flex flex-col gap-1">
					<span class="text-sm font-medium">{m.autolist_cfg_tunnel_probe()}</span>
					<span class="text-xs opacity-70">{m.autolist_cfg_tunnel_probe_hint()}</span>
				</span>
				<Toggle
					checked={cfgDraft.tunnelProbe ?? false}
					controlled
					onchange={(v) => (cfgDraft = { ...cfgDraft, tunnelProbe: v, autoAdd: v ? cfgDraft.autoAdd : false })}
					ariaLabel={m.autolist_cfg_tunnel_probe()}
				/>
			</div>

			<div class="flex items-start justify-between gap-3">
				<span class="flex flex-col gap-1">
					<span class="text-sm font-medium">{m.autolist_cfg_resolve_ip()}</span>
					<span class="text-xs opacity-70">{m.autolist_cfg_resolve_ip_hint()}</span>
				</span>
				<Toggle
					checked={cfgDraft.resolveIPEnabled ?? true}
					controlled
					onchange={(v) => (cfgDraft = { ...cfgDraft, resolveIPEnabled: v })}
					ariaLabel={m.autolist_cfg_resolve_ip()}
				/>
			</div>
			</section>

			<section class="flex flex-col gap-4">
				<h3 class="text-sm font-semibold opacity-70">{m.autolist_cfg_group_rules()}</h3>
			<div class="flex flex-col gap-1.5">
				<span class="text-sm font-medium">{m.autolist_cfg_exclude_domains()}</span>
				<textarea
					class="input-control font-mono"
					rows="3"
					value={cfgDraft.excludeDomains?.join('\n') ?? ''}
					oninput={(e) => setExcludedDomains(e.currentTarget.value)}
					placeholder="example.com, sub.example.org"
				></textarea>
				<span class="text-xs opacity-60">{m.autolist_cfg_exclude_domains_hint()}</span>
			</div>

			<div class="flex flex-col gap-1.5">
				<span class="text-sm font-medium">{m.autolist_cfg_exclude_rulesets()}</span>
				<ChipMultiSelect
					values={cfgDraft.excludeRuleSets ?? []}
					options={exclusionRuleSetOptions}
					onchange={(values) => (cfgDraft = { ...cfgDraft, excludeRuleSets: values })}
					allowOrphans
				/>
				<span class="text-xs opacity-60">{m.autolist_cfg_exclude_rulesets_hint()}</span>
			</div>









			<div class="flex items-start justify-between gap-3">
				<span class="flex flex-col gap-1">
					<span class="text-sm font-medium">{m.autolist_cfg_verbose()}</span>
					<span class="text-xs opacity-70">{m.autolist_cfg_verbose_hint()}</span>
				</span>
				<Toggle
					checked={cfgDraft.verbose ?? false}
					controlled
					onchange={(v) => (cfgDraft = { ...cfgDraft, verbose: v })}
					ariaLabel={m.autolist_cfg_verbose()}
				/>
			</div>

			<div class="autolist-warning flex items-start justify-between gap-3 rounded-lg bg-warning/10 border border-warning/30">
				<span class="flex flex-col gap-1">
					<span class="text-sm font-medium flex items-center gap-1.5">
						<TriangleAlert size={15} class="text-warning" aria-hidden="true" />
						{m.autolist_cfg_auto()}
					</span>
					<span class="text-xs opacity-70">{m.autolist_cfg_auto_hint()}</span>
					{#if !autoAddAvailable}
						<span class="text-xs text-warning font-medium">{m.autolist_cfg_auto_dependency()}</span>
					{/if}
					<span class="text-xs text-warning font-medium">{m.autolist_cfg_auto_warning()}</span>
				</span>
				<Toggle checked={cfgDraft.autoAdd} controlled onchange={toggleAuto} disabled={!autoAddAvailable} ariaLabel={m.autolist_cfg_auto()} />
			</div>
			</section>
			</div>
		{/if}
	</div>
	{#snippet actions()}
		<Button variant="ghost" size="md" onclick={() => (settingsOpen = false)}>{m.common_cancel()}</Button>
		<Button variant="primary" size="md" onclick={saveConfig} disabled={cfgLoading || busy} loading={busy}>
			{m.common_save()}
		</Button>
	{/snippet}
</Modal>

<!-- Подтверждение включения автоматики -->
<Modal open={autoConfirmOpen} onclose={() => (autoConfirmOpen = false)} title={m.autolist_auto_confirm_title()} size="sm">
	<div class="flex flex-col gap-3">
		<div class="autolist-warning flex items-start gap-2.5 rounded-lg bg-warning/10 border border-warning/30">
			<TriangleAlert size={18} class="text-warning shrink-0 mt-0.5" aria-hidden="true" />
			<p class="text-sm">{m.autolist_auto_confirm_text()}</p>
		</div>
		<p class="text-sm opacity-70">{m.autolist_auto_confirm_limits()}</p>
	</div>
	{#snippet actions()}
		<Button variant="ghost" size="md" onclick={() => (autoConfirmOpen = false)}>{m.common_cancel()}</Button>
		<Button variant="primary" size="md" onclick={confirmAuto}>{m.autolist_auto_confirm_yes()}</Button>
	{/snippet}
</Modal>

<style>
	/* В этой новой вкладке p-3 не попадает в Tailwind CSS; не полагаемся на
	   генерацию utility для критичного визуального отступа предупреждений. */
	.autolist-warning {
		padding: 0.875rem 1rem;
	}
	.autolist-intro {
		flex: 1 1 32rem;
		min-width: min(100%, 24rem);
	}
	.autolist-controls {
		margin-left: auto;
		flex-shrink: 0;
		max-width: 100%;
	}
	.autolist-settings-grid {
		display: grid;
		grid-template-columns: minmax(0, 1fr);
		gap: 1.5rem;
	}
	@media (min-width: 768px) {
		.autolist-settings-grid {
			grid-template-columns: repeat(2, minmax(0, 1fr));
			align-items: start;
		}
	}
</style>
