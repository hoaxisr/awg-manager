<script lang="ts">
	import { m } from '$lib/i18n';
	import { onMount } from 'svelte';
	import { api } from '$lib/api/client';
	import { onResourceInvalidated } from '$lib/stores/storeRegistry';
	import type { UplinkCapabilities, UplinkEntry, UplinkPolicy, UplinkProvider, UplinkSnapshot } from '$lib/types/uplink';
	import StatStrip, { type StatCellData } from '$lib/components/sb-router/StatStrip.svelte';
	import { Badge, Button, ConfirmModal, SideDrawer, StatusDot, Toggle } from '$lib/components/ui';
	import type { BadgeVariant } from '$lib/components/ui/Badge.svelte';
	import type { StatusDotVariant } from '$lib/components/ui/StatusDot.svelte';
	import {
		AlertTriangle,
		ArrowRight,
		ArrowRightLeft,
		ChevronDown,
		Globe,
		Plus,
		RotateCw,
		ShieldCheck,
		SlidersHorizontal,
		Trash2,
	} from 'lucide-svelte';

	interface Props {
		onCountChange?: (count: number) => void;
	}

	let { onCountChange }: Props = $props();

	let snapshot = $state<UplinkSnapshot>({ entries: [], providers: [], automaticFailback: false });
	let targets = $state<{ value: string; label: string; unavailableReason?: string; setupRequired?: boolean }[]>([]);
	let selectedTarget = $state('');
	let draft = $state<UplinkPolicy | null>(null);
	let draftIsNew = $state(false);
	let deleteConfirm = $state<UplinkEntry | null>(null);
	let busy = $state(false);
	let refreshing = $state(false);
	let error = $state('');
	let loaded = $state(false);
	let dnsProvider = $state<UplinkProvider | null>(null);
	let dnsRevision = $state(0);
	let dnsManual = $state(false);
	let dnsText = $state('');
	let dnsError = $state('');
	let dnsRequiredError = $state(false);
	let dnsBusy = $state(false);
	let wanDnsOverrides = $state<Record<string, string[]>>({});
	let wanDnsLoadedRev = 0;
	async function openDNS(provider: UplinkProvider) {
		if (busy || dnsBusy) return;
		dnsBusy = true;
		try {
			const config = await api.uplinkDNS();
			dnsRevision = config.revision;
			wanDnsLoadedRev = Math.max(wanDnsLoadedRev, config.revision);
			wanDnsOverrides = config.overrides || {};
			const servers = wanDnsOverrides[provider.id] || [];
			dnsManual = servers.length > 0;
			dnsText = servers.join(', ');
			dnsError = '';
			dnsRequiredError = false;
			draft = null;
			dnsProvider = provider;
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		} finally {
			dnsBusy = false;
		}
	}
	async function saveDNS() {
		if (!dnsProvider || dnsBusy) return;
		dnsError = '';
		dnsRequiredError = false;
		const servers = dnsManual ? dnsText.trim().split(/[\s,;]+/).filter(Boolean) : [];
		if (dnsManual && (servers.length < 1 || servers.length > 8)) {
			dnsRequiredError = true;
			return;
		}
		dnsBusy = true;
		try {
			const saved = await api.uplinkSaveDNS(dnsProvider.id, dnsRevision, servers);
			if (saved?.overrides) {
				wanDnsLoadedRev = Math.max(wanDnsLoadedRev, saved.revision);
				wanDnsOverrides = saved.overrides;
			}
			dnsProvider = null;
			await refresh();
		} catch (e) {
			dnsError = e instanceof Error ? e.message : String(e);
		} finally {
			dnsBusy = false;
		}
	}

	const phaseLabels: Record<string, () => string> = {
		pending: m.uplink_phase_pending,
		healthy: m.uplink_phase_healthy,
		suspect: m.uplink_phase_suspect,
		paused: m.uplink_phase_paused,
		switching: m.uplink_phase_switching,
		verifying: m.uplink_phase_verifying,
		unavailable: m.uplink_phase_unavailable,
		disabled: m.uplink_phase_disabled,
		stopped: m.uplink_phase_stopped,
		degraded: m.uplink_phase_degraded,
		conflict: m.uplink_phase_conflict,
		'recovery-required': m.uplink_phase_recovery_required,
		unsupported: m.uplink_phase_unsupported,
	};

	const ISSUE_PHASES = new Set(['degraded', 'unavailable', 'conflict', 'recovery-required', 'unsupported']);
	const ACTIVE_CHECK_PHASES = new Set(['pending', 'suspect', 'switching', 'verifying']);

	function phaseLabel(phase: string): string {
		return phaseLabels[phase]?.() || phase;
	}

	function phaseBadgeVariant(phase: string): BadgeVariant {
		if (phase === 'healthy') return 'success';
		if (ISSUE_PHASES.has(phase)) return 'error';
		if (phase === 'suspect') return 'warning';
		if (ACTIVE_CHECK_PHASES.has(phase)) return 'info';
		return 'muted';
	}

	function phaseDotVariant(phase: string): StatusDotVariant {
		if (phase === 'healthy') return 'success';
		if (ISSUE_PHASES.has(phase)) return 'error';
		if (phase === 'suspect') return 'warning';
		if (ACTIVE_CHECK_PHASES.has(phase)) return 'info';
		return 'muted';
	}

	function targetValue(engine: string, ref: string, kind?: 'subscription'): string {
		return JSON.stringify([engine, ref, kind || '']);
	}

	function targetLabel(target: UplinkPolicy['target']): string {
		return targets.find((t) => t.value === targetValue(target.engine, target.ref, target.kind))?.label || target.ref;
	}

	function cleanTargetTitle(target: UplinkPolicy['target']): string {
		const raw = targetLabel(target);
		// Backend appends `" · <engine>"` in RU/EN; strip any known engine suffix cleanly without hardcoded Cyrillic.
		const engineSuffixRe = /\s+·\s+(?:AWGM Route|FIRC|.*sing-box.*|kernel AmneziaWG|native WireGuard|system WireGuard)$/i;
		return raw.replace(engineSuffixRe, '') || raw;
	}

	function engineLabel(target: UplinkPolicy['target']): string {
		if (target.engine === 'sing-box' && target.kind === 'subscription') return m.uplink_engine_singbox_sub();
		switch (target.engine) {
			case 'firc':
				return m.uplink_engine_firc();
			case 'sing-box':
				return m.uplink_engine_singbox();
			case 'kernel-amneziawg':
				return m.uplink_engine_kernel_awg();
			case 'wireguard':
				return m.uplink_engine_wireguard();
			case 'system-wireguard':
				return m.uplink_engine_system_wg();
			default:
				return target.engine;
		}
	}

	function newID(): string {
		return 'uplink-' + Array.from(crypto.getRandomValues(new Uint8Array(16)), (v) => v.toString(16).padStart(2, '0')).join('');
	}

	function providerInfo(id: string): UplinkProvider | undefined {
		return snapshot.providers.find((p) => p.id === id);
	}

	function providerLabel(id: string): string {
		return providerInfo(id)?.label || id;
	}

	function localizeLimitation(raw: string): string {
		const normalized = raw.trim().toLowerCase();
		if (!normalized) return raw;
		if (normalized.includes('awgm route supports manual')) return m.uplink_limitation_firc();
		if (normalized.includes('system wireguard client')) return m.uplink_limitation_system_wireguard();
		if (normalized.includes('native ndms/asc')) return m.uplink_limitation_managed_wireguard();
		if (normalized.includes('pre-provisioned linux kernel wireguard')) return m.uplink_limitation_kernel_wireguard();
		if (normalized.includes('kernel amneziawg')) return m.uplink_limitation_kernel_awg();
		if (normalized.includes('controlled return restarts the engine')) return m.uplink_limitation_singbox_sub();
		if (normalized.includes('independent candidate probe unavailable')) return m.uplink_limitation_independent_probe();
		if (normalized.includes('no uplink adapter registered')) return m.uplink_limitation_no_adapter();
		return raw;
	}

	function capability(policy: UplinkPolicy): UplinkCapabilities | undefined {
		return (
			snapshot.entries.find((e) => e.policy.id === policy.id)?.capabilities ||
			snapshot.capabilities?.[policy.target.engine + (policy.target.kind ? ':' + policy.target.kind : '')]
		);
	}

	function setMode(mode: 'pinned' | 'fallback') {
		if (!draft) return;
		draft.mode = mode;
		if (mode === 'pinned') {
			draft.providers = draft.providers.slice(0, 1);
			draft.failback = 'manual';
		} else if (draft.providers.length < 2) {
			draft.providers.push('');
		}
	}

	async function refresh() {
		snapshot = await api.uplinkSnapshot();
		loaded = true;
		targets = (snapshot.targets || []).map((t) => ({
			value: targetValue(t.target.engine, t.target.ref, t.target.kind),
			label: t.label,
			unavailableReason: t.unavailableReason,
			setupRequired: t.setupRequired,
		}));
		onCountChange?.(snapshot.entries.length);
		void Promise.resolve(api.uplinkDNS?.())
			.then((cfg) => {
				if (cfg?.overrides && cfg.revision >= wanDnsLoadedRev) {
					wanDnsLoadedRev = cfg.revision;
					wanDnsOverrides = cfg.overrides;
				}
			})
			.catch(() => {});
	}

	async function manualRefresh() {
		if (refreshing || busy) return;
		refreshing = true;
		error = '';
		try {
			await refresh();
		} catch (e) {
			error = (e as Error).message;
		} finally {
			refreshing = false;
		}
	}

	onMount(() => {
		let live = true;
		void (async () => {
			try {
				await refresh();
			} catch (e) {
				if (live) error = (e as Error).message;
			}
		})();
		// Снимок перечитываем по подсказке uplink (мутации политик, смена
		// состояния контроллера, смена WAN) вместо 5-секундного таймера.
		const unsub = onResourceInvalidated('uplink', () => {
			refreshPending = true;
		});
		return () => {
			live = false;
			mounted = false;
			unsub();
		};
	});

	// Подсказка, пришедшая во время правки или идущего чтения, не теряется:
	// перечитываем, как только busy снят и прошлое чтение закончилось.
	let refreshPending = $state(false);
	let pendingInFlight = $state(false);
	// Чтение могло завершиться после размонтирования — ошибку тогда не пишем.
	let mounted = $state(true);
	$effect(() => {
		if (!refreshPending || busy || pendingInFlight) return;
		refreshPending = false;
		pendingInFlight = true;
		void refresh()
			.catch((e) => {
				if (mounted) error = (e as Error).message;
			})
			.finally(() => {
				pendingInFlight = false;
			});
	});

	function create() {
		if (!selectedTarget) return;
		const selected = targets.find((t) => t.value === selectedTarget);
		if (selected?.unavailableReason && !selected.setupRequired) return;
		const [engine, ref, kind] = JSON.parse(selectedTarget) as [string, string, '' | 'subscription'];
		const existing = snapshot.entries.find(
			(e) => e.policy.target.engine === engine && e.policy.target.ref === ref && (e.policy.target.kind || '') === kind,
		);
		dnsProvider = null;
		draftIsNew = !existing;
		draft = existing
			? $state.snapshot(existing.policy)
			: {
					id: newID(),
					revision: 0,
					target: { engine, ref, ...(kind ? { kind } : {}) },
					providers: ['', ''],
					enabled: true,
					intervalSeconds: 10,
					timeoutSeconds: 4,
					failureThreshold: 3,
					holdDownSeconds: 60,
					mode: 'fallback',
					failback: 'manual',
					recoveryThreshold: 3,
					recoveryIntervalSeconds: 30,
				};
	}

	function openEdit(entry: UplinkEntry) {
		dnsProvider = null;
		draftIsNew = false;
		draft = $state.snapshot(entry.policy);
	}

	async function act(action: () => Promise<unknown>) {
		if (busy) return;
		busy = true;
		error = '';
		try {
			await action();
			await refresh();
		} catch (e) {
			error = (e as Error).message;
		} finally {
			busy = false;
		}
	}

	async function save() {
		if (!draft) return;
		const policy = $state.snapshot(draft);
		await act(async () => {
			await api.uplinkSave(policy);
			draft = null;
			if (draftIsNew) selectedTarget = '';
		});
	}

	async function confirmDelete() {
		if (!deleteConfirm) return;
		const target = deleteConfirm;
		await act(async () => {
			await api.uplinkDelete(target.policy.id, target.policy.revision);
			deleteConfirm = null;
		});
	}

	let onlineWanCount = $derived(snapshot.providers.filter((p) => p.up).length);
	let activeCount = $derived(snapshot.entries.filter((e) => e.policy.enabled).length);
	let issueCount = $derived(
		snapshot.entries.filter((e) => ISSUE_PHASES.has(e.state.phase) || !!e.state.lastError).length,
	);
	let readyTargets = $derived(targets.filter((t) => (!t.unavailableReason || t.setupRequired)));
	let unavailableTargets = $derived(targets.filter((t) => !(!t.unavailableReason || t.setupRequired)));

	let headerTone = $derived<'success' | 'error' | 'muted'>(
		issueCount > 0 ? 'error' : activeCount > 0 ? 'success' : 'muted',
	);
	let headerStatusText = $derived(
		issueCount > 0
			? m.uplink_status_attention({ count: issueCount })
			: activeCount > 0
				? m.uplink_status_monitoring()
				: m.uplink_status_idle(),
	);

	let statCells = $derived<StatCellData[]>([
		{
			label: m.uplink_stat_policies(),
			value: `${activeCount}/${snapshot.entries.length}`,
			tone: activeCount > 0 && issueCount === 0 ? 'success' : snapshot.entries.length === 0 ? 'muted' : 'default',
			helpTitle: m.uplink_stat_policies_help_title(),
			helpText: m.uplink_stat_policies_help(),
		},
		{
			label: m.uplink_stat_wan(),
			value: `${onlineWanCount}/${snapshot.providers.length}`,
			tone: onlineWanCount > 0 ? 'success' : 'error',
			helpTitle: m.uplink_stat_wan_help_title(),
			helpText: m.uplink_stat_wan_help(),
		},
		{
			label: m.uplink_stat_targets(),
			value: `${readyTargets.length}/${targets.length}`,
			tone: readyTargets.length > 0 ? 'default' : 'muted',
			helpTitle: m.uplink_stat_targets_help_title(),
			helpText: m.uplink_requirements_hint(),
		},
		{
			label: m.uplink_stat_issues(),
			value: String(issueCount),
			tone: issueCount > 0 ? 'error' : 'muted',
			helpTitle: m.uplink_stat_issues_help_title(),
			helpText: m.uplink_stat_issues_help(),
		},
	]);

	const selectTargetId = 'muwan-target-select';
	const drawerModeId = 'muwan-mode-select';
	const drawerFailbackId = 'muwan-failback-select';
</script>

<section class="muwan-shell">
	<header class="muwan-header">
		<div class="muwan-title-block">
			<div class="muwan-title-row">
				<h2 class="muwan-title">{m.uplink_title()}</h2>
				<span
					class="muwan-status-pill"
					class:is-running={headerTone === 'success'}
					class:is-error={headerTone === 'error'}
				>
					<StatusDot variant={headerTone} size="sm" pulse={headerTone === 'success'} />
					<span>{headerStatusText}</span>
				</span>
			</div>
			<p class="muwan-subtitle">{m.uplink_intro()}</p>
		</div>

		<div class="muwan-header-tools">
			<Button variant="secondary" size="md" loading={refreshing} disabled={busy} onclick={manualRefresh}>
				{#snippet iconBefore()}
					<RotateCw size={14} aria-hidden="true" />
				{/snippet}
				{m.uplink_refresh()}
			</Button>
		</div>
	</header>

	{#if error}
		<div role="alert" class="muwan-banner is-error">
			<AlertTriangle size={16} aria-hidden="true" />
			<span>{error}</span>
		</div>
	{/if}

	{#if snapshot.targetError}
		<div role="status" class="muwan-banner is-warning">
			<AlertTriangle size={16} aria-hidden="true" />
			<span>{m.uplink_target_error({ error: snapshot.targetError })}</span>
		</div>
	{/if}

	{#if loaded}
		<StatStrip cells={statCells} />

		{#if snapshot.providers.length > 0}
			<div class="muwan-wan-panel">
				<div class="muwan-wan-head">
					<Globe size={14} aria-hidden="true" />
					<span>{m.uplink_wan_section()}</span>
				</div>
				<div class="muwan-wan-list">
					{#each snapshot.providers as p (p.id)}
						<div class="muwan-wan-chip" class:is-up={p.up}>
							<StatusDot variant={p.up ? 'success' : 'muted'} size="sm" pulse={p.up} />
							<span class="muwan-wan-name">{p.label || p.id}</span>
							{#if p.device}
								<code class="muwan-dev-pill">{p.device}</code>
							{/if}
							<Badge variant={p.up ? 'success' : 'muted'} size="xs">
								{p.up ? m.uplink_wan_online() : m.uplink_wan_offline()}
							</Badge>
							{#if wanDnsOverrides[p.id]?.length}
								<code class="muwan-dev-pill is-dns" title={m.uplink_dns_settings()}>
									{wanDnsOverrides[p.id].join(', ')}
								</code>
							{/if}
							<Button size="sm" variant="secondary" disabled={busy || dnsBusy} ariaLabel={m.uplink_dns_settings() + ': ' + (p.label || p.id)} onclick={() => openDNS(p)}>DNS</Button>
						</div>
					{/each}
				</div>
			</div>
		{/if}

		<div class="muwan-toolbar">
			<div class="muwan-toolbar-row">
				<label class="muwan-toolbar-field" for={selectTargetId}>
					<span class="muwan-field-label">{m.uplink_tunnel_label()}</span>
					<select id={selectTargetId} class="muwan-select" bind:value={selectedTarget} disabled={busy}>
						<option value="">{m.uplink_select_tunnel()}</option>
						{#each targets as target (target.value)}
							<option value={target.value} disabled={!!target.unavailableReason && !target.setupRequired}>
								{target.label}{target.unavailableReason ? ` — ${target.unavailableReason}` : ''}
							</option>
						{/each}
					</select>
				</label>
				<Button variant="primary" size="md" onclick={create} disabled={busy || !selectedTarget}>
					{#snippet iconBefore()}
						<Plus size={15} aria-hidden="true" />
					{/snippet}
					{m.uplink_configure()}
				</Button>
			</div>

			{#if unavailableTargets.length > 0}
				<details class="muwan-unavailable-box">
					<summary>{m.uplink_unavailable_targets_title({ count: unavailableTargets.length })}</summary>
					<ul class="muwan-unavailable-list">
						{#each unavailableTargets as t (t.value)}
							<li>
								<strong>{t.label}</strong>
								<span>{t.unavailableReason}</span>
							</li>
						{/each}
					</ul>
				</details>
			{/if}
		</div>

		{#if snapshot.entries.length === 0}
			<div class="muwan-empty-card">
				<div class="muwan-empty-icon">
					<ArrowRightLeft size={22} aria-hidden="true" />
				</div>
				<h3 class="muwan-empty-title">{m.uplink_empty_title()}</h3>
				<p class="muwan-empty-desc">{m.uplink_empty_desc()}</p>
			</div>
		{:else}
			<div class="muwan-section-head">
				<span>{m.uplink_policies_section({ count: snapshot.entries.length })}</span>
			</div>

			<div class="muwan-grid">
				{#each snapshot.entries as entry (entry.policy.id)}
					{@const isPinned = entry.policy.mode === 'pinned'}
					{@const cap = capability(entry.policy)}
					<article class="muwan-card" class:is-disabled={!entry.policy.enabled}>
						<header class="muwan-card-head">
							<div class="muwan-card-title-wrap">
								<div class="muwan-card-badges">
									<Badge variant="accent" size="xs">{engineLabel(entry.policy.target)}</Badge>
									<Badge variant="muted" size="xs">
										{isPinned ? m.uplink_mode_badge_pinned() : m.uplink_mode_badge_fallback()}
									</Badge>
								</div>
								<h3 class="muwan-card-title">{cleanTargetTitle(entry.policy.target)}</h3>
							</div>

							<Badge variant={phaseBadgeVariant(entry.state.phase)} size="sm" pill>
								<StatusDot
									variant={phaseDotVariant(entry.state.phase)}
									size="sm"
									pulse={entry.state.phase === 'healthy'}
								/>
								<span>
									{phaseLabel(entry.state.phase)}{entry.state.provider ? ` · ${providerLabel(entry.state.provider)}` : ''}
								</span>
							</Badge>
						</header>

						<div class="muwan-chain" aria-label={m.uplink_priority({ list: entry.policy.providers.map(providerLabel).join(' → ') })}>
							{#each entry.policy.providers as id, idx (`${entry.policy.id}:${idx}:${id}`)}
								{@const info = providerInfo(id)}
								{@const isActive = entry.state.provider === id}
								{#if idx > 0}
									<span class="muwan-hop-arrow" aria-hidden="true">
										<ArrowRight size={13} />
									</span>
								{/if}
								<div class="muwan-hop" class:is-active={isActive} class:is-offline={!info?.up}>
									<span class="muwan-hop-rank">#{idx + 1}</span>
									<StatusDot variant={isActive ? 'success' : info?.up ? 'info' : 'muted'} size="sm" />
									<span class="muwan-hop-name">{providerLabel(id)}</span>
									{#if info?.device}
										<code class="muwan-dev-pill">{info.device}</code>
									{/if}
									{#if isActive}
										<Badge variant="success" size="xs">{m.uplink_active_provider_badge()}</Badge>
									{/if}
								</div>
							{/each}
						</div>

						<div class="muwan-meta">
							<span>
								{entry.policy.failback === 'automatic' && cap?.automaticFailback
									? m.uplink_failback_automatic()
									: entry.policy.failback === 'controlled' ? m.uplink_failback_controlled() : m.uplink_failback_manual()}
							</span>
							<span class="muwan-meta-dot" aria-hidden="true">·</span>
							<span>
								{m.uplink_cadence_summary({
									interval: entry.policy.intervalSeconds,
									timeout: entry.policy.timeoutSeconds,
									failures: entry.policy.failureThreshold,
								})}
							</span>
						</div>

						{#if entry.policy.target.engine === 'kernel-amneziawg'}
							<p class="muwan-note">{m.uplink_kernel_remove_note()}</p>
						{/if}

						{#if entry.state.candidate}
							<p class="muwan-note is-info">
								{m.uplink_checking_failback({
									provider: providerLabel(entry.state.candidate),
									count: entry.state.recoverySuccesses || 0,
								})}
							</p>
						{/if}

						{#if entry.state.lastError}
							<p role="status" class="muwan-note is-error">{entry.state.lastError}</p>
						{/if}

						<footer class="muwan-card-foot">
							<div class="muwan-foot-switches">
								{#each entry.policy.providers as id (id)}
									{@const p = providerInfo(id)}
									{@const isAlreadyActiveHealthy = entry.state.provider === id && entry.state.phase === 'healthy'}
									<Button
										variant="secondary"
										size="sm"
										disabled={busy || !entry.policy.enabled || !p?.up || isAlreadyActiveHealthy}
										onclick={() => act(() => api.uplinkSwitch(entry.policy.id, id, entry.policy.revision))}
									>
										{m.uplink_switch_to({ provider: providerLabel(id) })}
									</Button>
								{/each}
							</div>

							<div class="muwan-foot-actions">
								<Button variant="secondary" size="sm" disabled={busy} onclick={() => openEdit(entry)}>
									{#snippet iconBefore()}
										<SlidersHorizontal size={13} aria-hidden="true" />
									{/snippet}
									{m.uplink_edit()}
								</Button>
								<Button
									variant="outline-danger"
									size="sm"
									disabled={busy}
									onclick={() => {
										deleteConfirm = entry;
									}}
								>
									{#snippet iconBefore()}
										<Trash2 size={13} aria-hidden="true" />
									{/snippet}
									{m.uplink_remove_policy()}
								</Button>
							</div>
						</footer>
					</article>
				{/each}
			</div>
		{/if}
	{:else}
		<div class="muwan-loading-card">
			<p class="muwan-subtitle">{m.uplink_loading()}</p>
		</div>
	{/if}
</section>

<SideDrawer
	open={draft !== null}
	title={draft ? (draftIsNew ? m.uplink_drawer_create_title() : m.uplink_drawer_edit_title({ target: cleanTargetTitle(draft.target) })) : ''}
	width={500}
	onClose={() => {
		if (!busy) draft = null;
	}}
>
	{#if draft}
		{@const draftCap = capability(draft)}
		<form
			class="muwan-drawer-form"
			onsubmit={(event) => {
				event.preventDefault();
				void save();
			}}
		>
			<div class="muwan-drawer-body">
				<div class="muwan-drawer-target-card">
					<div class="muwan-card-badges" aria-label={m.uplink_capabilities_title()}>
						<Badge variant="accent" size="xs">{engineLabel(draft.target)}</Badge>
						{#if draftCap}
							<Badge variant={draftCap.automaticFailback || draftCap.controlledFailback ? 'success' : 'muted'} size="xs">
								{draftCap.automaticFailback ? m.uplink_cap_auto_failback() : draftCap.controlledFailback ? m.uplink_failback_controlled() : m.uplink_cap_manual_failback()}
							</Badge>
							<Badge variant={draftCap.restartScope === 'engine' ? 'warning' : 'default'} size="xs">
								{draftCap.restartScope === 'engine' ? m.uplink_cap_restart_engine() : m.uplink_cap_restart_target()}
							</Badge>
						{/if}
					</div>
					<h3 class="muwan-drawer-target-name">{m.uplink_providers_for({ target: targetLabel(draft.target) })}</h3>
					{#if draftCap?.limitation}
						<p class="muwan-note">{localizeLimitation(draftCap.limitation)}</p>
					{/if}
				</div>

				{#if draft.target.engine === 'kernel-amneziawg'}
					<div class="muwan-banner is-info">
						<ShieldCheck size={16} aria-hidden="true" />
						<span>{m.uplink_kernel_disable_note()}</span>
					</div>
				{/if}

				<div class="muwan-toggle-card">
					<Toggle
						bind:checked={draft.enabled}
						onchange={(next) => {
							if (draft) draft.enabled = next;
						}}
						disabled={busy}
						label={m.uplink_manage_switching()}
						hint={m.uplink_manage_switching_hint()}
					/>
				</div>

				<div class="muwan-drawer-section">
					<label class="muwan-field" for={drawerModeId}>
						<span class="muwan-field-label">{m.uplink_mode_label()}</span>
						<select
							id={drawerModeId}
							class="muwan-select"
							value={draft.mode || 'fallback'}
							disabled={busy}
							onchange={(event) => setMode(event.currentTarget.value as 'pinned' | 'fallback')}
						>
							<option value="fallback">{m.uplink_mode_fallback()}</option>
							<option value="pinned">{m.uplink_mode_pinned()}</option>
						</select>
					</label>

					{#each draft.providers as provider, i}
						{@const selectedInfo = provider ? providerInfo(provider) : undefined}
						<label class="muwan-field">
							<span class="muwan-field-label-row">
								<span class="muwan-field-label">
									{i === 0 ? m.uplink_primary_provider() : m.uplink_backup_provider({ index: i })}
								</span>
								{#if selectedInfo}
									<StatusDot variant={selectedInfo.up ? 'success' : 'muted'} size="sm" />
								{/if}
							</span>
							<select class="muwan-select" bind:value={draft.providers[i]} required disabled={busy}>
								<option value="">{m.uplink_select_provider()}</option>
								{#if provider && !snapshot.providers.some((p) => p.id === provider)}
									<option value={provider}>{provider}{m.uplink_provider_missing_suffix()}</option>
								{/if}
								{#each snapshot.providers as p (p.id)}
									<option value={p.id}>{p.label || p.id}{p.up ? '' : m.uplink_provider_down_suffix()}</option>
								{/each}
							</select>
						</label>
					{/each}

					{#if draft.mode !== 'pinned'}
						<div class="muwan-inline-actions">
							<Button
								variant="secondary"
								size="sm"
								disabled={busy || draft.providers.length >= 8}
								onclick={() => {
									if (draft) draft.providers.push('');
								}}
							>
								{#snippet iconBefore()}
									<Plus size={13} aria-hidden="true" />
								{/snippet}
								{m.uplink_add_backup()}
							</Button>
							<Button
								variant="secondary"
								size="sm"
								disabled={busy || draft.providers.length <= 2}
								onclick={() => {
									if (draft) draft.providers.pop();
								}}
							>
								{m.uplink_remove_backup()}
							</Button>
						</div>
					{/if}
				</div>

				<div class="muwan-drawer-section">
					<label class="muwan-field" for={drawerFailbackId}>
						<span class="muwan-field-label">{m.uplink_failback_label()}</span>
						<select
							id={drawerFailbackId}
							class="muwan-select"
							value={draft.failback || 'manual'}
							disabled={busy || draft.mode === 'pinned'}
							onchange={(event) => {
								if (draft) draft.failback = event.currentTarget.value as 'manual' | 'automatic' | 'controlled';
							}}
						>
							<option value="manual">{m.uplink_failback_manual()}</option>
							<option value="automatic" disabled={!draftCap?.automaticFailback}>
								{m.uplink_failback_automatic()}
							</option>
							{#if draftCap?.controlledFailback}<option value="controlled">{m.uplink_failback_controlled()}</option>{/if}
						</select>
					</label>
					{#if draft.failback === 'controlled'}<p class="muwan-hint">{m.uplink_controlled_failback_hint()}</p>{/if}

					{#if !draftCap?.automaticFailback && !draftCap?.controlledFailback}
						<p class="muwan-hint">{m.uplink_no_auto_failback_hint()}</p>
					{/if}

					{#if draft.failback === 'automatic' || draft.failback === 'controlled'}
						<div class="muwan-timer-grid">
							<label class="muwan-field">
								<span class="muwan-field-label">{m.uplink_recovery_threshold()}</span>
								<input class="muwan-input" type="number" min="1" max="10" bind:value={draft.recoveryThreshold} required disabled={busy} />
							</label>
							<label class="muwan-field">
								<span class="muwan-field-label">{m.uplink_recovery_interval()}</span>
								<input class="muwan-input" type="number" min="1" max="3600" bind:value={draft.recoveryIntervalSeconds} required disabled={busy} />
							</label>
						</div>
					{/if}
				</div>

				<details class="muwan-advanced">
					<summary class="muwan-advanced-summary">
						<span>{m.uplink_advanced_timers()}</span>
						<ChevronDown size={15} aria-hidden="true" />
					</summary>
					<div class="muwan-timer-grid">
						<label class="muwan-field">
							<span class="muwan-field-label">{m.uplink_check_interval()}</span>
							<input class="muwan-input" type="number" min="1" max="3600" bind:value={draft.intervalSeconds} required disabled={busy} />
						</label>
						<label class="muwan-field">
							<span class="muwan-field-label">{m.uplink_timeout()}</span>
							<input class="muwan-input" type="number" min="1" max="30" bind:value={draft.timeoutSeconds} required disabled={busy} />
						</label>
						<label class="muwan-field">
							<span class="muwan-field-label">{m.uplink_failure_threshold()}</span>
							<input class="muwan-input" type="number" min="1" max="10" bind:value={draft.failureThreshold} required disabled={busy} />
						</label>
						<label class="muwan-field">
							<span class="muwan-field-label">{m.uplink_hold_down()}</span>
							<input class="muwan-input" type="number" min="10" max="3600" bind:value={draft.holdDownSeconds} required disabled={busy} />
						</label>
					</div>
				</details>

				<p class="muwan-hint">{m.uplink_keep_last_binding_hint()}</p>
			</div>

			<div class="muwan-drawer-footer">
				<Button
					variant="ghost"
					size="md"
					disabled={busy}
					onclick={() => {
						draft = null;
					}}
				>
					{m.uplink_cancel()}
				</Button>
				<Button variant="primary" size="md" type="submit" loading={busy} disabled={busy}>
					{m.uplink_save()}
				</Button>
			</div>
		</form>
	{/if}
</SideDrawer>

<SideDrawer
	open={dnsProvider !== null}
	title={m.uplink_dns_settings() + (dnsProvider ? ' · ' + (dnsProvider.label || dnsProvider.id) : '')}
	width={500}
	onClose={() => {
		if (!dnsBusy) dnsProvider = null;
	}}
>
	{#if dnsProvider}
		<form
			class="muwan-drawer-form"
			onsubmit={(event) => {
				event.preventDefault();
				void saveDNS();
			}}
		>
			<div class="muwan-drawer-body">
				<div class="muwan-drawer-target-card">
					<div class="muwan-card-badges">
						<StatusDot variant={dnsProvider.up ? 'success' : 'muted'} size="sm" pulse={dnsProvider.up} />
						{#if dnsProvider.device}
							<code class="muwan-dev-pill">{dnsProvider.device}</code>
						{/if}
						<Badge variant={dnsProvider.up ? 'success' : 'muted'} size="xs">
							{dnsProvider.up ? m.uplink_wan_online() : m.uplink_wan_offline()}
						</Badge>
					</div>
					<p class="muwan-hint">{m.uplink_dns_hint()}</p>
				</div>

				<div class="muwan-drawer-section">
					<div class="muwan-field">
						<label class="muwan-field-label" for="uplink-dns-mode">{m.uplink_dns_mode()}</label>
						<select id="uplink-dns-mode" class="muwan-select" bind:value={dnsManual} disabled={dnsBusy}>
							<option value={false}>{m.uplink_dns_auto()}</option>
							<option value={true}>{m.uplink_dns_manual()}</option>
						</select>
					</div>

					{#if dnsManual}
						<div class="muwan-field">
							<label class="muwan-field-label" for="uplink-dns-servers">{m.uplink_dns_servers()}</label>
							<input
								id="uplink-dns-servers"
								class="muwan-input"
								bind:value={dnsText}
								placeholder="1.1.1.1, 8.8.8.8"
								required
								disabled={dnsBusy}
							/>
							<p class="muwan-hint">{m.uplink_dns_manual_hint()}</p>
						</div>
					{:else}
						<p class="muwan-hint">{m.uplink_dns_auto_hint()}</p>
					{/if}
				</div>

				{#if dnsRequiredError || dnsError}
					<p role="alert" class="muwan-note is-error">
						{dnsRequiredError ? m.uplink_dns_required() : dnsError}
					</p>
				{/if}
			</div>

			<div class="muwan-drawer-footer">
				<Button
					variant="ghost"
					size="md"
					disabled={dnsBusy}
					onclick={() => {
						dnsProvider = null;
					}}
				>
					{m.uplink_cancel()}
				</Button>
				<Button variant="primary" size="md" type="submit" loading={dnsBusy} disabled={dnsBusy}>
					{m.common_save()}
				</Button>
			</div>
		</form>
	{/if}
</SideDrawer>

<ConfirmModal
	open={deleteConfirm !== null}
	title={m.uplink_delete_confirm_title()}
	message={deleteConfirm ? m.uplink_delete_confirm_message({ target: targetLabel(deleteConfirm.policy.target) }) : ''}
	secondary={deleteConfirm?.policy.target.engine === 'kernel-amneziawg' ? m.uplink_kernel_remove_note() : undefined}
	confirmLabel={m.uplink_remove_policy()}
	variant="danger"
	{busy}
	onConfirm={confirmDelete}
	onClose={() => {
		if (!busy) deleteConfirm = null;
	}}
/>

<style>
	.muwan-shell {
		display: flex;
		flex-direction: column;
		gap: 0.875rem;
		width: 100%;
		min-width: 0;
		margin-top: 0.25rem;
	}

	.muwan-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 1rem;
		padding-bottom: 0.75rem;
		border-bottom: 1px solid var(--border);
		flex-wrap: wrap;
	}

	.muwan-title-block {
		flex: 1 1 auto;
		min-width: 0;
	}

	.muwan-title-row {
		display: flex;
		align-items: center;
		gap: 0.625rem;
		flex-wrap: wrap;
	}

	.muwan-title {
		margin: 0;
		font-size: var(--fs-h3, 1.125rem);
		font-weight: 600;
		color: var(--text-primary);
		line-height: 1.25;
	}

	.muwan-status-pill {
		display: inline-flex;
		align-items: center;
		gap: 0.375rem;
		padding: 0.15rem 0.55rem;
		border-radius: 999px;
		font-family: var(--font-mono);
		font-size: 0.72rem;
		background: var(--bg-secondary);
		border: 1px solid var(--border);
		color: var(--text-muted);
	}

	.muwan-status-pill.is-running {
		color: var(--success);
		border-color: var(--color-success-border);
		background: var(--color-success-tint);
	}

	.muwan-status-pill.is-error {
		color: var(--error);
		border-color: var(--color-error-border);
		background: var(--color-error-tint);
	}

	.muwan-subtitle {
		margin: 4px 0 0;
		font-size: var(--fs-sm, 0.8125rem);
		color: var(--text-muted);
	}

	.muwan-header-tools {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		flex-shrink: 0;
	}

	.muwan-banner {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		padding: 0.65rem 0.9rem;
		border-radius: var(--radius-sm);
		font-size: 0.8125rem;
		border: 1px solid var(--border);
	}

	.muwan-banner.is-error {
		color: var(--error);
		background: var(--color-error-tint);
		border-color: var(--color-error-border);
	}

	.muwan-banner.is-warning {
		color: var(--warning);
		background: var(--color-warning-tint);
		border-color: var(--color-warning-border);
	}

	.muwan-banner.is-info {
		color: var(--info);
		background: var(--color-info-tint);
		border-color: var(--color-info-border);
	}

	.muwan-wan-panel,
	.muwan-toolbar,
	.muwan-empty-card,
	.muwan-loading-card,
	.muwan-card {
		background: var(--bg-secondary);
		border: 1px solid var(--border);
		border-radius: var(--radius);
		box-shadow: var(--shadow);
	}

	.muwan-wan-panel {
		padding: 0.75rem 1rem;
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
	}

	.muwan-wan-head {
		display: flex;
		align-items: center;
		gap: 0.4rem;
		font-size: 0.75rem;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		color: var(--text-muted);
	}

	.muwan-wan-list {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
	}

	.muwan-wan-chip {
		display: inline-flex;
		align-items: center;
		gap: 0.45rem;
		padding: 0.375rem 0.625rem;
		border-radius: var(--radius-sm);
		background: var(--bg-tertiary);
		border: 1px solid var(--border);
		font-size: 0.8125rem;
		color: var(--text-secondary);
	}

	.muwan-wan-chip.is-up {
		border-color: var(--color-success-border);
		color: var(--text-primary);
	}

	.muwan-wan-name {
		font-weight: 500;
	}

	.muwan-dev-pill {
		font-family: var(--font-mono);
		font-size: 0.72rem;
		padding: 0.08rem 0.35rem;
		border-radius: 4px;
		background: var(--bg-secondary);
		border: 1px solid var(--border);
		color: var(--text-muted);
	}

	.muwan-dev-pill.is-dns {
		border-color: var(--accent);
		color: var(--text-primary);
	}

	.muwan-toolbar {
		padding: 0.875rem 1rem;
		display: flex;
		flex-direction: column;
		gap: 0.625rem;
	}

	.muwan-toolbar-row {
		display: flex;
		align-items: flex-end;
		gap: 0.75rem;
		flex-wrap: wrap;
	}

	.muwan-toolbar-field {
		flex: 1 1 280px;
		min-width: 0;
		display: flex;
		flex-direction: column;
		gap: 0.35rem;
	}

	.muwan-field {
		display: flex;
		flex-direction: column;
		gap: 0.35rem;
		min-width: 0;
	}

	.muwan-field-label-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.5rem;
	}

	.muwan-field-label {
		font-size: 0.8125rem;
		font-weight: 500;
		color: var(--text-secondary);
	}

	.muwan-select,
	.muwan-input {
		width: 100%;
		min-width: 0;
		box-sizing: border-box;
		padding: 0.45rem 0.65rem;
		border-radius: var(--radius-sm);
		border: 1px solid var(--border);
		background: var(--bg-tertiary);
		color: var(--text-primary);
		font: inherit;
		font-size: 0.875rem;
		line-height: 1.4;
		transition: border-color var(--t-fast) ease;
	}

	.muwan-select:focus,
	.muwan-input:focus {
		outline: none;
		border-color: var(--accent);
	}

	.muwan-select:disabled,
	.muwan-input:disabled {
		opacity: 0.6;
		cursor: not-allowed;
	}

	.muwan-unavailable-box {
		font-size: 0.8125rem;
		color: var(--text-muted);
	}

	.muwan-unavailable-box summary {
		cursor: pointer;
		user-select: none;
		color: var(--text-secondary);
		font-weight: 500;
	}

	.muwan-unavailable-list {
		margin: 0.5rem 0 0;
		padding: 0.5rem 0.75rem;
		list-style: none;
		border-radius: var(--radius-sm);
		background: var(--bg-tertiary);
		border: 1px solid var(--border);
		display: grid;
		gap: 0.4rem;
	}

	.muwan-unavailable-list li {
		display: flex;
		flex-wrap: wrap;
		gap: 0.35rem 0.6rem;
		align-items: baseline;
	}

	.muwan-unavailable-list strong {
		color: var(--text-primary);
		font-weight: 500;
	}

	.muwan-empty-card,
	.muwan-loading-card {
		padding: 2.25rem 1.25rem;
		text-align: center;
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 0.5rem;
	}

	.muwan-empty-icon {
		width: 44px;
		height: 44px;
		border-radius: 10px;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		background: var(--bg-tertiary);
		border: 1px solid var(--border);
		color: var(--accent);
		margin-bottom: 0.25rem;
	}

	.muwan-empty-title {
		margin: 0;
		font-size: 1rem;
		font-weight: 600;
		color: var(--text-primary);
	}

	.muwan-empty-desc {
		margin: 0;
		max-width: 56ch;
		font-size: 0.875rem;
		color: var(--text-muted);
		line-height: 1.45;
	}

	.muwan-section-head {
		font-size: 0.75rem;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		color: var(--text-muted);
	}

	.muwan-grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(min(440px, 100%), 1fr));
		gap: 0.875rem;
	}

	.muwan-card {
		padding: 1rem;
		display: flex;
		flex-direction: column;
		gap: 0.75rem;
		min-width: 0;
	}

	.muwan-card.is-disabled {
		opacity: 0.82;
	}

	.muwan-card-head {
		display: flex;
		align-items: flex-start;
		justify-content: space-between;
		gap: 0.75rem;
		flex-wrap: wrap;
	}

	.muwan-card-title-wrap {
		display: flex;
		flex-direction: column;
		gap: 0.35rem;
		min-width: 0;
		flex: 1 1 200px;
	}

	.muwan-card-badges {
		display: flex;
		flex-wrap: wrap;
		gap: 0.35rem;
	}

	.muwan-card-title {
		margin: 0;
		font-size: 1rem;
		font-weight: 600;
		color: var(--text-primary);
		word-break: break-word;
	}

	.muwan-chain {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.4rem;
		padding: 0.6rem 0.75rem;
		border-radius: var(--radius-sm);
		background: var(--bg-tertiary);
		border: 1px solid var(--border);
	}

	.muwan-hop {
		display: inline-flex;
		align-items: center;
		gap: 0.375rem;
		padding: 0.25rem 0.5rem;
		border-radius: var(--radius-sm);
		background: var(--bg-secondary);
		border: 1px solid var(--border);
		font-size: 0.8125rem;
		color: var(--text-secondary);
	}

	.muwan-hop.is-active {
		border-color: var(--color-success-border);
		color: var(--text-primary);
	}

	.muwan-hop.is-offline {
		opacity: 0.7;
	}

	.muwan-hop-rank {
		font-family: var(--font-mono);
		font-size: 0.72rem;
		color: var(--text-muted);
	}

	.muwan-hop-name {
		font-weight: 500;
	}

	.muwan-hop-arrow {
		color: var(--text-muted);
		display: inline-flex;
		align-items: center;
	}

	.muwan-meta {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.35rem;
		font-size: 0.8125rem;
		color: var(--text-muted);
	}

	.muwan-meta-dot {
		opacity: 0.6;
	}

	.muwan-note {
		margin: 0;
		font-size: 0.8125rem;
		color: var(--text-muted);
		line-height: 1.4;
	}

	.muwan-note.is-info {
		color: var(--info);
		background: var(--color-info-tint);
		border: 1px solid var(--color-info-border);
		border-radius: var(--radius-sm);
		padding: 0.45rem 0.65rem;
	}

	.muwan-note.is-error {
		color: var(--error);
		background: var(--color-error-tint);
		border: 1px solid var(--color-error-border);
		border-radius: var(--radius-sm);
		padding: 0.45rem 0.65rem;
	}

	.muwan-card-foot {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.625rem;
		flex-wrap: wrap;
		padding-top: 0.625rem;
		border-top: 1px solid var(--border);
		margin-top: auto;
	}

	.muwan-foot-switches,
	.muwan-foot-actions,
	.muwan-inline-actions {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
	}

	.muwan-drawer-form {
		display: flex;
		flex-direction: column;
		gap: 1rem;
		min-height: 100%;
	}

	.muwan-drawer-body {
		display: flex;
		flex-direction: column;
		gap: 0.875rem;
		flex: 1 1 auto;
	}

	.muwan-drawer-target-card,
	.muwan-drawer-section,
	.muwan-toggle-card,
	.muwan-advanced {
		background: var(--bg-tertiary);
		border: 1px solid var(--border);
		border-radius: var(--radius-sm);
		padding: 0.75rem 0.875rem;
	}

	.muwan-drawer-target-card,
	.muwan-drawer-section {
		display: flex;
		flex-direction: column;
		gap: 0.65rem;
	}

	.muwan-drawer-target-name {
		margin: 0;
		font-size: 0.9375rem;
		font-weight: 600;
		color: var(--text-primary);
	}

	.muwan-advanced-summary {
		cursor: pointer;
		user-select: none;
		display: flex;
		align-items: center;
		justify-content: space-between;
		font-size: 0.8125rem;
		font-weight: 600;
		color: var(--text-secondary);
		list-style: none;
	}

	.muwan-advanced[open] .muwan-advanced-summary {
		margin-bottom: 0.75rem;
	}

	.muwan-timer-grid {
		display: grid;
		grid-template-columns: repeat(2, minmax(0, 1fr));
		gap: 0.75rem;
	}

	.muwan-hint {
		margin: 0;
		font-size: 0.8125rem;
		color: var(--text-muted);
		line-height: 1.4;
	}

	.muwan-drawer-footer {
		position: sticky;
		bottom: -1rem;
		display: flex;
		justify-content: flex-end;
		gap: 0.5rem;
		padding: 0.75rem 0 0.25rem;
		margin-top: auto;
		background: var(--bg-secondary);
		border-top: 1px solid var(--border);
	}

	@media (max-width: 600px) {
		.muwan-timer-grid {
			grid-template-columns: minmax(0, 1fr);
		}
	}
</style>
