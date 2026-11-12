<script lang="ts">
	import { onDestroy, onMount } from 'svelte';
	import {
		AlertTriangle,
		KeyRound,
		RefreshCcw,
		Trash2,
		UserPlus,
		ChevronDown,
		Route,
		Shield,
		Check,
	} from 'lucide-svelte';
	import { api } from '$lib/api/client';
	import { m } from '$lib/i18n';
	import { Button, Card, ConfirmModal, Dropdown } from '$lib/components/ui';
	import RichText from '$lib/components/ui/RichText.svelte';
	import type { DropdownOption } from '$lib/components/ui';
	import { notifications } from '$lib/stores/notifications';
	import type { WarpConfig, WarpRouteRef, WarpScoutState, WarpTunnelRef } from '$lib/types/warpscout';
	import WarpScannerCard from './WarpScannerCard.svelte';
	import WarpAutopilotCard from './WarpAutopilotCard.svelte';
	import {
		SIGNATURE_PROFILES,
		cloneConfig,
		formatTime,
		shortId,
		warpErrorMessage,
	} from './warpscoutUi';

	const FAST_POLL_MS = 1000;
	const SLOW_POLL_MS = 5000;

	let ws = $state<WarpScoutState | null>(null);
	let loadErrObj = $state<unknown>(null);
	let targets = $state<WarpTunnelRef[]>([]);

	const loadError = $derived(loadErrObj ? warpErrorMessage(loadErrObj) : '');

	let openPop = $state<'acct' | 'route' | 'awg' | null>(null);

	// Черновик конфигурации (профили + параметры AWG + маршрут сканера).
	// Пока пользователь его не трогал — синхронизируется с сервером на каждом опросе.
	let draft = $state<WarpConfig>({
		profileMode: 'single',
		scanInterface: '',
		profiles: [],
		awg: { signatureProfile: 'quic_initial', jc: 4, jmin: 40, jmax: 70 },
	});
	let draftLoaded = false;
	let serverConfigJson = $state('');
	let saving = $state(false);

	const dirty = $derived(serverConfigJson !== '' && JSON.stringify(draft) !== serverConfigJson);

	let destroyed = false;
	let pollTimer: ReturnType<typeof setTimeout> | null = null;

	function applyState(next: WarpScoutState): void {
		const incoming = JSON.stringify(next.config);
		if (!draftLoaded || !dirty) {
			draft = cloneConfig(next.config);
			draftLoaded = true;
		}
		serverConfigJson = incoming;
		ws = next;
		loadErrObj = null;
	}

	async function loadState(): Promise<void> {
		try {
			applyState(await api.warpscoutState());
		} catch (e) {
			if (e instanceof DOMException && e.name === 'AbortError') return;
			loadErrObj = e;
		}
	}

	async function loadTargets(): Promise<void> {
		try {
			targets = await api.warpscoutTargets();
		} catch (e) {
			if (e instanceof DOMException && e.name === 'AbortError') return;
			notifications.error(m.warpscout_targets_err({ err: warpErrorMessage(e) }));
		}
	}

	const fastPoll = $derived(
		!!ws && (ws.scan.running || (ws.watch ?? []).some((w) => w.state === 'swapping')),
	);

	function schedule(): void {
		if (destroyed) return;
		if (pollTimer) clearTimeout(pollTimer);
		pollTimer = setTimeout(tick, fastPoll ? FAST_POLL_MS : SLOW_POLL_MS);
	}

	async function tick(): Promise<void> {
		pollTimer = null;
		if (destroyed) return;
		if (typeof document === 'undefined' || document.visibilityState !== 'hidden') {
			await loadState();
		}
		schedule();
	}

	async function refreshNow(): Promise<void> {
		await loadState();
		schedule();
	}

	function handleWindowPointerDown(e: PointerEvent): void {
		if (!openPop) return;
		const target = e.target as HTMLElement | null;
		if (!target) return;
		// Не закрываем поповер при клике в портал выпадающего списка (<Dropdown>)
		if (target.closest('.pop-anchor') || target.closest('[role="listbox"], .dropdown-panel')) return;
		openPop = null;
	}

	function handleWindowKeyDown(e: KeyboardEvent): void {
		if (e.key === 'Escape' && openPop) {
			openPop = null;
		}
	}

	onMount(() => {
		window.addEventListener('pointerdown', handleWindowPointerDown);
		window.addEventListener('keydown', handleWindowKeyDown);
		void (async () => {
			await Promise.all([loadState(), loadTargets()]);
			schedule();
		})();
	});

	onDestroy(() => {
		destroyed = true;
		if (typeof window !== 'undefined') {
			window.removeEventListener('pointerdown', handleWindowPointerDown);
			window.removeEventListener('keydown', handleWindowKeyDown);
		}
		if (pollTimer) clearTimeout(pollTimer);
		pollTimer = null;
	});

	function togglePop(which: 'acct' | 'route' | 'awg'): void {
		openPop = openPop === which ? null : which;
	}

	// ── Аккаунт ──
	let accountBusy = $state(false);
	let license = $state('');
	let confirmReissue = $state(false);
	let confirmDelete = $state(false);

	async function register(): Promise<void> {
		accountBusy = true;
		try {
			await api.warpscoutRegister();
			notifications.success(m.warpscout_acct_registered());
			confirmReissue = false;
			await refreshNow();
		} catch (e) {
			notifications.error(warpErrorMessage(e));
		} finally {
			accountBusy = false;
		}
	}

	async function applyLicense(): Promise<void> {
		const key = license.trim();
		if (!key) return;
		accountBusy = true;
		try {
			const acct = await api.warpscoutLicense(key);
			notifications.success(
				acct.warpPlus ? m.warpscout_license_plus_applied() : m.warpscout_license_applied(),
			);
			license = '';
			await refreshNow();
		} catch (e) {
			notifications.error(warpErrorMessage(e));
		} finally {
			accountBusy = false;
		}
	}

	async function deleteAccount(): Promise<void> {
		accountBusy = true;
		try {
			await api.warpscoutDeleteAccount();
			notifications.success(m.warpscout_acct_deleted());
			confirmDelete = false;
			openPop = null;
			await refreshNow();
		} catch (e) {
			notifications.error(warpErrorMessage(e));
		} finally {
			accountBusy = false;
		}
	}

	// ── Конфигурация ──
	function sanitizeInt(v: unknown, def: number, min: number, max: number): number {
		const n = Math.round(Number(v));
		if (!Number.isFinite(n)) return def;
		return Math.min(max, Math.max(min, n));
	}

	function sanitizedDraft(cfg: WarpConfig): WarpConfig {
		const out = cloneConfig(cfg);
		out.scanInterface = (out.scanInterface ?? '').trim() || undefined;
		for (const p of out.profiles) {
			p.checkIntervalSec = sanitizeInt(p.checkIntervalSec, 60, 15, 86400);
			p.failThreshold = sanitizeInt(p.failThreshold, 2, 1, 100);
			p.cooldownSec = sanitizeInt(p.cooldownSec, 120, 0, 86400);
			p.filters.maxRttMs = sanitizeInt(p.filters.maxRttMs, 0, 0, 10000);
			p.filters.excludeColos ??= [];
			p.filters.allowedCountries ??= [];
		}
		out.awg.jc = sanitizeInt(out.awg.jc, 4, 0, 128);
		out.awg.jmin = sanitizeInt(out.awg.jmin, 40, 0, 1280);
		out.awg.jmax = sanitizeInt(out.awg.jmax, 70, 0, 1280);
		if (out.profileMode === 'single') {
			const active = out.profiles.find((p) => p.enabled);
			out.activeProfileId = active?.id ?? '';
		}
		return out;
	}

	async function saveConfig(quietMessage?: string): Promise<boolean> {
		const payload = sanitizedDraft(draft);
		const missing = payload.profiles.find((p) => !p.targetId);
		if (missing) {
			notifications.error(
				m.warpscout_profile_missing_target({ name: missing.name || m.warpscout_unnamed() }),
			);
			return false;
		}
		saving = true;
		try {
			const saved = await api.warpscoutSaveConfig(payload);
			draft = cloneConfig(saved);
			serverConfigJson = JSON.stringify(saved);
			if (ws) ws.config = saved;
			notifications.success(quietMessage || m.warpscout_settings_saved());
			await refreshNow();
			return true;
		} catch (e) {
			notifications.error(warpErrorMessage(e));
			return false;
		} finally {
			saving = false;
		}
	}

	async function selectScanRoute(ifname: string): Promise<void> {
		const normalized = ifname.trim();
		draft.scanInterface = normalized || undefined;
		openPop = null;
		// Если все существующие профили валидны — сохраняем маршрут сразу в один клик.
		if (!draft.profiles.some((p) => !p.targetId)) {
			const label = normalized ? routeDisplayLabel(normalized) : m.warpscout_route_main_wan();
			await saveConfig(m.warpscout_route_saved({ label }));
		}
	}

	async function saveAwgPopover(): Promise<void> {
		const ok = await saveConfig(m.warpscout_awg_saved());
		if (ok) openPop = null;
	}

	function resetDraft(): void {
		if (ws) draft = cloneConfig(ws.config);
	}

	async function resetProfileStats(profileId: string): Promise<void> {
		try {
			await api.warpscoutResetStats(profileId);
			notifications.success(m.warpscout_stats_reset_ok());
			await refreshNow();
		} catch (e) {
			notifications.error(warpErrorMessage(e));
		}
	}

	const signatureOptions = $derived<DropdownOption[]>(
		SIGNATURE_PROFILES.map((s) => ({
			value: s,
			label: s === 'none' ? m.warpscout_sig_none() : s,
		})),
	);

	const routes = $derived<WarpRouteRef[]>(ws?.routes ?? []);
	const defaultWan = $derived(ws?.defaultWan ?? '');

	function routeDisplayLabel(ifname: string): string {
		if (!ifname) {
			const def = routes.find((r) => r.name === defaultWan);
			if (def) return m.warpscout_route_auto_named({ name: def.label || def.name });
			return defaultWan
				? m.warpscout_route_auto_named({ name: defaultWan })
				: m.warpscout_route_auto_default();
		}
		const found = routes.find((r) => r.name === ifname);
		if (found) {
			return found.label && found.label !== found.name ? `${found.label} (${found.name})` : found.name;
		}
		return ifname;
	}

	const selectedScanIface = $derived((draft.scanInterface ?? '').trim());
	const routeChipText = $derived.by(() => {
		if (!selectedScanIface) {
			const def = routes.find((r) => r.name === defaultWan);
			if (def && def.label && def.label !== def.name) return `WAN · ${def.label} (${def.name})`;
			return defaultWan ? `WAN · ${defaultWan}` : m.warpscout_route_auto_default();
		}
		const found = routes.find((r) => r.name === selectedScanIface);
		if (found && found.label && found.label !== found.name) return `${found.label} (${found.name})`;
		return selectedScanIface;
	});
	const routeSummary = $derived(routeDisplayLabel(selectedScanIface));

	const awgDirty = $derived(
		!!ws && JSON.stringify(draft.awg) !== JSON.stringify(ws.config.awg),
	);

	const history = $derived([...(ws?.history ?? [])].slice(0, 30));
	const account = $derived(ws?.account ?? null);
</script>

<div class="warp-tab">
	{#if loadError && !ws}
		<Card padding="sm">
			<div class="error-box">
				<AlertTriangle size={16} />
				<span>{m.warpscout_load_failed({ err: loadError })}</span>
				<Button size="sm" onclick={refreshNow}>
					{#snippet iconBefore()}<RefreshCcw size={14} />{/snippet}
					{m.common_retry()}
				</Button>
			</div>
		</Card>
	{:else if !ws}
		<Card padding="sm"><p class="note">{m.common_loading()}</p></Card>
	{:else}
		{#if !ws.engine.available}
			<div class="banner warn">
				<AlertTriangle size={15} />
				<div>
					<b>{m.warpscout_err_engine_missing()}</b>
					<div class="muted">
						<RichText
							text={m.warpscout_engine_expected_at({
								path: ws.engine.path || '/opt/sbin/awgm-warpscout',
							})}
						/>
					</div>
				</div>
			</div>
		{/if}
		{#if loadError}
			<div class="banner err">
				<AlertTriangle size={15} />
				<span>{m.warpscout_conn_lost({ err: loadError })}</span>
			</div>
		{/if}
		{#if ws.accountUnreadable}
			<div class="banner err">
				<AlertTriangle size={15} />
				<span>{m.warpscout_acct_unreadable()}</span>
			</div>
		{/if}

		<!-- Единая компактная шапка Концепта А: слева чипы Аккаунта и Обфускации AWG, справа — Маршрут сканера -->
		<div class="top-bar">
			<div class="chips-left">
				<!-- 1. Аккаунт WARP -->
				{#if !account}
					<div class="acct-cta">
						<span class="chip-dot muted"></span>
						<span class="cta-text">{m.warpscout_acct_not_registered()}</span>
						<Button size="sm" variant="primary" loading={accountBusy} onclick={register}>
							{#snippet iconBefore()}<UserPlus size={13} />{/snippet}
							{m.warpscout_register_btn()}
						</Button>
					</div>
				{:else}
					<div class="pop-anchor">
						<button
							type="button"
							class="status-chip"
							class:active={openPop === 'acct'}
							aria-expanded={openPop === 'acct'}
							onclick={() => togglePop('acct')}
						>
							<span class="chip-dot ok"></span>
							<span class="chip-strong">{account.warpPlus ? 'WARP+' : 'WARP Free'}</span>
							<span class="chip-sep">·</span>
							<span class="mono chip-dim">{account.addressV4 || shortId(account.id)}</span>
							<span class="chev" class:open={openPop === 'acct'}><ChevronDown size={13} /></span>
						</button>

						{#if openPop === 'acct'}
							<div class="popover acct-pop" role="dialog" aria-label={m.warpscout_acct_pop_aria()}>
								<div class="pop-head">
									<span class="pop-title">{m.warpscout_acct_pop_title()}</span>
									<span class="tier-pill" class:plus={account.warpPlus}>
										{account.warpPlus ? 'WARP+' : account.accountType || 'free'}
									</span>
								</div>

								<div class="kv-grid">
									<div class="kv">
										<span class="k">Device ID</span>
										<span class="v mono" title={account.id}>{shortId(account.id)}</span>
									</div>
									<div class="kv">
										<span class="k">{m.warpscout_acct_ipv4()}</span>
										<span class="v mono">{account.addressV4 || '—'}</span>
									</div>
									<div class="kv">
										<span class="k">{m.warpscout_acct_created()}</span>
										<span class="v">{formatTime(account.createdAt)}</span>
									</div>
									<div class="kv">
										<span class="k">{m.warpscout_acct_keys()}</span>
										<span class="v">{m.warpscout_acct_encrypted()}</span>
									</div>
								</div>

								<div class="lic-box">
									<label class="lic-label" for="warp-license">
										<KeyRound size={12} />
										<span>{m.warpscout_license_label()}</span>
									</label>
									<div class="lic-row">
										<input
											id="warp-license"
											class="lic-input mono"
											type="text"
											placeholder="xxxxxxxx-xxxxxxxx-xxxxxxxx"
											bind:value={license}
											autocomplete="off"
											spellcheck="false"
											onkeydown={(e) => {
												if (e.key === 'Enter' && license.trim()) void applyLicense();
											}}
										/>
										<Button
											size="sm"
											variant="primary"
											loading={accountBusy}
											disabled={!license.trim()}
											onclick={applyLicense}
										>
											{m.warpscout_license_bind()}
										</Button>
									</div>
								</div>

								<div class="pop-foot">
									<Button
										size="sm"
										variant="ghost"
										disabled={accountBusy}
										onclick={() => {
											openPop = null;
											confirmReissue = true;
										}}
									>
										{#snippet iconBefore()}<RefreshCcw size={13} />{/snippet}
										{m.warpscout_reissue_btn()}
									</Button>
									<Button
										size="sm"
										variant="outline-danger"
										disabled={accountBusy}
										onclick={() => {
											openPop = null;
											confirmDelete = true;
										}}
									>
										{#snippet iconBefore()}<Trash2 size={13} />{/snippet}
										{m.common_delete()}
									</Button>
								</div>
							</div>
						{/if}
					</div>
				{/if}

				<!-- 2. Обход ТСПУ (параметры AWG) -->
				<div class="pop-anchor">
					<button
						type="button"
						class="status-chip"
						class:active={openPop === 'awg'}
						aria-expanded={openPop === 'awg'}
						title={m.warpscout_awg_chip_tip()}
						onclick={() => togglePop('awg')}
					>
						<Shield size={13} class="chip-ic" />
						<span class="chip-dim hide-sm">{m.warpscout_awg_chip_label()}</span>
						<span class="mono chip-strong">{draft.awg.signatureProfile}</span>
						<span class="mono chip-dim">· Jc {draft.awg.jc} · {draft.awg.jmin}–{draft.awg.jmax}</span>
						{#if awgDirty}<span class="unsaved-dot" title={m.warpscout_unsaved_tip()}></span>{/if}
						<span class="chev" class:open={openPop === 'awg'}><ChevronDown size={13} /></span>
					</button>

					{#if openPop === 'awg'}
						<div class="popover awg-pop" role="dialog" aria-label={m.warpscout_awg_pop_aria()}>
							<div class="pop-head">
								<span class="pop-title">{m.warpscout_awg_pop_title()}</span>
							</div>

							<div class="awg-pop-body">
								<Dropdown
									label={m.warpscout_awg_sig_label()}
									value={draft.awg.signatureProfile}
									options={signatureOptions}
									fullWidth
									onchange={(v) => (draft.awg.signatureProfile = v)}
								/>

								<div class="awg-nums">
									<label class="num-field">
										<span>Jc</span>
										<input class="num" type="number" min="0" max="128" bind:value={draft.awg.jc} />
									</label>
									<label class="num-field">
										<span>Jmin</span>
										<input class="num" type="number" min="0" max="1280" bind:value={draft.awg.jmin} />
									</label>
									<label class="num-field">
										<span>Jmax</span>
										<input class="num" type="number" min="0" max="1280" bind:value={draft.awg.jmax} />
									</label>
								</div>

								<p class="pop-hint">{m.warpscout_awg_hint()}</p>
							</div>

							<div class="pop-foot end">
								{#if dirty}
									<Button size="sm" variant="ghost" onclick={resetDraft} disabled={saving}>
										{m.warpscout_reset_btn()}
									</Button>
								{/if}
								<Button
									size="sm"
									variant="primary"
									loading={saving}
									disabled={!dirty}
									onclick={() => saveAwgPopover()}
								>
									{m.common_save()}
								</Button>
							</div>
						</div>
					{/if}
				</div>
			</div>

			<!-- Справа в шапке Концепта А: интерактивный Маршрут сканера -->
			<div class="chips-right">
				<div class="pop-anchor">
					<button
						type="button"
						class="status-chip"
						class:active={openPop === 'route'}
						aria-expanded={openPop === 'route'}
						title={m.warpscout_route_saved({ label: routeSummary })}
						onclick={() => togglePop('route')}
					>
						<Route size={13} class="chip-ic" />
						<span class="chip-dim">{m.warpscout_route_chip_label()}</span>
						<span class="mono chip-strong">{routeChipText}</span>
						<span class="chev" class:open={openPop === 'route'}><ChevronDown size={13} /></span>
					</button>

					{#if openPop === 'route'}
						<div class="popover route-pop align-right" role="dialog" aria-label={m.warpscout_route_pop_title()}>
							<div class="pop-head">
								<span class="pop-title">{m.warpscout_route_pop_title()}</span>
							</div>
							<p class="pop-hint">{m.warpscout_route_pop_hint()}</p>

							<div class="route-list">
								<button
									type="button"
									class="route-opt"
									class:selected={!selectedScanIface}
									onclick={() => selectScanRoute('')}
								>
									<span class="chip-dot ok"></span>
									<div class="route-opt-main">
										<span class="route-opt-title">{m.warpscout_route_auto_default()}</span>
										<span class="route-opt-sub">
											{defaultWan
												? m.warpscout_route_pref_link({ name: defaultWan })
												: m.warpscout_route_ndms_prio()}
										</span>
									</div>
									{#if !selectedScanIface}<Check size={14} class="route-check" />{/if}
								</button>

								{#if routes.some((r) => r.kind === 'wan')}
									<div class="route-group-label">{m.warpscout_route_group_wan()}</div>
									{#each routes.filter((r) => r.kind === 'wan') as r (r.name)}
										<button
											type="button"
											class="route-opt"
											class:selected={selectedScanIface === r.name}
											onclick={() => selectScanRoute(r.name)}
										>
											<span class="chip-dot" class:ok={r.up} class:muted={!r.up}></span>
											<div class="route-opt-main">
												<span class="route-opt-title">{r.label || r.name}</span>
												<span class="route-opt-sub mono">
													{r.up ? r.name : m.warpscout_route_no_link({ name: r.name })}
												</span>
											</div>
											{#if selectedScanIface === r.name}<Check size={14} class="route-check" />{/if}
										</button>
									{/each}
								{/if}

								{#if routes.some((r) => r.kind === 'tunnel')}
									<div class="route-group-label">{m.warpscout_route_group_tunnels()}</div>
									{#each routes.filter((r) => r.kind === 'tunnel') as r (r.name)}
										<button
											type="button"
											class="route-opt"
											class:selected={selectedScanIface === r.name}
											onclick={() => selectScanRoute(r.name)}
										>
											<span class="chip-dot" class:ok={r.up} class:muted={!r.up}></span>
											<div class="route-opt-main">
												<span class="route-opt-title">{r.label || r.name}</span>
												<span class="route-opt-sub mono">
													{r.up ? r.name : m.warpscout_route_stopped({ name: r.name })}
												</span>
											</div>
											{#if selectedScanIface === r.name}<Check size={14} class="route-check" />{/if}
										</button>
									{/each}
								{/if}
							</div>
						</div>
					{/if}
				</div>
			</div>
		</div>

		<!-- Двухколоночный пульт Концепта А: слева Радар эндпоинтов (7), справа Автопилот + Журнал (5) -->
		<div class="cockpit-grid">
			<div class="col-scanner">
				<WarpScannerCard
					scan={ws.scan}
					engineAvailable={ws.engine.available}
					hasAccount={!!account}
					{targets}
					scanInterface={selectedScanIface}
					{routeSummary}
					onchanged={refreshNow}
					ontargetschanged={loadTargets}
				/>
			</div>

			<div class="col-autopilot">
				<WarpAutopilotCard
					bind:config={draft}
					{targets}
					watch={ws.watch ?? []}
					stats={ws.stats ?? []}
					{history}
					{dirty}
					{saving}
					onsave={async () => {
						await saveConfig(m.warpscout_autopilot_saved());
					}}
					onreset={resetDraft}
					onresetstats={resetProfileStats}
				/>
			</div>
		</div>
	{/if}
</div>

<ConfirmModal
	open={confirmReissue}
	title={m.warpscout_confirm_reissue_title()}
	message={m.warpscout_confirm_reissue_msg()}
	secondary={m.warpscout_confirm_reissue_sec()}
	confirmLabel={m.warpscout_reissue_btn()}
	variant="danger"
	busy={accountBusy}
	onConfirm={register}
	onClose={() => (confirmReissue = false)}
/>

<ConfirmModal
	open={confirmDelete}
	title={m.warpscout_confirm_del_acct_title()}
	message={m.warpscout_confirm_del_acct_msg()}
	secondary={m.warpscout_confirm_del_acct_sec()}
	confirmLabel={m.common_delete()}
	variant="danger"
	busy={accountBusy}
	onConfirm={deleteAccount}
	onClose={() => (confirmDelete = false)}
/>

<style>
	.warp-tab {
		display: flex;
		flex-direction: column;
		gap: 0.75rem;
		margin-top: 0.55rem;
	}
	.top-bar {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.5rem 0.75rem;
		padding: 0.45rem 0.6rem;
		border-radius: var(--radius);
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		box-shadow: var(--shadow);
		flex-wrap: wrap;
	}
	.chips-left {
		display: flex;
		align-items: center;
		gap: 0.45rem;
		flex-wrap: wrap;
		min-width: 0;
	}
	.chips-right {
		display: flex;
		align-items: center;
		margin-left: auto;
	}
	.cockpit-grid {
		display: grid;
		grid-template-columns: minmax(0, 7fr) minmax(0, 5fr);
		gap: 0.85rem;
		align-items: start;
	}
	.col-scanner,
	.col-autopilot {
		min-width: 0;
	}
	.acct-cta {
		display: inline-flex;
		align-items: center;
		gap: 0.5rem;
		padding: 0.15rem 0.25rem 0.15rem 0.55rem;
		border-radius: var(--radius-sm);
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		flex-wrap: wrap;
	}
	.cta-text {
		font-size: 0.76rem;
		font-weight: 500;
		color: var(--color-text-secondary);
	}
	.pop-anchor {
		position: relative;
		display: inline-flex;
	}
	.status-chip {
		display: inline-flex;
		align-items: center;
		gap: 0.35rem;
		height: 30px;
		padding: 0 0.6rem;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-primary);
		color: var(--color-text-primary);
		font: inherit;
		font-size: 0.75rem;
		cursor: pointer;
		white-space: nowrap;
		transition: border-color 0.15s ease, background 0.15s ease;
	}
	.status-chip:hover {
		border-color: var(--color-border-hover);
		background: var(--color-bg-hover);
	}
	.status-chip.active {
		border-color: var(--color-accent);
		background: var(--color-accent-tint);
	}
	.status-chip :global(.chip-ic) {
		color: var(--color-text-muted);
		flex-shrink: 0;
	}
	.chip-strong {
		font-weight: 600;
		color: var(--color-text-primary);
	}
	.chip-dim {
		color: var(--color-text-secondary);
	}
	.chip-sep {
		color: var(--color-text-muted);
	}
	.chip-dot {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		flex-shrink: 0;
		background: var(--color-text-muted);
	}
	.chip-dot.ok {
		background: var(--color-success);
	}
	.unsaved-dot {
		width: 6px;
		height: 6px;
		border-radius: 50%;
		background: var(--color-warning);
		flex-shrink: 0;
	}
	.chev {
		display: inline-flex;
		color: var(--color-text-muted);
		transition: transform 0.15s ease;
	}
	.chev.open {
		transform: rotate(180deg);
	}

	/* Поповеры верхней панели */
	.popover {
		position: absolute;
		top: calc(100% + 6px);
		left: 0;
		z-index: 40;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		box-shadow: 0 12px 30px rgba(0, 0, 0, 0.28);
		padding: 0.75rem;
		display: flex;
		flex-direction: column;
		gap: 0.65rem;
	}
	.popover.align-right {
		left: auto;
		right: 0;
	}
	.acct-pop {
		width: min(21.5rem, calc(100vw - 2rem));
	}
	.route-pop {
		width: min(20.5rem, calc(100vw - 2rem));
	}
	.awg-pop {
		width: min(21rem, calc(100vw - 2rem));
	}
	.pop-head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.5rem;
	}
	.pop-title {
		font-size: 0.82rem;
		font-weight: 600;
		color: var(--color-text-primary);
	}
	.pop-hint {
		margin: 0;
		font-size: 0.72rem;
		line-height: 1.38;
		color: var(--color-text-muted);
	}
	.tier-pill {
		padding: 0.08rem 0.45rem;
		border-radius: 999px;
		font-size: 0.68rem;
		font-weight: 600;
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		color: var(--color-text-secondary);
	}
	.tier-pill.plus {
		background: var(--color-accent-tint);
		border-color: var(--color-accent-border);
		color: var(--color-accent);
	}
	.kv-grid {
		display: grid;
		grid-template-columns: repeat(2, minmax(0, 1fr));
		gap: 0.4rem;
	}
	.kv {
		display: flex;
		flex-direction: column;
		gap: 0.1rem;
		padding: 0.35rem 0.5rem;
		border-radius: var(--radius-sm);
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
	}
	.kv .k {
		font-size: 0.67rem;
		color: var(--color-text-muted);
	}
	.kv .v {
		font-size: 0.76rem;
		font-weight: 500;
		color: var(--color-text-primary);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.lic-box {
		display: flex;
		flex-direction: column;
		gap: 0.3rem;
		padding-top: 0.5rem;
		border-top: 1px solid var(--color-border);
	}
	.lic-label {
		display: inline-flex;
		align-items: center;
		gap: 0.3rem;
		font-size: 0.72rem;
		font-weight: 500;
		color: var(--color-text-secondary);
	}
	.lic-row {
		display: flex;
		align-items: center;
		gap: 0.4rem;
	}
	.lic-input {
		flex: 1;
		min-width: 0;
		height: 30px;
		box-sizing: border-box;
		padding: 0.2rem 0.45rem;
		font-size: 0.76rem;
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		color: var(--color-text-primary);
	}
	.pop-foot {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.4rem;
		padding-top: 0.5rem;
		border-top: 1px solid var(--color-border);
	}
	.pop-foot.end {
		justify-content: flex-end;
	}

	/* Список маршрутов в поповере */
	.route-list {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
		max-height: 15rem;
		overflow-y: auto;
	}
	.route-group-label {
		font-size: 0.67rem;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		color: var(--color-text-muted);
		padding: 0.3rem 0.25rem 0.1rem;
	}
	.route-opt {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		width: 100%;
		padding: 0.4rem 0.55rem;
		border-radius: var(--radius-sm);
		border: 1px solid transparent;
		background: var(--color-bg-primary);
		color: var(--color-text-primary);
		font: inherit;
		text-align: left;
		cursor: pointer;
		transition: border-color 0.12s ease, background 0.12s ease;
	}
	.route-opt:hover {
		border-color: var(--color-border-hover);
		background: var(--color-bg-hover);
	}
	.route-opt.selected {
		border-color: var(--color-accent-border);
		background: var(--color-accent-tint);
	}
	.route-opt-main {
		display: flex;
		flex-direction: column;
		min-width: 0;
		flex: 1;
	}
	.route-opt-title {
		font-size: 0.77rem;
		font-weight: 600;
		color: var(--color-text-primary);
	}
	.route-opt-sub {
		font-size: 0.69rem;
		color: var(--color-text-muted);
	}
	.route-opt :global(.route-check) {
		color: var(--color-accent);
		flex-shrink: 0;
	}

	/* Поповер обфускации AWG */
	.awg-pop-body {
		display: flex;
		flex-direction: column;
		gap: 0.55rem;
	}
	.awg-nums {
		display: grid;
		grid-template-columns: repeat(3, minmax(0, 1fr));
		gap: 0.45rem;
	}
	.num-field {
		display: flex;
		flex-direction: column;
		gap: 0.2rem;
		font-size: 0.72rem;
		font-weight: 500;
		color: var(--color-text-secondary);
	}
	.num {
		width: 100%;
		height: 32px;
		box-sizing: border-box;
		padding: 0.25rem 0.45rem;
		font-family: var(--font-mono);
		font-size: 0.8125rem;
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		color: var(--color-text-primary);
	}

	.banner {
		display: flex;
		align-items: flex-start;
		gap: 0.55rem;
		padding: 0.55rem 0.8rem;
		border-radius: var(--radius-sm);
		font-size: 0.78rem;
		color: var(--color-text-primary);
	}
	.banner.warn {
		background: var(--color-warning-tint);
		border: 1px solid var(--color-warning-border);
	}
	.banner.err {
		background: var(--color-error-tint);
		border: 1px solid var(--color-error-border);
	}
	.error-box {
		display: flex;
		align-items: center;
		gap: 0.6rem;
		color: var(--color-error);
		font-size: 0.8125rem;
		flex-wrap: wrap;
	}
	.note {
		margin: 0;
		font-size: 0.78rem;
		color: var(--color-text-secondary);
	}
	.mono {
		font-family: var(--font-mono);
	}
	.muted {
		color: var(--color-text-muted);
	}

	@media (max-width: 1080px) {
		.cockpit-grid {
			grid-template-columns: 1fr;
		}
	}

	@media (max-width: 720px) {
		.hide-sm {
			display: none;
		}
		.chips-right {
			width: 100%;
			margin-left: 0;
		}
		.chips-right .pop-anchor,
		.chips-right .status-chip {
			width: 100%;
		}
		.chips-right .chev {
			margin-left: auto;
		}
		.popover,
		.popover.align-right {
			position: fixed;
			left: 0.75rem;
			right: 0.75rem;
			top: 5.5rem;
			width: auto;
		}
	}
</style>
