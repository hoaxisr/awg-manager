<script lang="ts">
	import { m } from '$lib/i18n';
	import { formatAutolistBytes, formatAutolistDuration, formatAutolistTime } from './time';
	import { autolistStatus } from '$lib/stores/autolist';
	import { notifications } from '$lib/stores/notifications';
	import { settings } from '$lib/stores/settings';
	import { api } from '$lib/api/client';
	import {
		autolistConfigDraft,
		defaultAutolistConfig,
		AUTOLIST_DEFAULT_ADDS_PER_HOUR,
		AUTOLIST_DEFAULT_WAN_URL,
		AUTOLIST_MAX_ADDS_PER_HOUR,
		buildAutolistConfigPayload,
		mergeAutolistExclusions,
	} from './configPayload';
	import { errorMessage } from '$lib/utils/errorMessage';
	import { clientName } from './clientName';
	import RecheckModal from './RecheckModal.svelte';
	import type {
		AWGTagInfo,
		AutolistBatchFailure,
		AutolistCandidate,
		AutolistCandidateState,
		AutolistCheckEvidence,
		AutolistConfig,
		AutolistHistoryEntry,
		AutolistVerdict,
		PolicyDevice,
		RouterInterface,
		SingboxRouterDNSServer,
		SingboxRouterDNSGlobals,
		SingboxRouterRuleSet,
		SingboxTunnel,
		Subscription,
	} from '$lib/types';
	import { Button, IconButton, Modal, Toggle, Dropdown, Input, ChipMultiSelect, Tabs, Stat, StatStrip } from '$lib/components/ui';
	import { PageHeader } from '$lib/components/layout';
	import {
		RotateCw,
		RefreshCw,
		EyeOff,
		Eye,
		CircleCheck,
		CircleAlert,
		CircleX,
		CircleDashed,
		Settings2,
		TriangleAlert,
		ShieldPlus,
		Trash2,
		Power,
		ArchiveRestore,
	} from 'lucide-svelte';

	interface Props {
		/** Устройства роутера: по ним клиенту-кандидату ищется имя. */
		policyDevices?: PolicyDevice[];
	}

	let { policyDevices = [] }: Props = $props();

	let status = $derived($autolistStatus.data);
	let loading = $derived($autolistStatus.lastFetchedAt === 0 && $autolistStatus.status !== 'error');
	let running = $derived(status?.running === true);
	let capturePaused = $derived(status?.capturePaused === true);
	let captureActive = $derived(status?.captureActive === true && !capturePaused);
	let candidates = $derived(status?.candidates ?? []);
	let history = $derived(status?.history ?? []);
	/** Ожидающие/проверяемые кандидаты — вкладка с выбором и массовыми действиями. */
	let waitingCandidates = $derived(candidates.filter((c) => c.state === 'waiting' || c.state === 'checking'));
	/** Уже добавленные в туннель — отдельная вкладка с действием «удалить правило». */
	let addedCandidates = $derived(candidates.filter((c) => c.state === 'added'));
	let addLimitExhausted = $derived(
		(status?.addsLimit ?? 0) > 0 && (status?.addsUsedHour ?? 0) >= (status?.addsLimit ?? 0),
	);
	let view = $state<'candidates' | 'added' | 'history'>('candidates');
	let viewTabs = $derived([
		{ id: 'candidates', label: m.autolist_view_candidates(), badge: waitingCandidates.length },
		{ id: 'added', label: m.autolist_view_added(), badge: addedCandidates.length },
		{ id: 'history', label: m.autolist_view_history(), badge: history.length },
	]);

	// Развёрнутые карточки кандидатов — по id.
	let busyIds = $state<Set<string>>(new Set());
	let actionBusy = $state(false);
	let lifecycleBusy = $state(false);
	let refreshing = $state(false);

	// ── Массовые действия над ожидающими кандидатами ─────────────────────
	let selectedIds = $state<Set<string>>(new Set());
	let bulkBusy = $state(false);
	/** Ошибки per-ID последнего batch-запуска — карточки с ошибкой и их тексты. */
	let bulkFailures = $state<AutolistBatchFailure[]>([]);

	let selectableCandidates = $derived(waitingCandidates);
	let allSelected = $derived(
		selectableCandidates.length > 0 && selectableCandidates.every((c) => selectedIds.has(c.id)),
	);
	let selectedCount = $derived(selectedIds.size);

	function toggleSelected(id: string) {
		const next = new Set(selectedIds);
		if (next.has(id)) next.delete(id);
		else next.add(id);
		selectedIds = next;
	}

	function toggleAllSelected() {
		selectedIds = allSelected ? new Set() : new Set(selectableCandidates.map((c) => c.id));
	}

	/** Кандидаты, выбранные и всё ещё доступные (могли измениться после статуса). */
	function selectedCandidates(): AutolistCandidate[] {
		return selectableCandidates.filter((c) => selectedIds.has(c.id));
	}

	/** Домены выбранных кандидатов; null — среди них есть IP-only цели. */
	function selectedDomains(): string[] | null {
		const picked = selectedCandidates();
		if (picked.length === 0) return null;
		const domains: string[] = [];
		for (const c of picked) {
			if (!c.target.domain) return null;
			domains.push(c.target.domain);
		}
		return domains;
	}

	function setBusy(id: string, value: boolean) {
		const next = new Set(busyIds);
		if (value) next.add(id);
		else next.delete(id);
		busyIds = next;
	}

	/** Три активные проверки карточки в фиксированном порядке. */
	function evFields(c: AutolistCandidate): Array<{ key: 'direct' | 'wan' | 'tunnel'; e: AutolistCheckEvidence }> {
		return [
			{ key: 'direct', e: c.direct },
			{ key: 'wan', e: c.wan },
			{ key: 'tunnel', e: c.tunnel },
		];
	}

	async function refresh() {
		refreshing = true;
		try {
			await autolistStatus.refetch();
		} finally {
			refreshing = false;
		}
	}

	// ── Lifecycle: install / enable / disable ────────────────────────────

	// Установленный пакет тот же вызов обновляет на месте (opkg install).
	async function installSatellite(upgrade = false) {
		if (lifecycleBusy) return;
		lifecycleBusy = true;
		try {
			await api.installAutolist();
			notifications.success(upgrade ? m.autolist_update_ok() : m.autolist_install_ok());
			autolistStatus.invalidate();
		} catch (e) {
			notifications.error(errorMessage(e, upgrade ? m.autolist_update_err() : m.autolist_install_err()));
		} finally {
			lifecycleBusy = false;
		}
	}

	async function setSatelliteEnabled(enabled: boolean) {
		if (lifecycleBusy) return;
		lifecycleBusy = true;
		try {
			await api.controlAutolist(enabled ? 'enable' : 'disable');
			notifications.success(enabled ? m.autolist_enable_ok() : m.autolist_disable_ok());
			autolistStatus.invalidate();
		} catch (e) {
			notifications.error(errorMessage(e, m.autolist_control_err()));
		} finally {
			lifecycleBusy = false;
		}
	}

	// ── Candidate actions ────────────────────────────────────────────────

	/**
	 * Кандидат, открытый в модалке повторной проверки. Хранится ОБЪЕКТОМ, а
	 * не id: карточка может обновиться поллингом, а модалка фиксирует
	 * кандидата на момент открытия и не должна перезапускать прогон.
	 */
	let recheckCandidate = $state<AutolistCandidate | null>(null);

	function openRecheck(c: AutolistCandidate) {
		if (busyIds.has(c.id) || actionBusy) return;
		recheckCandidate = c;
	}

	function closeRecheck() {
		// RecheckModal сам пишет в стор при успехе — инвалидация по закрытию
		// безопасна в любом случае: карточка подтянет свежие evidence.
		recheckCandidate = null;
		autolistStatus.invalidate();
	}

	/**
	 * Действие из футера модалки recheck: тот же обработчик, что на карточке
	 * (addCandidate/dismissCandidate/…), без двойной обёртки candidateAction —
	 * иначе внутренний вызов видит чужой busy и молча отказывает. После
	 * успеха модалка закрывается, при ошибке остаётся открытой.
	 */
	async function recheckModalAction(action: (c: AutolistCandidate) => Promise<boolean>) {
		if (!recheckCandidate) return;
		if (await action(recheckCandidate)) closeRecheck();
	}

	async function candidateAction(id: string, run: () => Promise<void>, ok: () => string, err: () => string): Promise<boolean> {
		if (busyIds.has(id) || actionBusy) return false;
		setBusy(id, true);
		try {
			await run();
			notifications.success(ok());
			autolistStatus.invalidate();
			return true;
		} catch (e) {
			notifications.error(errorMessage(e, err()));
			return false;
		} finally {
			setBusy(id, false);
		}
	}

	const addCandidate = (c: AutolistCandidate) =>
		candidateAction(c.id, () => api.addAutolistCandidate(c.id), () => m.autolist_add_ok(), () => m.autolist_add_err());
	const dismissCandidate = (c: AutolistCandidate) =>
		candidateAction(c.id, () => api.dismissAutolistCandidate(c.id), () => m.autolist_dismiss_ok(), () => m.autolist_dismiss_err());
	const forgetCandidate = (c: AutolistCandidate) =>
		candidateAction(c.id, () => api.forgetAutolistCandidate(c.id), () => m.autolist_forget_ok(), () => m.autolist_forget_err());
	const removeCandidate = (c: AutolistCandidate) =>
		candidateAction(c.id, () => api.removeAutolistCandidate(c.id), () => m.autolist_remove_ok(), () => m.autolist_remove_err());
	const restoreHistoryEntry = (h: AutolistHistoryEntry) =>
		candidateAction(`history:${h.at}:${h.action}`, () => api.restoreAutolistCandidate(historyEntryId(h)), () => m.autolist_restore_ok(), () => m.autolist_restore_err());

	// ── Batch-действия над выбранными кандидатами ────────────────────────

	/**
	 * Общий каркас batch-запуска: гоняет запрос, раскладывает per-ID
	 * исход в уведомление и ошибки карточек. Частичный успех не маскируется:
	 * при смешанном результате показываем и «ok», и список отказов.
	 */
	async function runBatch(
		ids: string[],
		run: (batchIds: string[]) => Promise<{ ok: string[]; failed: AutolistBatchFailure[] }>,
		okText: (count: number) => string,
		partialText: (failed: number, total: number) => string,
		errText: () => string,
	): Promise<void> {
		if (ids.length === 0 || bulkBusy) return;
		bulkBusy = true;
		bulkFailures = [];
		for (const id of ids) setBusy(id, true);
		try {
			const result = await run(ids);
			if (result.failed.length === 0) {
				notifications.success(okText(result.ok.length));
			} else {
				bulkFailures = result.failed;
				if (result.ok.length > 0) {
					notifications.warning(partialText(result.failed.length, ids.length));
				} else {
					notifications.error(partialText(result.failed.length, ids.length));
				}
			}
			// Успешные id уходят из кандидатов после refetch — снимаем их с выбора
			// сразу, не дожидаясь нового статуса.
			if (result.ok.length > 0) {
				const done = new Set(result.ok);
				selectedIds = new Set([...selectedIds].filter((id) => !done.has(id)));
			}
			autolistStatus.invalidate();
		} catch (e) {
			notifications.error(errorMessage(e, errText()));
		} finally {
			for (const id of ids) setBusy(id, false);
			bulkBusy = false;
		}
	}

	const addSelected = () =>
		runBatch(
			[...selectedIds],
			(ids) => api.addAutolistCandidates(ids),
			(count) => m.autolist_bulk_add_ok({ count }),
			(failed, total) => m.autolist_bulk_add_partial({ failed, total }),
			() => m.autolist_bulk_add_err(),
		);

	const hideSelected = () =>
		runBatch(
			[...selectedIds],
			(ids) => api.dismissAutolistCandidates(ids),
			(count) => m.autolist_bulk_hide_ok({ count }),
			(failed, total) => m.autolist_bulk_hide_partial({ failed, total }),
			() => m.autolist_bulk_hide_err(),
		);

	/**
	 * «Отключить навсегда»: домены выбранных кандидатов добавляются в
	 * excludeDomains действующего конфига сателлита (тот же Autolist
	 * ruleset/туннель, отдельного /exclude в 0.9.0 нет), затем карточки
	 * убираются batch-forget. Конфиг и forget — два разных шага: неудача
	 * forget после сохранённого конфига не превращается в «всё упало».
	 */
	const disableForever = async () => {
		const picked = selectedCandidates();
		if (picked.length === 0 || bulkBusy) return;
		if (picked.some((c) => !c.target.domain)) {
			// Конфиг-сторе бэкенда отвергнет IP в excludeDomains целиком:
			// заранее просим убрать IP-кандидатов из выбора.
			notifications.error(m.autolist_bulk_exclude_ip());
			return;
		}
		const domains = picked.map((c) => c.target.domain as string);
		const ids = picked.map((c) => c.id);
		bulkBusy = true;
		bulkFailures = [];
		try {
			const cfg = await api.getAutolistConfig();
			const merged = mergeAutolistExclusions(cfg.excludeDomains, domains);
			if (!merged) {
				notifications.error(m.autolist_bulk_exclude_ip());
				return;
			}
			await api.setAutolistConfig(buildAutolistConfigPayload({ ...autolistConfigDraft(cfg), excludeDomains: merged }));
			// Конфиг уже сохранён: неудача forget — не повод молчать об этом.
			try {
				const forget = await api.forgetAutolistCandidates(ids);
				if (forget.failed.length > 0) {
					bulkFailures = forget.failed;
					notifications.warning(m.autolist_bulk_exclude_forget_err());
				} else {
					notifications.success(m.autolist_bulk_exclude_ok());
				}
			} catch {
				notifications.warning(m.autolist_bulk_exclude_forget_err());
			}
			// Карточки выбранных кандидатов уйдут после refetch; снятие выбора не ждёт его.
			selectedIds = new Set([...selectedIds].filter((id) => !ids.includes(id)));
			autolistStatus.invalidate();
		} catch (e) {
			notifications.error(errorMessage(e, m.autolist_bulk_exclude_err()));
		} finally {
			bulkBusy = false;
		}
	};

	/** Восстанавливаем только точную identity, сохранённую в dismissed history. */
	function historyEntryId(h: AutolistHistoryEntry): string {
		if (h.domain && h.network && h.port) return `domain|${h.domain}|${h.network}|${h.port}`;
		if (!h.domain && !h.host && h.ip) return `ip|${h.ip}`;
		return '';
	}
	function canRestoreHistory(h: AutolistHistoryEntry): boolean {
		return h.action === 'dismissed' && historyEntryId(h) !== '';
	}

	// ── Target helpers ───────────────────────────────────────────────────

	function targetName(t: AutolistCandidate): string {
		return t.target.domain || t.target.host || t.target.ip || t.id;
	}

	function targetSubtitle(t: AutolistCandidate): string {
		const parts: string[] = [];
		if (t.target.host && t.target.host !== t.target.domain) parts.push(t.target.host);
		if (t.target.ip && t.target.ip !== t.target.host) parts.push(t.target.ip);
		parts.push(`${t.target.network.toUpperCase()}/${t.target.port}`);
		return parts.join(' · ');
	}

	function targetText(t?: AutolistCandidate['target']): string {
		if (!t) return '—';
		const name = t.domain || t.host || t.ip || '—';
		return `${name} ${t.network.toUpperCase()}/${t.port}`;
	}

	/** Имя клиента-источника: из hotspot роутера, по MAC, иначе по IP. */
	function clientLabel(ip?: string, mac?: string): string {
		return clientName(policyDevices, ip, mac);
	}

	/** У записи истории цель плоская: домен или IP + протокол/порт. */
	function historyTargetText(h: AutolistHistoryEntry): string {
		const name = h.domain || h.host || h.ip || '—';
		if (h.network && h.port) return `${name} ${h.network.toUpperCase()}/${h.port}`;
		return name;
	}

	// ── Verdicts and labels ──────────────────────────────────────────────

	/** Kernel-имя интерфейса для StatStrip: label, если есть, иначе имя. */
	function interfaceDisplay(name?: string): string {
		if (!name) return '—';
		const iface = interfaces.find((i) => i.name === name);
		if (!iface) return name;
		// Отчёт Stat value ↔ sub: человекочитаемое имя плюс kernel-имя.
		return iface.label && iface.label !== iface.name ? `${iface.label} · ${iface.name}` : iface.name;
	}

	function reasonText(reason: string): string {
		switch (reason) {
			case 'tcp-rst': return m.autolist_reason_tcp_reset();
			case 'tls-no-response': return m.autolist_reason_tls_no_response();
			case 'connect-no-response': return m.autolist_reason_connect_no_response();
			case 'response-stall': return m.autolist_reason_response_stall();
			case 'udp-no-response': return m.autolist_reason_udp_no_response();
			default: return reason;
		}
	}

	function stateText(state: AutolistCandidateState): string {
		switch (state) {
			case 'checking': return m.autolist_state_checking();
			case 'waiting': return m.autolist_state_waiting();
			case 'added': return m.autolist_state_added();
			case 'rejected': return m.autolist_state_rejected();
			default: return state;
		}
	}

	function verdictText(v?: AutolistVerdict | string): string {
		switch (v) {
			case 'not-checked': return m.autolist_verdict_not_checked();
			case 'blocked': return m.autolist_verdict_blocked();
			case 'reachable': return m.autolist_verdict_reachable();
			case 'error': return m.autolist_verdict_error();
			default: return '—';
		}
	}

	/** Статус проверки интернета: «интернет работает» вместо «открывается». */
	function wanVerdictText(v?: AutolistVerdict | string): string {
		switch (v) {
			case 'not-checked': return m.autolist_wan_verdict_not_checked();
			case 'blocked': return m.autolist_wan_verdict_blocked();
			case 'reachable': return m.autolist_wan_verdict_reachable();
			case 'error': return m.autolist_wan_verdict_error();
			default: return '—';
		}
	}

	function verdictClass(v?: AutolistVerdict | string): string {
		switch (v) {
			case 'blocked': return 'text-error';
			case 'reachable': return 'text-success';
			case 'error': return 'text-warning';
			default: return 'opacity-60';
		}
	}

	function coverageText(c: AutolistCandidate): string {
		if (c.coverage?.covered) return m.autolist_coverage_covered();
		if (c.coverage?.excluded) return m.autolist_coverage_excluded();
		return m.autolist_coverage_ok();
	}

	function historyActionText(action: string): string {
		switch (action) {
			case 'added': return m.autolist_history_added();
			case 'removed': return m.autolist_history_removed();
			case 'dismissed': return m.autolist_history_dismissed();
			case 'restored': return m.autolist_history_restored();
			case 'forgotten': return m.autolist_history_forgotten();
			default: return action;
		}
	}

	function historySourceText(source: string): string {
		switch (source) {
			case 'user': return m.autolist_source_user();
			case 'auto': return m.autolist_source_auto();
			default: return source;
		}
	}

	function historyResultText(result?: string): string {
		if (!result) return '—';
		if (result === 'ok' || result.startsWith('ok')) return m.autolist_history_result_ok();
		return result;
	}

	function resolveViaText(resolveVia?: string): string {
		if (!resolveVia) return m.autolist_dns_final();
		return resolveVia.replace(/^sb:/, '');
	}

	/**
	 * Третья строка DNS-стата: транспорт · сервер по данным sing-box.
	 * '' resolveVia — dns.final: показываем effective final (тег/сервер),
	 * когда он разрешается в известный сервер; иначе просто «dns.final».
	 * Транспорт — нижний регистр, как в настройках DNS sing-box.
	 */
	function dnsStatSub(resolveVia?: string): string {
		const tag = resolveVia ? resolveVia.replace(/^sb:/, '') : dnsGlobals?.final ?? '';
		if (!tag) return '';
		const server = dnsServers.find((s) => s.tag === tag);
		if (!server) return '';
		const addr = server.server?.trim();
		if (!addr) return server.tag;
		return `${server.type} · ${addr}`;
	}

	function protocolsText(protocols?: string[]): string {
		return protocols && protocols.length > 0 ? protocols.join(', ') : '—';
	}

	/** Действия, доступные на карточке в текущем состоянии. */
	function canAdd(c: AutolistCandidate): boolean {
		return c.state === 'waiting' || c.state === 'rejected';
	}
	function canRemove(c: AutolistCandidate): boolean {
		return c.state === 'added';
	}

	// ── Settings modal ───────────────────────────────────────────────────

	let settingsOpen = $state(false);
	/** Наборы, не влезшие в потолок памяти: в покрытии они не учитываются. */
	let skippedRuleSets = $state<string[]>([]);
	let autoConfirmOpen = $state(false);
	let busy = $state(false);
	let historyClearConfirmOpen = $state(false);
	let historyClearBusy = $state(false);

	// ── Teardown объектов Autolist (danger, явная кнопка) ────────────────
	let teardownConfirmOpen = $state(false);
	let teardownBusy = $state(false);

	/** Полный снос owner-правила, rule-set и outbound Autolist. */
	async function teardownAutolistObjects() {
		if (teardownBusy) return;
		teardownBusy = true;
		try {
			const res = await api.teardownAutolistObjects();
			if (res?.backupSaved === false) {
				// Снос выполнен, но снимка нет: восстановить будет не из чего.
				notifications.warning(m.autolist_backup_toast_nosnapshot());
			} else {
				notifications.success(m.autolist_teardown_success());
			}
			teardownConfirmOpen = false;
		} catch (e) {
			// 409 от бэкенда («сателлит работает») переводим в понятный текст;
			// request() кладёт HTTP-статус в поле status ошибки.
			const status = e instanceof Error ? (e as Error & { status?: number }).status : undefined;
			notifications.error(
				status === 409 ? m.autolist_teardown_err_running() : errorMessage(e, m.autolist_teardown_err()),
			);
		} finally {
			teardownBusy = false;
		}
	}

	// ── Снимок удалённых объектов: восстановить или начать заново ─────────
	let backupConfirmOpen = $state(false);
	let backupRestoreBusy = $state(false);
	let backupDiscardBusy = $state(false);

	/** Дата снимка для текста модалки; недоступная дата — без неё. */
	let backupCapturedAt = $state<string | undefined>(undefined);
	let backupDateText = $derived(formatAutolistTime(backupCapturedAt));

	async function openBackupDialog() {
		// Дата подтягивается на момент открытия; ошибка чтения не блокирует —
		// в тексте модалки останется «—».
		try {
			const info = await api.getAutolistBackup();
			backupCapturedAt = info.exists ? info.capturedAt : undefined;
		} catch {
			backupCapturedAt = undefined;
		}
		backupConfirmOpen = true;
	}

	/** Восстановить объекты из снимка (draft→apply на бэкенде). */
	async function restoreAutolistBackup() {
		if (backupRestoreBusy || backupDiscardBusy) return;
		backupRestoreBusy = true;
		try {
			await api.restoreAutolistObjects();
			notifications.success(m.autolist_backup_success());
			backupConfirmOpen = false;
			backupCapturedAt = undefined;
			autolistStatus.invalidate();
		} catch (e) {
			const status = e instanceof Error ? (e as Error & { status?: number }).status : undefined;
			notifications.error(
				status === 409
					? running
						? m.autolist_backup_err_running()
						: m.autolist_backup_err_exists()
					: errorMessage(e, m.autolist_backup_err()),
			);
		} finally {
			backupRestoreBusy = false;
		}
	}

	/** Начать заново: снимок удаляется, объекты не восстанавливаются. */
	async function discardAutolistBackup() {
		if (backupRestoreBusy || backupDiscardBusy) return;
		backupDiscardBusy = true;
		try {
			await api.discardAutolistBackup();
			notifications.success(m.autolist_backup_discarded());
			backupConfirmOpen = false;
			backupCapturedAt = undefined;
			autolistStatus.invalidate();
		} catch (e) {
			notifications.error(errorMessage(e, m.autolist_backup_err()));
		} finally {
			backupDiscardBusy = false;
		}
	}

	// Локальная копия конфига для модалки настроек; сохранение и включение
	// autoAdd запрещены, пока чтение не succeed (никакого stale default draft).
	let cfgDraft = $state<AutolistConfig>(defaultAutolistConfig());
	let cfgLoading = $state(false);
	let cfgLoadSucceeded = $state(false);
	let cfgLoadGeneration = 0;

	// Ресурсы для выпадающих списков и StatStrip — читаются один раз.
	let dnsServers = $state<SingboxRouterDNSServer[]>([]);
	let dnsGlobals = $state<SingboxRouterDNSGlobals | null>(null);
	let interfaces = $state<RouterInterface[]>([]);
	let exclusionRuleSets = $state<SingboxRouterRuleSet[]>([]);
	// Каталог туннелей (те же три источника, что у TunnelCatalog сателлита):
	// managed AWG, standalone sing-box и подписки.
	let awgTagInfos = $state<AWGTagInfo[]>([]);
	let singboxTunnels = $state<SingboxTunnel[]>([]);
	let subscriptions = $state<Subscription[]>([]);
	let probeResourcesFetched = $state(false);
	let probeResourcesLoading = $state(false);

	async function fetchProbeResources() {
		if (probeResourcesFetched || probeResourcesLoading) return;
		probeResourcesLoading = true;
		const [dns, globals, ifaces, ruleSets, awgTags, sbTunnels, subs] = await Promise.allSettled([
			api.singboxRouterListDNSServers(),
			api.singboxRouterGetDNSGlobals(),
			api.getAllInterfaces(),
			api.singboxRouterListRuleSets(),
			api.getAWGTags(),
			api.singboxListTunnels(),
			api.listSubscriptions(),
		]);
		if (dns.status === 'fulfilled') dnsServers = dns.value;
		if (globals.status === 'fulfilled') dnsGlobals = globals.value;
		if (ifaces.status === 'fulfilled') interfaces = ifaces.value;
		if (ruleSets.status === 'fulfilled') exclusionRuleSets = ruleSets.value;
		if (awgTags.status === 'fulfilled') awgTagInfos = awgTags.value;
		if (sbTunnels.status === 'fulfilled') singboxTunnels = sbTunnels.value;
		if (subs.status === 'fulfilled') subscriptions = subs.value;
		probeResourcesFetched = true;
		probeResourcesLoading = false;
	}

	// StatStrip показывает человекочитаемое имя туннеля и описание DNS-сервера:
	// ресурсы нужны и на главном экране, не только в настройках. Не ждём их
	// синхронно — статы рисуются сразу с тем, что известно.
	$effect(() => {
		if (running) void fetchProbeResources();
	});

	/**
	 * Список «Интерфейс туннеля»: каталог туннелей (те же три источника, что
	 * у TunnelCatalog сателлита, awgm/catalog.go) плюс WAN из all-interfaces.
	 * Managed AWG-туннели (opkgtun*) вырезаны из all-interfaces (isOwnTunnel),
	 * поэтому приходят только из getAWGTags. Порядок: AWG, standalone
	 * sing-box, подписки, WAN, затем «хвост» текущего targetInterface —
	 * сохранённое значение видно, даже если источник временно недоступен.
	 * Дедуп по kernel-имени интерфейса: первый источник выигрывает, у
	 * дубликата забираем человеческое имя, если у первого его не было.
	 */
	let tunnelInterfaceOptions = $derived.by(() => {
		/** Накопитель по kernel-имени; label — человеческое имя или ''. */
		const byValue = new Map<string, { value: string; label: string; description?: string }>();
		const add = (value: string, human: string, description?: string) => {
			const existing = byValue.get(value);
			if (!existing) byValue.set(value, { value, label: human, description });
			else if (!existing.label && human) existing.label = human;
		};
		const fmt = (value: string, human: string) => (human ? `${human} (${value})` : value);
		// Managed AWG: enabled-поля нет, берём все с непустым iface.
		for (const t of awgTagInfos) {
			if (!t.iface) continue;
			add(t.iface, t.label, m.autolist_cfg_target_kind_awg());
		}
		// Standalone sing-box: интерфейс существует, только пока процесс поднят.
		for (const t of singboxTunnels) {
			if (!t.running || !t.kernelInterface) continue;
			add(t.kernelInterface, t.tag, m.autolist_cfg_target_kind_singbox());
		}
		// Подписки: синтетический интерфейс t2s<proxyIndex>.
		for (const s of subscriptions) {
			if (!s.enabled || s.proxyIndex < 0) continue;
			add(`t2s${s.proxyIndex}`, s.label, m.autolist_cfg_target_kind_subscription());
		}
		// WAN/прочие из all-interfaces: LAN/bridge не предлагаем (private).
		for (const iface of interfaces) {
			if (!iface.up) continue;
			if (iface.securityLevel && iface.securityLevel !== 'public') continue;
			add(iface.name, iface.label, m.autolist_cfg_target_kind_wan());
		}
		const opts = [...byValue.values()].map((o) => ({ ...o, label: fmt(o.value, o.label) }));
		const current = cfgDraft.targetInterface ?? '';
		if (current && !byValue.has(current)) {
			opts.push({ value: current, label: current });
		}
		return opts;
	});

	let dnsOptions = $derived.by(() => {
		const finalTag = dnsGlobals?.final ?? '';
		const opts: { value: string; label: string; description?: string; disabled?: boolean }[] = [];
		if (finalTag) {
			opts.push({ value: '', label: m.autolist_dns_final(), description: finalTag });
		}
		for (const server of dnsServers) {
			const supportedTransport = server.type === 'udp' || server.type === 'https' || server.type === 'tls';
			const disabled = !supportedTransport;
			const description = !supportedTransport
				? m.autolist_dns_unsupported()
				: server.detour
					? m.autolist_dns_detour()
					: `${server.type.toUpperCase()} · ${server.server}`;
			opts.push({ value: `sb:${server.tag}`, label: server.tag, description, disabled });
		}
		const current = cfgDraft.resolveVia ?? '';
		if (current && !opts.some((o) => o.value === current)) {
			opts.push({ value: current, label: current });
		}
		return opts;
	});

	let exclusionRuleSetOptions = $derived(exclusionRuleSets.map((rs) => ({ value: rs.tag, label: rs.tag })));

	/** AutoAdd доступен только при включённой tunnel-пробе. */
	let autoAddAvailable = $derived((cfgDraft.tunnelProbe ?? false) === true);

	async function openSettings() {
		const generation = ++cfgLoadGeneration;
		settingsOpen = true;
		cfgLoading = true;
		cfgLoadSucceeded = false;
		skippedRuleSets = [];
		void fetchProbeResources();
		try {
			const cfgLoaded = await api.getAutolistConfig();
			if (generation !== cfgLoadGeneration) return;
			cfgDraft = autolistConfigDraft(cfgLoaded);
			cfgLoadSucceeded = true;
			void fetchSkippedRuleSets(cfgDraft.excludeRuleSets ?? []);
		} catch {
			if (generation === cfgLoadGeneration) notifications.error(m.autolist_cfg_read_err());
		} finally {
			if (generation === cfgLoadGeneration) cfgLoading = false;
		}
	}

	/**
	 * Наборы, не влезшие в потолок памяти: панель их не учитывает в покрытии.
	 */
	async function fetchSkippedRuleSets(tags: string[]) {
		if (tags.length === 0) {
			skippedRuleSets = [];
			return;
		}
		try {
			const snapshot = await api.singboxRouterIndexedSnapshot(tags);
			skippedRuleSets = snapshot.skipped_rule_sets ?? [];
		} catch {
			skippedRuleSets = [];
		}
	}

	function setExcludedDomains(raw: string) {
		const domains = raw
			.split(/[\s,]+/)
			.map((d) => d.trim().toLowerCase().replace(/\.$/, ''))
			.filter((d) => d !== '');
		cfgDraft = { ...cfgDraft, excludeDomains: [...new Set(domains)] };
	}

	/**
	 * Выключение tunnel-пробы сразу гасит autoAdd в черновике: бэкенд
	 * сохранит autoAdd=false той же записью. Повторное включение tunnel
	 * автоматику не возвращает — нужно отдельное действие.
	 */
	function setTunnelProbe(value: boolean) {
		cfgDraft = value
			? { ...cfgDraft, tunnelProbe: true }
			: { ...cfgDraft, tunnelProbe: false, autoAdd: false };
	}

	function toggleAuto() {
		if (!cfgLoadSucceeded || !autoAddAvailable) return;
		if (!cfgDraft.autoAdd) {
			// включаем — сначала предупреждение
			autoConfirmOpen = true;
		} else {
			cfgDraft = { ...cfgDraft, autoAdd: false };
		}
	}

	async function confirmAuto() {
		if (!cfgLoadSucceeded || !autoAddAvailable || busy) return;
		autoConfirmOpen = false;
		cfgDraft = { ...cfgDraft, autoAdd: true };
		await saveConfig();
	}

	async function saveConfig() {
		if (!cfgLoadSucceeded || busy) return;
		const generation = cfgLoadGeneration;
		const draftAtSave = cfgDraft;
		busy = true;
		try {
			await api.setAutolistConfig(buildAutolistConfigPayload(draftAtSave));
			notifications.success(m.autolist_cfg_saved());
			if (generation === cfgLoadGeneration && cfgDraft === draftAtSave) settingsOpen = false;
			autolistStatus.invalidate();
			void fetchSkippedRuleSets(draftAtSave.excludeRuleSets ?? []);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.autolist_cfg_save_err());
		} finally {
			busy = false;
		}
	}

	// ── Clearing the decision journal ────────────────────────────────────

	/**
	 * Очистка журнала истории: только история, кандидаты/правила/бюджет
	 * не трогаются. Отмена просто закрывает подтверждение.
	 */
	async function clearHistory() {
		if (historyClearBusy) return;
		historyClearBusy = true;
		try {
			await api.clearAutolistHistory();
			notifications.success(m.autolist_history_clear_ok());
			historyClearConfirmOpen = false;
			autolistStatus.invalidate();
		} catch (e) {
			notifications.error(errorMessage(e, m.autolist_history_clear_err()));
		} finally {
			historyClearBusy = false;
		}
	}

	/**
	 * Добавлений в час: поле Input отдаёт СТРОКУ при любом типе. Конвертация
	 * безопасная: пустое/нечисловое значение оставляет прежний черновик,
	 * вне 1..500 — тоже (validate в payload вернёт дефолт).
	 */
	function setAddsPerHour(raw: string) {
		const parsed = Number(raw);
		if (raw.trim() === '' || !Number.isInteger(parsed)) return;
		cfgDraft = { ...cfgDraft, addsPerHour: parsed };
	}
</script>

{#snippet candidateCard(c: AutolistCandidate)}
	{@const busy = busyIds.has(c.id)}
	<div class="card p-4 flex flex-col gap-2">
		<div class="flex items-center gap-3 flex-wrap">
			{#if c.state === 'waiting' || c.state === 'checking'}
				<input
					type="checkbox"
					checked={selectedIds.has(c.id)}
					onchange={() => toggleSelected(c.id)}
					disabled={bulkBusy}
					aria-label={targetName(c)}
					title={targetName(c)}
				/>
			{/if}
			<div class="min-w-0 grow">
				<div class="font-mono text-sm font-medium break-all">{targetName(c)}</div>
				<div class="text-xs opacity-60 break-all">{targetSubtitle(c)}</div>
			</div>
			<span class="badge badge-sm" class:badge-success={c.state === 'added'} class:badge-warning={c.state === 'checking' || c.state === 'waiting'}>
				{busy ? m.autolist_state_checking() : stateText(c.state)}
			</span>
			<div class="flex items-center gap-1">
				{#if canAdd(c)}
					<IconButton
						size="sm"
						variant="default"
						ariaLabel={m.autolist_action_add()}
						title={m.autolist_action_add_hint()}
						loading={busy}
						disabled={busy}
						onclick={() => void addCandidate(c)}
					>
						<ShieldPlus size={14} strokeWidth={2} aria-hidden="true" />
					</IconButton>
				{/if}
				{#if canRemove(c)}
					<IconButton
						size="sm"
						variant="default"
						ariaLabel={m.autolist_action_remove()}
						title={m.autolist_action_remove_hint()}
						loading={busy}
						disabled={busy}
						onclick={() => void removeCandidate(c)}
					>
						<Trash2 size={14} strokeWidth={2} aria-hidden="true" />
					</IconButton>
				{/if}
				{#if c.state !== 'added'}
					<IconButton
						size="sm"
						variant="default"
						ariaLabel={m.autolist_action_dismiss()}
						title={m.autolist_action_dismiss_hint()}
						disabled={busy}
						onclick={() => void dismissCandidate(c)}
					>
						<EyeOff size={14} strokeWidth={2} aria-hidden="true" />
					</IconButton>
				{/if}
				{#if c.state !== 'added'}
					<IconButton
						size="sm"
						variant="default"
						ariaLabel={m.autolist_action_forget()}
						title={m.autolist_action_forget_hint()}
						disabled={busy}
						onclick={() => void forgetCandidate(c)}
					>
						<CircleX size={14} strokeWidth={2} aria-hidden="true" />
					</IconButton>
				{/if}
				<!-- Повторная проверка доступна в любом состоянии карточки:
				     и у кандидата, и у добавленной записи — карточка одна. -->
				<IconButton
					size="sm"
					variant="default"
					ariaLabel={m.autolist_recheck_again()}
					title={m.autolist_recheck_again()}
					disabled={busy}
					onclick={() => openRecheck(c)}
				>
					<RefreshCw size={14} strokeWidth={2} aria-hidden="true" />
				</IconButton>
			</div>
		</div>

		<!-- Ровно три колонки по два значения: Reason/WAN, Direct/Tunnel, First seen/Last seen. -->
		<div class="autolist-evidence text-xs">
			<div class="flex flex-col gap-1">
				<span>
					<span class="opacity-60">{m.autolist_col_reason()}:</span>
					{reasonText(c.reason)}
				</span>
				<span>
					<span class="opacity-60">{m.autolist_col_wan()}:</span>
					<span class={verdictClass(c.wan?.verdict)}>{wanVerdictText(c.wan?.verdict)}</span>
					<span class="opacity-60">{formatAutolistDuration(c.wan?.durationMs)}</span>
				</span>
			</div>
			<div class="flex flex-col gap-1">
				<span>
					<span class="opacity-60">{m.autolist_col_direct()}:</span>
					<span class={verdictClass(c.direct?.verdict)}>{verdictText(c.direct?.verdict)}</span>
					<span class="opacity-60">{formatAutolistDuration(c.direct?.durationMs)}</span>
				</span>
				<span>
					<span class="opacity-60">{m.autolist_col_tunnel()}:</span>
					<span class={verdictClass(c.tunnel?.verdict)}>{verdictText(c.tunnel?.verdict)}</span>
					<span class="opacity-60">{formatAutolistDuration(c.tunnel?.durationMs)}</span>
				</span>
			</div>
			<div class="flex flex-col gap-1">
				<span class="opacity-60">{m.autolist_col_first_seen()}: {formatAutolistTime(c.firstAt)}</span>
				<span class="opacity-60">{m.autolist_col_last_seen()}: {formatAutolistTime(c.lastAt)}</span>
			</div>
		</div>

		{@render candidateDetails(c, true)}
	</div>
{/snippet}

<!-- Детальная информация по проверке: карточка кандидата и модалка повторной проверки. -->
{#snippet candidateDetails(c: AutolistCandidate, withLastError: boolean)}
	<details class="autolist-details text-xs">
		<summary>{m.autolist_details()}</summary>
		<div class="flex flex-col gap-1.5 mt-2">
			<div class="grid grid-cols-2 gap-x-4 gap-y-1">
				<span>
					<span class="opacity-60">{m.autolist_details_coverage()}:</span>
					{coverageText(c)}
					<span class="opacity-60">{formatAutolistDuration(c.coverage?.durationMs)}</span>
				</span>
				<span>
					<span class="opacity-60">{m.autolist_details_interface()}:</span>
					<span class="font-mono">{c.targetInterface || '—'} → {c.targetOutbound || '—'}</span>
				</span>
				{#if c.clientIP || c.clientMAC}
					<span>
						<span class="opacity-60">{m.autolist_details_client()}:</span>
						{#if clientLabel(c.clientIP, c.clientMAC)}
							<span>{clientLabel(c.clientIP, c.clientMAC)} · </span>
						{/if}
						<span class="font-mono">{[c.clientIP, c.clientMAC].filter(Boolean).join(' · ')}</span>
					</span>
				{/if}
				{#if c.correlationID}
					<span>
						<span class="opacity-60">{m.autolist_details_correlation()}:</span>
						<span class="font-mono">{c.correlationID}</span>
					</span>
				{/if}
			</div>
			{#each evFields(c) as { key, e } (key)}
				<div class="border-t border-base-200 pt-1.5">
					<div class="font-medium">
						{key === 'direct' ? m.autolist_col_direct() : key === 'wan' ? m.autolist_col_wan() : m.autolist_col_tunnel()}:
						<span class={verdictClass(e?.verdict)}>{key === 'wan' ? wanVerdictText(e?.verdict) : verdictText(e?.verdict)}</span>
						<span class="opacity-60">{formatAutolistDuration(e?.durationMs)} · {formatAutolistBytes(e?.bytes)}</span>
					</div>
					{#if e?.detail}
						<pre class="autolist-raw">{e.detail}</pre>
					{/if}
				</div>
			{/each}
			{#if withLastError && c.lastError}
				<div class="text-error break-all">
					<span class="opacity-60">{m.autolist_last_error()}:</span>
					{c.lastError}
				</div>
			{/if}
		</div>
	</details>
{/snippet}

<PageHeader title={m.autolist_tab_title()} />
<div class="flex flex-col gap-4">
	<div class="flex items-center gap-4 flex-wrap">
		<p class="autolist-intro text-sm opacity-70 max-w-3xl">{m.autolist_tab_desc()}</p>
		{#if running}
			<div class="autolist-controls flex items-center gap-2 flex-wrap justify-end">
				<span class="flex items-center gap-1.5 text-sm mr-2">
					<CircleCheck size={16} class="text-success" aria-hidden="true" />
					{m.autolist_running()}
					{#if status?.version}
						<span class="text-xs opacity-60">· {m.autolist_version()} {status.version}</span>
					{/if}
				</span>
				<Button variant="primary" size="sm" onclick={() => void setSatelliteEnabled(false)} loading={lifecycleBusy}>
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

	{#if status?.installed && status.updateAvailable}
		<div class="card p-4 flex items-center gap-3 flex-wrap">
			<CircleAlert size={18} class="text-warning" aria-hidden="true" />
			<span class="text-sm flex-1">{m.autolist_update_available({ version: status.packageVersion ?? '' })}</span>
			<Button variant="primary" size="sm" onclick={() => void installSatellite(true)} loading={lifecycleBusy}>
				{m.autolist_update()}
			</Button>
		</div>
	{/if}

	{#if $settings?.authEnabled && !$settings?.apiKey}
		<div class="card p-4 flex items-center gap-3 flex-wrap">
			<TriangleAlert size={18} class="text-warning" aria-hidden="true" />
			<span class="text-sm flex-1">{m.autolist_need_api_key()}</span>
		</div>
	{/if}

	{#if loading}
		<div class="card p-6 text-sm opacity-60">…</div>
	{:else if !running}
		<div class="card p-6 flex flex-col gap-3">
			{#if !status?.installed}
				<div class="flex items-center gap-2 font-medium">
					<CircleAlert size={18} class="text-warning" aria-hidden="true" />
					{m.autolist_not_installed()}
				</div>
				<p class="text-sm opacity-70">{m.autolist_not_installed_hint()}</p>
				<div>
					<Button variant="primary" size="sm" onclick={() => void installSatellite()} loading={lifecycleBusy}>
						{m.autolist_install()}
					</Button>
				</div>
			{:else}
				<div class="flex items-center gap-2 font-medium">
					<CircleAlert size={18} class="text-warning" aria-hidden="true" />
					{status?.enabled === false ? m.autolist_disabled() : m.autolist_not_running()}
				</div>
				<p class="text-sm opacity-70 break-all">
					{status?.enabled === false ? m.autolist_disabled_hint() : m.autolist_not_running_hint()}
				</p>
				<div>
					<div class="flex items-center gap-2 flex-wrap">
						<Button variant="primary" size="sm" onclick={() => void setSatelliteEnabled(true)} loading={lifecycleBusy}>
							{#snippet iconBefore()}<Power size={14} strokeWidth={2} aria-hidden="true" />{/snippet}
							{status?.enabled === false ? m.autolist_enable() : m.common_start()}
						</Button>
						<!-- Объекты Autolist остаются в конфиге и после остановки: снос —
						     только явной кнопкой, поэтому она нужна и в stopped-карточке. -->
						<Button variant="outline-danger" size="sm" onclick={() => (teardownConfirmOpen = true)} disabled={teardownBusy}>
							{#snippet iconBefore()}<Trash2 size={14} strokeWidth={2} aria-hidden="true" />{/snippet}
							{m.autolist_teardown_button()}
						</Button>
						<!-- Есть снимок последнего сноса: вернуть объекты или начать заново. -->
						{#if status?.hasRemovedBackup}
							<Button variant="outline-primary" size="sm" onclick={() => void openBackupDialog()}>
								{#snippet iconBefore()}<ArchiveRestore size={14} strokeWidth={2} aria-hidden="true" />{/snippet}
								{m.autolist_backup_button()}
							</Button>
						{/if}
					</div>
				</div>
			{/if}
		</div>
	{:else}
		<!-- Главный экран: состояние мониторинга и бюджет -->
		<StatStrip>
			<Stat
				value={capturePaused ? m.autolist_capture_paused() : m.autolist_capture_active()}
				label={m.autolist_monitor()}
				sub={m.autolist_protocols() + ': ' + protocolsText(status?.protocols)}
			/>
			<Stat
				value={interfaceDisplay(status?.targetInterface)}
				label={m.autolist_target()}
				sub={status?.targetOutbound ? `→ ${status.targetOutbound}` : undefined}
			/>
			<Stat
				value={resolveViaText(status?.resolveVia)}
				label={m.autolist_dns()}
				sub={dnsStatSub(status?.resolveVia) || undefined}
			/>
			<Stat
				value={`${status?.activeChecks ?? 0}`}
				label={m.autolist_active_checks()}
				sub={`${m.autolist_col_state()}: ${m.autolist_queued_checks()} ${status?.queuedChecks ?? 0}`}
			/>
			<Stat
				value={`${status?.addsUsedHour ?? 0}${(status?.addsLimit ?? 0) > 0 ? ` ${m.autolist_adds_limit({ limit: status?.addsLimit ?? 0 })}` : ''}`}
				label={m.autolist_adds_hour()}
			/>
			<Stat
				value={wanVerdictText(status?.lastWAN?.verdict)}
				label={m.autolist_last_wan()}
				sub={formatAutolistTime(status?.lastWAN?.checkedAt)}
			/>
		</StatStrip>

		<div class="flex flex-wrap items-center gap-x-6 gap-y-1 text-sm opacity-80">
			{#if addLimitExhausted}
				<span class="badge badge-warning badge-sm gap-1">
					<TriangleAlert size={13} aria-hidden="true" />
					{m.autolist_limit_adds_exhausted({ limit: status?.addsLimit ?? 0 })}
				</span>
			{/if}
		</div>

		<Tabs tabs={viewTabs} active={view} onchange={(id) => (view = id as 'candidates' | 'added' | 'history')} />

		{#if view === 'candidates'}
			{#if bulkFailures.length > 0}
				<div class="card p-4 flex flex-col gap-3 text-error" role="alert">
					{#each bulkFailures as failure (failure.id)}
						<div class="min-w-0">
							<div class="font-mono text-sm font-medium break-all">{failure.id}</div>
							<details class="autolist-details">
								<summary>{m.autolist_error_details()}</summary>
								<pre class="autolist-raw">{failure.error}</pre>
							</details>
						</div>
					{/each}
				</div>
			{/if}
			{#if waitingCandidates.length === 0}
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
						<input type="checkbox" checked={allSelected} onchange={toggleAllSelected} disabled={bulkBusy} />
						{m.autolist_select_all()}
					</label>
					<span class="text-xs opacity-50">{m.autolist_selected({ count: selectedCount })}</span>
					<div class="grow"></div>
					<Button
						variant="primary"
						size="sm"
						onclick={() => void addSelected()}
						disabled={selectedCount === 0 || bulkBusy}
						loading={bulkBusy}
					>
						{#snippet iconBefore()}<ShieldPlus size={14} strokeWidth={2} aria-hidden="true" />{/snippet}
						{m.autolist_add_selected()}
					</Button>
					<Button
						variant="secondary"
						size="sm"
						onclick={() => void hideSelected()}
						disabled={selectedCount === 0 || bulkBusy}
					>
						{#snippet iconBefore()}<EyeOff size={14} strokeWidth={2} aria-hidden="true" />{/snippet}
						{m.autolist_hide_selected()}
					</Button>
					<Button
						variant="secondary"
						size="sm"
						onclick={() => void disableForever()}
						disabled={selectedCount === 0 || bulkBusy}
					>
						{#snippet iconBefore()}<CircleX size={14} strokeWidth={2} aria-hidden="true" />{/snippet}
						{m.autolist_disable_forever()}
					</Button>
				</div>
				<div class="flex flex-col gap-2">
					{#each waitingCandidates as c (c.id)}
						{@render candidateCard(c)}
					{/each}
				</div>
			{/if}
		{:else if view === 'added'}
			{#if addedCandidates.length === 0}
				<div class="card p-6 flex flex-col gap-2">
					<div class="flex items-center gap-2 font-medium">
						<CircleDashed size={18} aria-hidden="true" />
						{m.autolist_empty()}
					</div>
					<p class="text-sm opacity-70">{m.autolist_empty_hint()}</p>
				</div>
			{:else}
				<div class="flex flex-col gap-2">
					{#each addedCandidates as c (c.id)}
						{@render candidateCard(c)}
					{/each}
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
								<th class="px-4 py-2.5 font-medium">{m.autolist_history_at()}</th>
								<th class="px-4 py-2.5 font-medium">{m.autolist_col_target()}</th>
								<th class="px-4 py-2.5 font-medium">{m.autolist_details_client()}</th>
								<th class="px-4 py-2.5 font-medium">{m.autolist_col_action()}</th>
								<th class="px-4 py-2.5 font-medium">{m.autolist_col_source()}</th>
								<th class="px-4 py-2.5 font-medium">{m.autolist_details_interface()}</th>
								<th class="px-4 py-2.5 font-medium">{m.autolist_col_result()}</th>
								<th class="px-4 py-2.5"></th>
							</tr>
						</thead>
						<tbody>
							{#each history as h (`${h.at}:${h.action}:${h.domain ?? h.ip ?? ''}`)}
								<tr class="border-b border-base-200 last:border-0 align-top">
									<td class="px-4 py-2.5 opacity-70 whitespace-nowrap">{formatAutolistTime(h.at)}</td>
									<td class="px-4 py-2.5 font-mono">
										{historyTargetText(h)}
										{#if h.host && h.host !== h.domain}
											<div class="text-xs opacity-50">{h.host}</div>
										{/if}
									</td>
									<td class="px-4 py-2.5">
										{#if clientLabel(h.clientIP, h.clientMAC)}
											<div>{clientLabel(h.clientIP, h.clientMAC)}</div>
											<div class="text-xs opacity-50 font-mono">{[h.clientIP, h.clientMAC].filter(Boolean).join(' · ')}</div>
										{:else if h.clientIP || h.clientMAC}
											<div class="font-mono text-xs">{[h.clientIP, h.clientMAC].filter(Boolean).join(' · ')}</div>
										{:else}
											—
										{/if}
									</td>
									<td class="px-4 py-2.5">
										<span class="badge badge-sm" class:badge-success={h.action === 'added'} class:badge-ghost={h.action === 'removed' || h.action === 'dismissed'}>
											{historyActionText(h.action)}
										</span>
									</td>
									<td class="px-4 py-2.5 opacity-70">{historySourceText(h.source)}</td>
									<td class="px-4 py-2.5 font-mono text-xs">{h.interface || '—'} → {h.outbound || '—'}</td>
									<td class="px-4 py-2.5 text-xs">
										{#if h.result}
											{#if h.result === 'ok' || h.result.startsWith('ok')}
												<span class="text-success">{historyResultText(h.result)}</span>
											{:else}
												<span class="text-error">{m.autolist_history_result_error()}</span>
												<details class="autolist-details">
													<summary class="opacity-60">{m.autolist_details()}</summary>
													<pre class="autolist-raw">{h.result}</pre>
												</details>
											{/if}
										{:else}
											—
										{/if}
									</td>
									<td class="px-4 py-2.5 text-right">
										{#if canRestoreHistory(h)}
											<IconButton
												size="sm"
												variant="default"
												ariaLabel={m.autolist_action_restore()}
												title={m.autolist_action_restore_hint()}
												disabled={actionBusy}
												onclick={() => void restoreHistoryEntry(h)}
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

		{#if status?.capturePaused}
			<p class="text-xs opacity-60">{m.autolist_capture_paused_hint()}</p>
		{/if}
		{#if status?.lastError}
			<div class="text-xs text-error break-all">
				{m.autolist_last_error()}: {status.lastError}
			</div>
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
						<span class="text-sm font-medium">{m.autolist_cfg_target()}</span>
						<Dropdown
							value={cfgDraft.targetInterface || ''}
							options={tunnelInterfaceOptions}
							placeholder={m.autolist_cfg_target_placeholder()}
							onchange={(v) => (cfgDraft = { ...cfgDraft, targetInterface: v })}
							fullWidth
						/>
						<span class="text-xs opacity-60">{m.autolist_cfg_target_hint()}</span>
					</div>

					<div class="flex flex-col gap-1.5">
						<span class="text-sm font-medium">{m.autolist_cfg_dns()}</span>
						<Dropdown
							value={cfgDraft.resolveVia || ''}
							options={dnsOptions}
							onchange={(v) => (cfgDraft = { ...cfgDraft, resolveVia: v })}
							fullWidth
						/>
						<span class="text-xs opacity-60">{m.autolist_cfg_dns_hint()}</span>
					</div>

					<div class="flex flex-col gap-1.5">
						<Input
							type="number"
							label={m.autolist_cfg_adds_per_hour()}
							value={`${cfgDraft.addsPerHour ?? AUTOLIST_DEFAULT_ADDS_PER_HOUR}`}
							min={`${1}`}
							max={`${AUTOLIST_MAX_ADDS_PER_HOUR}`}
							oninput={setAddsPerHour}
							hint={m.autolist_cfg_adds_per_hour_hint()}
							fullWidth
						/>
					</div>

					<div class="flex flex-col gap-1.5">
						<Input
							type="url"
							label={m.autolist_cfg_wan_url()}
							value={cfgDraft.wanCheckUrl ?? AUTOLIST_DEFAULT_WAN_URL}
							oninput={(v) => (cfgDraft = { ...cfgDraft, wanCheckUrl: v })}
							hint={m.autolist_cfg_wan_url_hint()}
							fullWidth
						/>
					</div>

					<div class="flex items-start justify-between gap-3">
						<span class="flex flex-col gap-1">
							<span class="text-sm font-medium">{m.autolist_cfg_wan_monitor()}</span>
							<span class="text-xs opacity-70">{m.autolist_cfg_wan_monitor_hint()}</span>
						</span>
						<Toggle
							checked={cfgDraft.wanMonitorEnabled ?? false}
							controlled
							onchange={(v) => (cfgDraft = { ...cfgDraft, wanMonitorEnabled: v })}
							ariaLabel={m.autolist_cfg_wan_monitor()}
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
							onchange={setTunnelProbe}
							ariaLabel={m.autolist_cfg_tunnel_probe()}
						/>
					</div>

					<div class="flex items-start justify-between gap-3">
						<span class="flex flex-col gap-1">
							<span class="text-sm font-medium">{m.autolist_cfg_udp()}</span>
							<span class="text-xs opacity-70">{m.autolist_cfg_udp_hint()}</span>
						</span>
						<Toggle
							checked={cfgDraft.udpEnabled ?? false}
							controlled
							onchange={(v) => (cfgDraft = { ...cfgDraft, udpEnabled: v })}
							ariaLabel={m.autolist_cfg_udp()}
						/>
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
						{#if skippedRuleSets.length > 0}
							<div class="autolist-warning rounded-lg bg-warning/10 border border-warning/30 px-3 py-2 text-xs">
								{m.autolist_cfg_exclude_rulesets_too_large()}
								<span class="opacity-70">{skippedRuleSets.join(', ')}</span>
							</div>
						{/if}
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

			<!-- Опасная зона: очистка журнала. Только история, не кандидаты/правила/бюджет. -->
			<div class="autolist-warning flex items-start justify-between gap-3 rounded-lg bg-error/10 border border-error/30">
				<span class="flex flex-col gap-1">
					<span class="text-sm font-medium flex items-center gap-1.5">
						<Trash2 size={15} class="text-error" aria-hidden="true" />
						{m.autolist_history_clear()}
					</span>
					<span class="text-xs opacity-70">{m.autolist_history_clear_hint()}</span>
				</span>
				<Button variant="danger" size="sm" onclick={() => (historyClearConfirmOpen = true)} disabled={historyClearBusy}>
					{m.autolist_history_clear()}
				</Button>
			</div>
		{/if}
	</div>
	{#snippet actions()}
		<Button variant="ghost" size="md" onclick={() => (settingsOpen = false)}>{m.common_cancel()}</Button>
		<Button variant="primary" size="md" onclick={saveConfig} disabled={cfgLoading || !cfgLoadSucceeded || busy} loading={busy}>
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
		<Button variant="primary" size="md" onclick={confirmAuto} disabled={!cfgLoadSucceeded || !autoAddAvailable || busy} loading={busy}>{m.autolist_auto_confirm_yes()}</Button>
	{/snippet}
</Modal>

<!-- Подтверждение очистки журнала -->
<Modal open={historyClearConfirmOpen} onclose={() => (historyClearConfirmOpen = false)} title={m.autolist_history_clear_confirm_title()} size="sm">
	<div class="autolist-warning flex items-start gap-2.5 rounded-lg bg-error/10 border border-error/30">
		<TriangleAlert size={18} class="text-error shrink-0 mt-0.5" aria-hidden="true" />
		<p class="text-sm">{m.autolist_history_clear_confirm_text()}</p>
	</div>
	{#snippet actions()}
		<Button variant="ghost" size="md" onclick={() => (historyClearConfirmOpen = false)}>{m.common_cancel()}</Button>
		<Button variant="danger" size="md" onclick={() => void clearHistory()} disabled={historyClearBusy} loading={historyClearBusy}>
			{m.autolist_history_clear_confirm_yes()}
		</Button>
	{/snippet}
</Modal>

<!-- Подтверждение полного сноса объектов Autolist -->
<Modal open={teardownConfirmOpen} onclose={() => (teardownConfirmOpen = false)} title={m.autolist_teardown_title()} size="sm">
	<div class="autolist-warning flex items-start gap-2.5 rounded-lg bg-error/10 border border-error/30">
		<Trash2 size={18} class="text-error shrink-0 mt-0.5" aria-hidden="true" />
		<p class="text-sm">{m.autolist_teardown_description()}</p>
	</div>
	{#snippet actions()}
		<Button variant="ghost" size="md" onclick={() => (teardownConfirmOpen = false)}>{m.common_cancel()}</Button>
		<Button variant="danger" size="md" onclick={() => void teardownAutolistObjects()} disabled={teardownBusy} loading={teardownBusy}>
			{m.autolist_teardown_confirm()}
		</Button>
	{/snippet}
</Modal>

<!-- Снимок удалённых объектов: восстановить или начать заново -->
<Modal open={backupConfirmOpen} onclose={() => (backupConfirmOpen = false)} title={m.autolist_backup_title()} size="sm">
	<div class="flex flex-col gap-3">
		<div class="autolist-warning flex items-start gap-2.5 rounded-lg bg-warning/10 border border-warning/30">
			<ArchiveRestore size={18} class="text-warning shrink-0 mt-0.5" aria-hidden="true" />
			<p class="text-sm">{m.autolist_backup_description({ date: backupDateText })}</p>
		</div>
	</div>
	{#snippet actions()}
		<Button
			variant="ghost"
			size="md"
			onclick={() => void discardAutolistBackup()}
			disabled={backupRestoreBusy || backupDiscardBusy}
			loading={backupDiscardBusy}
		>
			{m.autolist_backup_discard()}
		</Button>
		<Button
			variant="primary"
			size="md"
			onclick={() => void restoreAutolistBackup()}
			disabled={backupRestoreBusy || backupDiscardBusy}
			loading={backupRestoreBusy}
		>
			{backupRestoreBusy ? m.autolist_backup_busy() : m.autolist_backup_restore()}
		</Button>
	{/snippet}
</Modal>

<!-- Повторная проверка кандидата: действия карточки продублированы в футере. -->
{#snippet recheckActions()}
	{#if recheckCandidate}
		{#if canAdd(recheckCandidate)}
			<Button variant="secondary" size="md" onclick={() => void recheckModalAction(addCandidate)}>
				{#snippet iconBefore()}
					<ShieldPlus size={14} strokeWidth={2} aria-hidden="true" />
				{/snippet}
				{m.autolist_action_add()}
			</Button>
		{/if}
		{#if canRemove(recheckCandidate)}
			<Button variant="secondary" size="md" onclick={() => void recheckModalAction(removeCandidate)}>
				{#snippet iconBefore()}
					<Trash2 size={14} strokeWidth={2} aria-hidden="true" />
				{/snippet}
				{m.autolist_action_remove()}
			</Button>
		{/if}
		{#if recheckCandidate.state !== 'added'}
			<Button variant="ghost" size="md" onclick={() => void recheckModalAction(dismissCandidate)}>
				{#snippet iconBefore()}
					<EyeOff size={14} strokeWidth={2} aria-hidden="true" />
				{/snippet}
				{m.autolist_action_dismiss()}
			</Button>
		{/if}
		{#if recheckCandidate.state !== 'added'}
			<Button variant="ghost" size="md" onclick={() => void recheckModalAction(forgetCandidate)}>
				{#snippet iconBefore()}
					<CircleX size={14} strokeWidth={2} aria-hidden="true" />
				{/snippet}
				{m.autolist_action_forget()}
			</Button>
		{/if}
	{/if}
{/snippet}
<RecheckModal
	open={recheckCandidate !== null}
	candidate={recheckCandidate}
	onclose={closeRecheck}
	actions={recheckActions}
	details={candidateDetails}
/>

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
	.autolist-details summary {
		cursor: pointer;
		font-size: 0.75rem;
	}
	/* Ровно три колонки доказательств: Reason/WAN · Direct/Tunnel ·
	   First seen/Last seen. На узких экранах колонки складываются в столбец. */
	.autolist-evidence {
		display: grid;
		grid-template-columns: repeat(3, minmax(0, 1fr));
		gap: 0.25rem 1rem;
	}
	@media (max-width: 640px) {
		.autolist-evidence {
			grid-template-columns: minmax(0, 1fr);
		}
	}
	.autolist-details pre,
	.autolist-raw {
		margin: 0.35rem 0 0;
		white-space: pre-wrap;
		overflow-wrap: anywhere;
		word-break: break-word;
		font-size: 0.75rem;
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
