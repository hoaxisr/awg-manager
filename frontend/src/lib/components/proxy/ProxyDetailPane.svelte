<script lang="ts">
	import { notifications } from '$lib/stores/notifications';
	import { listenPortNumber } from '$lib/utils/listenPortUtils';
	import { proxyInOpsMode } from '$lib/utils/proxyOpsMode';
	import { errText } from '$lib/utils/errorMessage';
	import type {
		AccessPolicy,
		FreeTurnConfig,
		FreeTurnStatus,
		TunnelListItem,
		WdttConfig,
		WdttStatus,
	} from '$lib/types';
	import ExitDetail from './ExitDetail.svelte';
	import ExitWizard from './ExitWizard.svelte';
	import ShareDetail from './ShareDetail.svelte';
	import ShareWizard from './ShareWizard.svelte';
	import { exitInstance, saveExitInstance } from './exitConfig';
	import { shareInstance, saveShareInstance } from './shareConfig';
	import { reportDeletedTunnels } from './deleteNotice';
	import type { ExitConfig } from './exitConfig';
	import type { ExitProtocol } from './exitWizard';
	import type { ShareConfig } from './shareConfig';
	import type { ProxyInstanceRow } from './rows';

	interface Props {
		activeTab: 'exit' | 'share';
		/** Выбранная строка вкладки и её ключ — ключ живёт у страницы. */
		exitRow: ProxyInstanceRow | null;
		exitKey: string | null;
		shareRow: ProxyInstanceRow | null;
		shareKey: string | null;
		wdttConfig: WdttConfig | null;
		ftConfig: FreeTurnConfig | null;
		wdttStatus: WdttStatus | null;
		ftStatus: FreeTurnStatus | null;
		policies: AccessPolicy[];
		tunnels: TunnelListItem[];
		busyKeys: string[];
		/** Мастер, открытый явно; страница гасит его при смене инстанса. */
		exitWizard: 'new' | 'instance' | null;
		shareWizard: 'new' | 'instance' | null;
		ontoggle: (row: ProxyInstanceRow, on: boolean) => Promise<void>;
		/** Перечитать статусы — после сохранения деталь показывает жизнь процесса. */
		onstatuses: () => Promise<void>;
		/** Перечитать конфиги — мастер раздачи заводит сервер по ходу шагов. */
		onconfigs: () => Promise<void>;
		onreload: () => Promise<void>;
		onexitdone: (protocol: ExitProtocol, id: string) => void;
		onsharedone: (protocol: ExitProtocol, id: string) => void;
	}

	let {
		activeTab,
		exitRow,
		exitKey,
		shareRow,
		shareKey,
		wdttConfig,
		ftConfig,
		wdttStatus,
		ftStatus,
		policies,
		tunnels,
		busyKeys,
		exitWizard = $bindable(),
		shareWizard = $bindable(),
		ontoggle,
		onstatuses,
		onconfigs,
		onreload,
		onexitdone,
		onsharedone,
	}: Props = $props();

	let savingExit = $state(false);
	let savingShare = $state(false);

	const exitWdttClient = $derived(
		exitRow?.protocol === 'wdtt'
			? wdttConfig?.clients.find((c) => c.id === exitRow.id)?.config
			: undefined,
	);
	const exitFtClient = $derived(
		exitRow?.protocol === 'freeturn'
			? ftConfig?.clients.find((c) => c.id === exitRow.id)?.config
			: undefined,
	);
	const exitStatus = $derived(
		exitRow?.protocol === 'wdtt'
			? wdttStatus?.clients?.find((c) => c.id === exitRow.id)?.status
			: ftStatus?.clients?.find((c) => c.id === exitRow?.id)?.status,
	);
	const shareWdttServer = $derived(
		shareRow?.protocol === 'wdtt'
			? wdttConfig?.servers.find((s) => s.id === shareRow.id)?.config
			: undefined,
	);
	const shareFtServer = $derived(
		shareRow?.protocol === 'freeturn'
			? ftConfig?.servers.find((s) => s.id === shareRow.id)?.config
			: undefined,
	);
	const shareStatus = $derived(
		shareRow?.protocol === 'wdtt'
			? wdttStatus?.servers?.find((s) => s.id === shareRow.id)?.status
			: ftStatus?.servers?.find((s) => s.id === shareRow?.id)?.status,
	);

	const exitLife = $derived({
		running: exitRow?.state === 'running',
		startedAt: exitRow?.startedAt,
		enabled: exitRow?.autostart,
	});
	/** Инстанс ни разу не поднимался — только тогда возврат в мастер осмыслен (RB-08). */
	const exitNeverRan = $derived(!proxyInOpsMode(exitLife));
	// Мастер открывается ТОЛЬКО явно — кнопкой списка или «Мастер» в детали.
	// Прежде он подменял деталь у ненастроенного инстанса, и клик по строке
	// вёл в мастер вместо параметров: настроить инстанс было негде, а обходом
	// служил тумблер (включённый инстанс считался «в эксплуатации»).
	const exitWizardOpen = $derived(exitWizard !== null);
	const shareLife = $derived({
		running: shareRow?.state === 'running',
		startedAt: shareRow?.startedAt,
		enabled: shareRow?.autostart,
	});
	/** Сервер ни разу не поднимался — только тогда возврат в мастер осмыслен (RB-08). */
	const shareNeverRan = $derived(!proxyInOpsMode(shareLife));
	const shareWizardOpen = $derived(shareWizard !== null);
	const wizardOpen = $derived(activeTab === 'exit' ? exitWizardOpen : shareWizardOpen);
	/**
	 * Дефолт порта Endpoint мастера раздачи: listen FreeTurn-клиента роутера
	 * (F-18). Подставляется, только когда клиент ЕДИНСТВЕННЫЙ: понятия
	 * «выбранный клиент» у мастера раздачи нет, а при нескольких клиентах любой
	 * выбор был бы догадкой. 0 — порт неизвестен, поле остаётся пустым.
	 */
	const ftClientPort = $derived(
		(ftConfig?.clients?.length ?? 0) === 1
			? listenPortNumber(ftConfig?.clients?.[0]?.config.listen ?? '', 0)
			: 0,
	);
	/**
	 * Занятые порты серверов раздачи — подсказка порта новому серверу. WDTT-
	 * сервер держит два: DTLS-listen и внутренний WG-порт (`wgPort`); бэкенд
	 * считает занятыми оба (`proxylisten.CrossChecker`), и подсказка обязана
	 * считать так же — иначе она называет порт, который бэкенд молча подвинет.
	 */
	const usedSharePorts = $derived([
		...(ftConfig?.servers ?? []).map((s) => listenPortNumber(s.config.listen ?? '', 0)),
		...(wdttConfig?.servers ?? []).flatMap((s) => [
			listenPortNumber(s.config.listen ?? '', 0),
			s.config.wgPort ?? 0,
		]),
	]);
	/** Занятые локальные порты: подсказка порта новому клиенту. */
	const usedListens = $derived({
		wdtt: (wdttConfig?.clients ?? []).map((c) => c.config.listen),
		freeturn: (ftConfig?.clients ?? []).map((c) => c.config.listen),
	});

	/**
	 * Сохранение параметров клиента: смена адреса сервера останавливает клиент
	 * (W-27), работающему после сохранения — просьба перезапустить (TS-04).
	 * Возвращает конфиг сервера после записи; null — не сохранилось, и деталь
	 * по нему не запускает клиента (EX-32).
	 */
	async function saveExit(config: ExitConfig): Promise<ExitConfig | null> {
		const row = exitRow;
		const inst = exitInstance(row, wdttConfig, ftConfig);
		if (!row || !inst) return null;
		savingExit = true;
		try {
			const res = await saveExitInstance(row, inst, config);
			reportDeletedTunnels(res.deletedTunnels, res.tunnelErrors);
			if (row.state === 'running' && res.peerChanged) await ontoggle(row, false);
			else if (row.state === 'running') {
				notifications.info('Перезапустите клиент, чтобы изменения применились');
			}
			await onstatuses();
			return res.config;
		} catch (e) {
			notifications.error(errText(e));
			return null;
		} finally {
			savingExit = false;
		}
	}

	/**
	 * Сохранение конфига сервера раздачи. Возвращает конфиг на бэкенде после
	 * записи; null — не сохранилось.
	 */
	async function saveShare(config: ShareConfig): Promise<ShareConfig | null> {
		const row = shareRow;
		const inst = shareInstance(row, wdttConfig, ftConfig);
		if (!row || !inst) return null;
		savingShare = true;
		try {
			const stored = await saveShareInstance(row, inst, config);
			await onstatuses();
			return stored;
		} catch (e) {
			notifications.error(errText(e));
			return null;
		} finally {
			savingShare = false;
		}
	}

	/** Перезапуск сервера формой детали: смена режима server.log (SH-68). */
	async function restartShare() {
		const row = shareRow;
		if (!row) return;
		await ontoggle(row, false);
		await ontoggle(row, true);
	}
</script>

{#if activeTab === 'exit' && exitWizardOpen}
	<!-- Мастер живёт от источника до запуска: {#key} сбрасывает его
	     состояние при смене инстанса и при заходе за новым. -->
	{#key exitWizard === 'new' ? 'new' : exitKey}
		<ExitWizard
			{policies}
			{usedListens}
			row={exitWizard === 'new' ? null : exitRow}
			wdttClient={exitWizard === 'new' ? undefined : exitWdttClient}
			ftClient={exitWizard === 'new' ? undefined : exitFtClient}
			onclose={() => (exitWizard = null)}
			ondone={onexitdone}
		/>
	{/key}
{:else if wizardOpen}
	<!-- Мастер живёт от протокола до ссылки: {#key} сбрасывает его
	     состояние при смене инстанса и при заходе за новым. -->
	{#key shareWizard === 'new' ? 'new' : shareKey}
		<ShareWizard
			wdttServerExists={(wdttConfig?.servers.length ?? 0) > 0}
			serverSupported={wdttStatus?.serverSupported}
			{ftClientPort}
			usedPorts={usedSharePorts}
			row={shareWizard === 'new' ? null : shareRow}
			wdttServer={shareWizard === 'new' ? undefined : shareWdttServer}
			ftServer={shareWizard === 'new' ? undefined : shareFtServer}
			onclose={() => (shareWizard = null)}
			onreload={onconfigs}
			ondone={onsharedone}
		/>
	{/key}
{:else if activeTab === 'exit' && exitRow}
	<!-- Локальное состояние детали не переживает смену инстанса (W-13). -->
	{#key exitRow.key}
		<ExitDetail
			row={exitRow}
			status={exitStatus}
			wdttClient={exitWdttClient}
			ftClient={exitFtClient}
			routerClock={wdttStatus?.routerClock ?? ftStatus?.routerClock}
			{policies}
			{tunnels}
			saving={savingExit}
			busy={busyKeys.includes(exitRow.key)}
			onstart={() => exitRow && ontoggle(exitRow, true)}
			onstop={() => exitRow && ontoggle(exitRow, false)}
			onwizard={exitNeverRan ? () => (exitWizard = 'instance') : undefined}
			onsave={saveExit}
			{onreload}
		/>
	{/key}
{:else if activeTab === 'share' && shareRow}
	<!-- Локальное состояние детали не переживает смену инстанса (W-13). -->
	{#key shareRow.key}
		<ShareDetail
			row={shareRow}
			status={shareStatus}
			wdttServer={shareWdttServer}
			ftServer={shareFtServer}
			routerClock={wdttStatus?.routerClock ?? ftStatus?.routerClock}
			{policies}
			saving={savingShare}
			busy={busyKeys.includes(shareRow.key)}
			onstart={() => shareRow && ontoggle(shareRow, true)}
			onstop={() => shareRow && ontoggle(shareRow, false)}
			onwizard={shareNeverRan ? () => (shareWizard = 'instance') : undefined}
			onrestart={restartShare}
			onsave={saveShare}
			{onreload}
		/>
	{/key}
{/if}
