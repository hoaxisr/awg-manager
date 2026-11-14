<script lang="ts">
	import { onDestroy, onMount } from 'svelte';
	import { AlertTriangle, RefreshCcw } from 'lucide-svelte';
	import { api } from '$lib/api/client';
	import { m } from '$lib/i18n';
	import { Button, Card } from '$lib/components/ui';
	import RichText from '$lib/components/ui/RichText.svelte';
	import { notifications } from '$lib/stores/notifications';
	import { warpFindJunkPush, warpScanPush, warpWatchPush } from '$lib/stores/warpscout';
	import type { WarpConfig, WarpRouteRef, WarpScoutState, WarpTunnelRef } from '$lib/types/warpscout';
	import WarpAccountChip from './WarpAccountChip.svelte';
	import WarpAwgPopover from './WarpAwgPopover.svelte';
	import WarpRoutePicker from './WarpRoutePicker.svelte';
	import WarpScannerCard from './WarpScannerCard.svelte';
	import WarpAutopilotCard from './WarpAutopilotCard.svelte';
	import {
		routeDisplayLabel,
		sanitizeConfig,
		sectionEqual,
		syncDraft,
		warpErrorMessage,
		withSection,
	} from './warpscoutUi';
	import type { ConfigSection } from './warpscoutUi';

	// Состояние приходит по SSE (`warpscout:scan`, `warpscout:watchdog`);
	// опрос — редкая страховка на случай потерянного события.
	const SAFETY_POLL_MS = 30_000;
	// Сторож публикует событие на каждый шаг проверки — пачку сводим в один запрос.
	const WATCHDOG_RELOAD_DELAY_MS = 500;

	let ws = $state<WarpScoutState | null>(null);
	let loadErrObj = $state<unknown>(null);
	let targets = $state<WarpTunnelRef[]>([]);

	const loadError = $derived(loadErrObj ? warpErrorMessage(loadErrObj) : '');

	// Черновик конфигурации: профили автопилота и параметры AWG. У каждой части
	// своё «Сохранить»; часть без локальных правок следует за сервером.
	let draft = $state<WarpConfig>({
		profileMode: 'single',
		profiles: [],
		awg: { signatureProfile: 'quic_initial', jc: 4, jmin: 40, jmax: 70 },
	});
	let saving = $state(false);

	const profilesDirty = $derived(!!ws && !sectionEqual(draft, ws.config, 'profiles'));
	const awgDirty = $derived(!!ws && !sectionEqual(draft, ws.config, 'awg'));

	let destroyed = false;
	let pollTimer: ReturnType<typeof setTimeout> | null = null;
	let watchTimer: ReturnType<typeof setTimeout> | null = null;
	// Поколение запроса состояния: ответ устаревшего запроса не применяется.
	let loadSeq = 0;

	async function loadState(): Promise<void> {
		const my = ++loadSeq;
		try {
			const next = await api.warpscoutState();
			if (my !== loadSeq) return;
			draft = syncDraft(draft, ws?.config ?? null, next.config);
			ws = next;
			loadErrObj = null;
		} catch (e) {
			if (my !== loadSeq) return;
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

	function schedule(): void {
		if (destroyed) return;
		if (pollTimer) clearTimeout(pollTimer);
		pollTimer = setTimeout(tick, SAFETY_POLL_MS);
	}

	async function tick(): Promise<void> {
		pollTimer = null;
		if (destroyed) return;
		if (document.visibilityState !== 'hidden') await loadState();
		schedule();
	}

	async function refreshNow(): Promise<void> {
		await loadState();
		schedule();
	}

	function handleVisibility(): void {
		if (document.visibilityState === 'visible') void refreshNow();
	}

	let unsubscribe: Array<() => void> = [];

	onMount(() => {
		// В сторах лежит последнее событие, возможно давнее: берём только новые.
		let live = false;
		unsubscribe = [
			// Событие скана несёт полный снимок — применяем без запроса.
			warpScanPush.subscribe((scan) => {
				if (live && scan && ws) ws.scan = scan;
			}),
			warpFindJunkPush.subscribe((fj) => {
				if (!live || !fj || !ws) return;
				const wasApplied = ws.findJunk?.applied ?? false;
				ws.findJunk = fj;
				if (!wasApplied && fj.applied && fj.best?.awg) {
					ws.config = { ...ws.config, awg: { ...fj.best.awg } };
					draft = withSection(draft, ws.config, 'awg');
				}
			}),
			// Событие сторожа — статус одного профиля; статистика и журнал
			// подмен в него не входят, поэтому перечитываем состояние.
			warpWatchPush.subscribe(() => {
				if (!live || watchTimer || document.visibilityState === 'hidden') return;
				watchTimer = setTimeout(() => {
					watchTimer = null;
					void refreshNow();
				}, WATCHDOG_RELOAD_DELAY_MS);
			}),
		];
		live = true;
		document.addEventListener('visibilitychange', handleVisibility);
		void (async () => {
			await Promise.all([loadState(), loadTargets()]);
			schedule();
		})();
	});

	onDestroy(() => {
		destroyed = true;
		for (const off of unsubscribe) off();
		if (typeof document !== 'undefined') {
			document.removeEventListener('visibilitychange', handleVisibility);
		}
		if (pollTimer) clearTimeout(pollTimer);
		if (watchTimer) clearTimeout(watchTimer);
	});

	// ── Конфигурация ──
	async function postConfig(payload: WarpConfig, saved: ConfigSection[], okMessage: string): Promise<boolean> {
		saving = true;
		try {
			const next = await api.warpscoutSaveConfig(payload);
			// Запрос состояния, начатый до сохранения, вернёт прежнюю конфигурацию.
			loadSeq++;
			if (ws) {
				draft = syncDraft(draft, ws.config, next, saved);
				ws.config = next;
			}
			notifications.success(okMessage);
			await refreshNow();
			return true;
		} catch (e) {
			notifications.error(warpErrorMessage(e));
			return false;
		} finally {
			saving = false;
		}
	}

	// Каждое «Сохранить» отправляет серверную конфигурацию плюс только свою
	// часть: несохранённые правки соседней части на сервер не уходят.
	async function saveSection(section: ConfigSection, okMessage: string): Promise<boolean> {
		if (!ws) return false;
		const payload = withSection(ws.config, sanitizeConfig(draft), section);
		if (section === 'profiles') {
			const missing = payload.profiles.find((p) => !p.targetId);
			if (missing) {
				notifications.error(
					m.warpscout_profile_missing_target({ name: missing.name || m.warpscout_unnamed() }),
				);
				return false;
			}
		}
		return postConfig(payload, [section], okMessage);
	}

	function resetSection(section: ConfigSection): void {
		if (ws) draft = withSection(draft, ws.config, section);
	}

	// Маршрут сканера сохраняется сразу, одним щелчком.
	async function selectScanRoute(ifname: string): Promise<void> {
		if (!ws) return;
		const name = ifname.trim();
		const label = name ? routeDisplayLabel(routes, defaultWan, name) : m.warpscout_route_main_wan();
		await postConfig(
			{ ...ws.config, scanInterface: name || undefined },
			[],
			m.warpscout_route_saved({ label }),
		);
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

	const routes = $derived<WarpRouteRef[]>(ws?.routes ?? []);
	const defaultWan = $derived(ws?.defaultWan ?? '');
	const scanIface = $derived((ws?.config.scanInterface ?? '').trim());
	const routeSummary = $derived(routeDisplayLabel(routes, defaultWan, scanIface));

	const history = $derived((ws?.history ?? []).slice(0, 30));
	const account = $derived(ws?.account ?? null);
	const usedIpv4 = $derived(ws?.usedIpv4 ?? []);
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

		<!-- Шапка: слева учётная запись и обфускация AWG, справа — маршрут сканера -->
		<div class="top-bar">
			<div class="chips-left">
				<WarpAccountChip {account} {targets} {usedIpv4} onchanged={refreshNow} />
				<WarpAwgPopover
					bind:awg={draft.awg}
					obfuscationSources={ws.obfuscationSources ?? []}
					dirty={awgDirty}
					{saving}
					findJunk={ws.findJunk ?? null}
					engineAvailable={ws.engine.available}
					hasAccount={!!account}
					scanRunning={ws.scan.running}
					scanInterface={scanIface}
					onsave={() => saveSection('awg', m.warpscout_awg_saved())}
					onreset={() => resetSection('awg')}
					onfindjunkchanged={refreshNow}
					onfindjunkapplied={async (awg) => {
						loadSeq++;
						if (ws) {
							ws.config = { ...ws.config, awg: { ...awg } };
							draft = withSection(draft, ws.config, 'awg');
						}
						notifications.success(m.warpscout_findjunk_applied_ok());
						await refreshNow();
					}}
				/>
			</div>

			<div class="chips-right">
				<WarpRoutePicker {routes} {defaultWan} selected={scanIface} onselect={selectScanRoute} />
			</div>
		</div>

		<!-- Два столбца: слева сканер узлов, справа автопилот с журналом подмен -->
		<div class="cockpit-grid">
			<div class="col-scanner">
				<WarpScannerCard
					scan={ws.scan}
					engineAvailable={ws.engine.available}
					hasAccount={!!account}
					accountAddressV4={account?.addressV4 ?? ''}
					{targets}
					{usedIpv4}
					{routes}
					obfuscationSources={ws.obfuscationSources ?? []}
					globalAwg={ws.config.awg}
					scanInterface={scanIface}
					{routeSummary}
					autopilotLocked={profilesDirty}
					findJunkRunning={ws.findJunk?.running ?? false}
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
					dirty={profilesDirty}
					{saving}
					onsave={async () => {
						await saveSection('profiles', m.warpscout_autopilot_saved());
					}}
					onreset={() => resetSection('profiles')}
					onresetstats={resetProfileStats}
				/>
			</div>
		</div>
	{/if}
</div>

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
		max-width: 100%;
	}
	.chips-right {
		display: flex;
		align-items: center;
		margin-left: auto;
		min-width: 0;
		max-width: 100%;
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
	.muted {
		color: var(--color-text-muted);
	}

	@media (max-width: 1080px) {
		.cockpit-grid {
			grid-template-columns: 1fr;
		}
	}

	@media (max-width: 720px) {
		.chips-right {
			width: 100%;
			margin-left: 0;
		}
		/* Чип маршрута на узком экране занимает всю строку. */
		.chips-right :global(.pop-anchor),
		.chips-right :global(.status-chip) {
			width: 100%;
		}
		.chips-right :global(.chev) {
			margin-left: auto;
		}
	}
</style>
