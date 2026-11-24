<script lang="ts">
	// Деталь вкладки «Выход» — одна колонка секций сверху вниз (ia.md §2.2).
	// Конфиг с прихода страницы — состояние сервера; правит пользователь
	// редактируемую копию, которая живёт здесь (W-22). Сохраняет её страница:
	// она владеет конфигами и статусами.
	import { m } from '$lib/i18n';
	import { untrack } from 'svelte';
	import { Badge, Button, Card, FieldHint, SideDrawer, Stat, StatStrip } from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import { WDTT_WG_ADDRESS_CONFLICT, WDTT_WG_NOT_READY } from '$lib/api/clientWdtt';
	import { notifications } from '$lib/stores/notifications';
	import { errText } from '$lib/utils/errorMessage';
	import { formatUptime } from '../freeturn/uptime';
	import { formatBytes } from '$lib/utils/format';
	import type {
		AccessPolicy,
		FreeTurnClientConfig,
		FreeTurnProcessStatus,
		TunnelListItem,
		WdttClientConfig,
		WdttProcessStatus,
	} from '$lib/types';
	import AdvancedSection from './AdvancedSection.svelte';
	import CaptchaSection from './CaptchaSection.svelte';
	import ExitParamsSection from './ExitParamsSection.svelte';
	import ExitWhereSection from './ExitWhereSection.svelte';
	import LastErrorBox from './LastErrorBox.svelte';
	import LogSection from './LogSection.svelte';
	import SubscriptionSection from './SubscriptionSection.svelte';
	import { allowEnsure, markEnsured } from './ensureGuard';
	import { cloneConfig, type ExitConfig } from './exitConfig';
	import { apiErrorCode } from './serverClients';
	import { findLinkedTunnel, listenPort } from './linkedTunnel';
	import InstanceBadges from './InstanceBadges.svelte';
	import WdttDiagModal from './WdttDiagModal.svelte';
	import type { ProxyInstanceRow } from './rows';

	interface Props {
		row: ProxyInstanceRow;
		status?: WdttProcessStatus | FreeTurnProcessStatus;
		/** Конфиг выбранного инстанса на сервере — ровно один из двух. */
		wdttClient?: WdttClientConfig;
		ftClient?: FreeTurnClientConfig;
		routerClock?: string;
		policies: AccessPolicy[];
		tunnels: TunnelListItem[];
		saving?: boolean;
		busy?: boolean;
		onstart: () => void;
		onstop: () => void;
		onwizard?: () => void;
		/** Сохранение: конфиг сервера после записи или null, если не сохранилось. */
		onsave: (config: ExitConfig) => Promise<ExitConfig | null>;
		onreload: () => Promise<void> | void;
	}

	let {
		row,
		status,
		wdttClient,
		ftClient,
		routerClock = '',
		policies,
		tunnels,
		saving = false,
		busy = false,
		onstart,
		onstop,
		onwizard,
		onsave,
		onreload,
	}: Props = $props();

	// Редактируемая копия конфига. Поллинг и перезагрузки страницы обновляют
	// prop-конфиг, а несохранённые правки живут здесь и не затираются (W-22).
	// Копия создаётся вместе с деталью, а деталь пересоздаётся при смене
	// инстанса ({#key} на странице, W-13).
	let wdttDraft = $state(untrack(() => (wdttClient ? cloneConfig(wdttClient) : undefined)));
	let ftDraft = $state(untrack(() => (ftClient ? cloneConfig(ftClient) : undefined)));

	const wdttStatus = $derived(row.protocol === 'wdtt' ? (status as WdttProcessStatus) : undefined);
	// raw — режим ПРИМЕНЁННЫЙ: по нему живут связанный туннель, интерфейс и
	// автозавод, то есть всё, что уже существует в системе.
	const raw = $derived(row.mode === 'raw');
	// draftRaw — режим ВЫБРАННЫЙ в форме. Бейдж шапки и подсказки полей идут по
	// нему: пока они шли по применённому, переключение WG→Raw оставляло бейдж
	// прежним до нажатия «Сохранить», и человек видел «Raw» в сегменте рядом с
	// «WDTT · WG» в заголовке.
	const draftRaw = $derived(
		wdttDraft ? wdttDraft.connMode === 'raw' : raw,
	);
	const running = $derived(row.state === 'running');
	const listen = $derived(wdttClient?.listen ?? ftClient?.listen ?? '');
	const port = $derived(listenPort(listen));

	// LS-10..12 — протокол и режим инстанса.
	const badge = $derived(
		row.protocol === 'freeturn' ? 'FreeTurn' : draftRaw ? 'WDTT · Raw' : 'WDTT · WG',
	);

	const uptime = $derived(formatUptime(row.startedAt) || '—');

	// Настройки — одноразовые: уезжают в ящик, как у раздачи.
	let settingsOpen = $state(false);
	let diagOpen = $state(false);
	// Закрываем модалку только при смене id инстанса (на случай монтирования
	// без {#key}): фоновый поллинг пересоздаёт объект row с тем же id и не
	// должен сбрасывать открытую диагностику.
	let prevRowId = untrack(() => row.id);

	$effect(() => {
		const id = row.id;
		if (id !== prevRowId) {
			prevRowId = id;
			diagOpen = false;
		}
	});

	// Правило имён ia.md §1.0: на странице NDMS-имя, kernel-имя — под (i).
	const rawNdms = $derived(wdttStatus?.ndmsIface?.trim() || wdttClient?.ndmsIface?.trim() || '');
	const rawKernel = $derived(wdttStatus?.rawIface?.trim() || wdttClient?.rawIface?.trim() || '');
	const tunnel = $derived(
		raw ? null : findLinkedTunnel(tunnels, listen, row.protocol === 'wdtt' ? row.id : undefined),
	);
	// Трафик берётся у СВЯЗАННОГО туннеля: своей истории у прокси-процесса нет,
	// а гонять человека за цифрами на другую страницу — ровно та причина, по
	// которой «Выход» выглядел бледной копией карточки туннеля.
	const trafficValue = $derived(
		tunnel && (tunnel.rxBytes !== undefined || tunnel.txBytes !== undefined)
			? `${formatBytes(tunnel.rxBytes ?? 0)} / ${formatBytes(tunnel.txBytes ?? 0)}`
			: '—',
	);

	// Пустой адрес сервера: клиент с ним не стартует («не задан адрес сервера
	// (-peer)»), поэтому ни сохранять, ни запускать такой конфиг нельзя. Ловится
	// в первую очередь при смене режима — у WG и Raw адреса разные, а в ссылке
	// wdtt:// лежит только DTLS-порт.
	const noPeerHint = $derived(
		wdttDraft && !wdttDraft.peer?.trim()
			? m.proxy_exit_detail_no_peer_hint()
			: '',
	);

	// ─── Сохранение и откат правок.

	async function save(): Promise<boolean> {
		const draft = wdttDraft ?? ftDraft;
		if (!draft) return false;
		const sent = cloneConfig(draft);
		const stored = await onsave(sent);
		if (!stored) return false;
		// W-22: ответ ложится в копию, только если пользователь не правил поля
		// во время запроса — иначе его правки были бы затёрты.
		if (JSON.stringify(draft) === JSON.stringify(sent)) {
			if (wdttDraft) wdttDraft = cloneConfig(stored) as WdttClientConfig;
			else ftDraft = cloneConfig(stored) as FreeTurnClientConfig;
		}
		return true;
	}

	/** EX-24 «Отменить» — возврат к тому, что лежит на сервере. */
	function revert() {
		if (wdttClient) wdttDraft = cloneConfig(wdttClient);
		else if (ftClient) ftDraft = cloneConfig(ftClient);
	}

	/** EX-32: клиент стартует только после успешного сохранения. */
	async function saveAndStart() {
		if (await save()) onstart();
	}

	let restarting = $state(false);

	async function restart() {
		if (restarting || busy) return;
		restarting = true;
		try {
			if (row.protocol === 'wdtt') {
				await api.restartWdttClient(row.id);
			} else {
				await api.restartFreeTurnClient(row.id);
			}
			notifications.success(m.proxy_exit_detail_restarted());
			await onreload();
		} catch (e) {
			notifications.error(errText(e));
		} finally {
			restarting = false;
		}
	}

	// ─── Автоповедения клиента (W-19, W-20): туннель заводится сам.

	let tunnelBusy = $state(false);

	async function ensureTunnel(manual = false) {
		// tunnelBusy держит и реентрантность: второй запрос ждать нечего.
		// Raw сюда не заходит: зеркальную запись выхода ведёт сам движок,
		// ручки подготовки у неё больше нет.
		if (row.protocol !== 'wdtt' || raw || tunnelBusy) return;
		const id = row.id;
		if (!allowEnsure(id, manual)) return;
		tunnelBusy = true;
		try {
			const res = await api.ensureWdttWgTunnel(id);
			if (res.created) {
				markEnsured(id);
				// TS-01 — про туннель из журнала (режим WG).
				notifications.success(m.proxy_exit_detail_tunnel_created({ name: res.tunnelName ?? '' }));
				await onreload();
			} else if (res.tunnelId) {
				markEnsured(id);
			}
		} catch (e) {
			// «Конфиг ещё не приехал» — не ошибка, а ожидание: автоэффект зовёт
			// ручку сам, и тост на каждый заход был бы ложной тревогой. Глушим
			// ровно этот код, не «ошибку вообще».
			const code = apiErrorCode(e);
			if (code === WDTT_WG_NOT_READY) return;
			// Конфликт адреса (#869) сам не рассосётся: тост один раз, автозавод
			// гасим, кнопка «вручную» остаётся.
			if (code === WDTT_WG_ADDRESS_CONFLICT) markEnsured(id);
			notifications.error(errText(e));
		} finally {
			tunnelBusy = false;
		}
	}

	$effect(() => {
		const wg = wdttStatus?.wgConfig?.trim();
		if (row.protocol !== 'wdtt' || raw || !running || !wg) return;
		untrack(() => ensureTunnel());
	});

	async function importConf(conf: string) {
		tunnelBusy = true;
		try {
			const tun = await api.importConfig({
				content: conf,
				name: row.name,
				freeTurnClientId: row.protocol === 'freeturn' ? row.id : undefined,
				wdttClientId: row.protocol === 'wdtt' ? row.id : undefined,
			});
			markEnsured(row.id);
			notifications.success(m.proxy_exit_detail_tunnel_created({ name: tun.name }));
			await onreload();
		} catch (e) {
			notifications.error(errText(e));
		} finally {
			tunnelBusy = false;
		}
	}
</script>

<Card padding="lg">
	<div class="head">
		<h2>{row.name}</h2>
		<Badge size="sm" variant={row.protocol === 'wdtt' ? 'accent' : 'purple'}>{badge}</Badge>
		<InstanceBadges {row} mode={draftRaw ? 'raw' : 'wg'} />
		<div class="head-actions">
			{#if running}
				<Button variant="secondary" size="sm" loading={restarting} disabled={busy || restarting} onclick={restart}>
					{m.common_restart()}
				</Button>
				<Button variant="secondary" size="sm" disabled={busy || restarting} onclick={onstop}>{m.common_stop()}</Button>
			{:else}
				<Button variant="primary" size="sm" disabled={busy || !!noPeerHint} onclick={onstart}>
					{m.common_start()}
				</Button>
				{#if noPeerHint}
					<FieldHint text={noPeerHint} ariaLabel={m.proxy_exit_detail_start_blocked_aria()} />
				{/if}
			{/if}
			{#if row.protocol === 'wdtt'}
				<Button variant="ghost" size="sm" disabled={!!noPeerHint} onclick={() => (diagOpen = true)}>
					{m.proxy_exit_detail_diag()}
				</Button>
			{/if}
			<Button variant="ghost" size="sm" onclick={() => (settingsOpen = true)}>{m.proxy_common_settings()}</Button>
			{#if onwizard}
				<Button variant="ghost" size="sm" onclick={onwizard}>{m.proxy_common_wizard()}</Button>
			{/if}
		</div>
	</div>

	<!-- Состояние: то, что смотрят каждый день. Трафик — из связанного
	     туннеля, чтобы за ним не ходить на другую страницу. -->
	<StatStrip>
		<Stat
			value={running ? m.proxy_detail_state_running() : m.proxy_detail_state_stopped()}
			label={m.proxy_detail_state()}
			sub={uptime}
		/>
		<Stat value={String(status?.dtlsConnections ?? 0)} label={m.proxy_exit_detail_connections()} />
		<Stat
			value={trafficValue}
			label={m.proxy_exit_detail_traffic()}
			sub={m.proxy_exit_detail_traffic_sub()}
		/>
		<Stat value={listen || '—'} label={m.proxy_exit_detail_local_port()} />
	</StatStrip>

	<!-- EX-01: ошибка живёт, пока процесс не работает. -->
	<LastErrorBox text={running ? '' : (status?.lastError ?? '')} />

	<ExitWhereSection protocol={row.protocol} {raw} {rawNdms} {rawKernel} {tunnel} {port} {policies} />

	<!-- Поллинг капчи не крутится у остановленного клиента: подтверждать
	     нечего, пока потоки не поднимаются. -->
	{#if row.protocol === 'freeturn' && running}
		<CaptchaSection clientId={row.id} />
	{/if}

	<LogSection
		log={status?.log}
		{routerClock}
		showDebug={row.protocol === 'freeturn'}
		debug={ftDraft?.debug ?? false}
		ondebug={(on) => {
			if (ftDraft) ftDraft.debug = on;
		}}
	/>
</Card>

<!-- Параметры выхода — одноразовая настройка: адрес, пароль, хеши, потоки,
     капча. В ящике они не занимают экран, на котором смотрят состояние. -->
<SideDrawer open={settingsOpen} onClose={() => (settingsOpen = false)} title={m.proxy_exit_detail_drawer_title()}>
	<ExitParamsSection
		bind:wdttClient={wdttDraft}
		bind:ftClient={ftDraft}
		{saving}
		saveBlockedHint={noPeerHint}
		onsave={save}
		onrevert={revert}
	/>

	{#if wdttDraft?.sub?.trim()}
		<SubscriptionSection
			instanceId={row.id}
			bind:client={wdttDraft}
			onsaveandstart={saveAndStart}
			{onreload}
		/>
	{/if}

	<AdvancedSection
		bind:wdttClient={wdttDraft}
		bind:ftClient={ftDraft}
		{raw}
		wgConf={wdttStatus?.wgConfig ?? ''}
		ports={listen ? [{ listen }] : []}
		onensuretunnel={() => ensureTunnel(true)}
		onimportconf={importConf}
		busyTunnel={tunnelBusy}
	/>
</SideDrawer>

{#if row.protocol === 'wdtt'}
	<WdttDiagModal
		open={diagOpen}
		instanceId={row.id}
		instanceName={row.name}
		diagKeySet={wdttClient?.diagKeySet}
		onclose={() => (diagOpen = false)}
	/>
{/if}

<style>
	.head {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		flex-wrap: wrap;
		min-width: 0;
		margin-bottom: 0.75rem;
	}

	h2 {
		margin: 0;
		font-size: 1.125rem;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	/* Действия — группой справа, как у раздачи и карточки WG-сервера. */
	.head-actions {
		display: flex;
		align-items: center;
		gap: 0.375rem;
		margin-left: auto;
	}
</style>
