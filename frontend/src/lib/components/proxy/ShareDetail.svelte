<script lang="ts">
	// Деталь вкладки «Раздача» — одна колонка секций сверху вниз (ia.md §3.2).
	// Конфиг с прихода страницы — состояние сервера; правит пользователь
	// редактируемую копию, которая живёт здесь (W-22). Сохраняет её страница:
	// она владеет конфигами и статусами.
	import { onMount, untrack } from 'svelte';
	import { Badge, Button, Card, Dropdown, FieldHint, SideDrawer, Stat, StatStrip, Toggle } from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { errText } from '$lib/utils/errorMessage';
	import { createIngressMutationLock } from '$lib/utils/ingressMutation';
	import { servers, type ServersSnapshot } from '$lib/stores/servers';
	import { buildRunningServerPeerDropdownOptions } from '$lib/utils/serverPeerOptions';
	import { formatUptime } from '../freeturn/uptime';
	import type { NatMode } from '$lib/utils/network';
	import type {
		FreeTurnProcessStatus,
		FreeTurnServerConfig,
		SingboxRouterSettings,
		WdttProcessStatus,
		WdttServerConfig,
	} from '$lib/types';
	import LastErrorBox from './LastErrorBox.svelte';
	import LogSection from './LogSection.svelte';
	import ServerAllowlist from './ServerAllowlist.svelte';
	import ServerClients from './ServerClients.svelte';
	import { CLIENT_TEXT } from './serverClients';
	import ShareAdvancedSection from './ShareAdvancedSection.svelte';
	import ShareNetworkSection from './ShareNetworkSection.svelte';
	import { cloneConfig } from './exitConfig';
	import {
		freeTurnServerPorts,
		wdttServerKillPorts,
		wdttServerPorts,
		type ShareConfig,
	} from './shareConfig';
	import { ingressOn, nextIngressInterfaces, wdttIngressRefs } from './shareIngress';
	import InstanceBadges from './InstanceBadges.svelte';
	import type { ProxyInstanceRow } from './rows';

	interface Props {
		row: ProxyInstanceRow;
		status?: WdttProcessStatus | FreeTurnProcessStatus;
		/** Конфиг выбранного сервера на бэкенде — ровно один из двух. */
		wdttServer?: WdttServerConfig;
		ftServer?: FreeTurnServerConfig;
		routerClock?: string;
		saving?: boolean;
		busy?: boolean;
		onstart: () => void;
		onstop: () => void;
		onwizard?: () => void;
		/** Перезапуск сервера формой: смена режима server.log (SH-68). */
		onrestart: () => Promise<void>;
		/** Сохранение: конфиг сервера после записи или null, если не сохранилось. */
		onsave: (config: ShareConfig) => Promise<ShareConfig | null>;
		onreload: () => Promise<void> | void;
	}

	let {
		row,
		status,
		wdttServer,
		ftServer,
		routerClock = '',
		saving = false,
		busy = false,
		onstart,
		onstop,
		onwizard,
		onrestart,
		onsave,
		onreload,
	}: Props = $props();

	// Редактируемая копия конфига: поллинг обновляет prop, а несохранённые
	// правки живут здесь и не затираются (W-22). Деталь пересоздаётся при смене
	// инстанса ({#key} на странице).
	let wdttDraft = $state(untrack(() => (wdttServer ? cloneConfig(wdttServer) : undefined)));
	let ftDraft = $state(untrack(() => (ftServer ? cloneConfig(ftServer) : undefined)));

	// Общий замок мутаций сервера: одна операция за раз. Мутации ingress вдобавок
	// сериализуются своим замком — чтение-правка-запись настроек sing-box иначе
	// теряет запись (WS-22).
	const withIngressLock = createIngressMutationLock();
	let mutating = $state(false);

	// .conf пира, выбранного виджетом «Сеть»: он уезжает в ссылку абоненту
	// FreeTurn (ia.md §3.3 часть Б — конфиг пира попадает в ссылку).
	// Настройки — одноразовые: живут в выдвижном ящике, а не занимают экран,
	// на котором человек каждый день смотрит состояние и правит абонентов.
	let settingsOpen = $state(false);

	let peerConf = $state('');
	// Выбранный пир живёт здесь: его показывают ДВА контрола — быстрый селект
	// строки состояния (Дополнение №4 п.3) и виджет «Сети». Состояние одно,
	// вся механика (.conf Keenetic, запрос конфига) осталась в «Сети».
	let peer = $state('');
	let peerSnap = $state<ServersSnapshot | null>(null);
	let lanOptions = $state<{ value: string; label: string }[]>([]);
	let wanOptions = $state<{ value: string; label: string }[]>([]);
	let ingress = $state(false);
	// RB-11 показывается, только когда точно известно, что sing-box не работает:
	// до ответа ручки статуса тумблер молчит, а не пугает.
	let singboxRunning = $state(true);
	// Режим устройств «все» в tproxy делает тумблер неотличимым от включённого:
	// jump в цепочку sing-box эмитится в PREROUTING безусловно, а MARK-правила
	// по входным интерфейсам в этом режиме не эмитятся вовсе
	// (internal/singbox/router/iptables.go) — трафик абонентов с opkgtun
	// перехватывается при любом его положении. Ссылка при этом сохраняется и
	// заработает после возврата режима «по политике», поэтому тумблер живой.
	let ingressForced = $state(false);

	// Гейт старта WDTT-сервера (SH-91): «Абонент 1» на путях UI больше не
	// рождается, и сервер без единого рабочего пароля просто не поднимется
	// («[WRAP] нет активных паролей»). Число рабочих отдаёт блок «Абоненты»;
	// `undefined` — состав ещё не пришёл, и блокировать нельзя.
	let usableClients = $state<number | undefined>(undefined);
	let totalClients = $state<number | undefined>(undefined);

	const peerOptions = $derived(buildRunningServerPeerDropdownOptions(peerSnap));
	const wdttStatus = $derived(row.protocol === 'wdtt' ? (status as WdttProcessStatus) : undefined);
	const running = $derived(row.state === 'running');
	// У работающего сервера подсказки нет: «Запустить» и так заперта состоянием,
	// а текст про незапускаемый сервер рядом с запущенным — прямая неправда.
	const startBlockedHint = $derived(
		wdttServer && !running && usableClients === 0 ? CLIENT_TEXT.startNoUsable : '',
	);
	const ports = $derived(
		wdttDraft ? wdttServerPorts(wdttDraft) : ftDraft ? freeTurnServerPorts(ftDraft) : [],
	);
	// Освобождать приходится и внутренний WG-порт: сервер поднимает на нём
	// WireGuard, а в мете строки состояния (RB-07) его не показывают.
	const killPorts = $derived(wdttDraft ? wdttServerKillPorts(wdttDraft) : ports);

	// Плитки состояния — то, ради чего на страницу заходят каждый день.
	// Прежде это была одна строка через точки-разделители, где аптайм, PID и
	// два порта стояли в ряд с кнопками и тумблером.
	const uptimeValue = $derived(running ? formatUptime(row.startedAt) || '—' : '—');
	const clientsValue = $derived(
		usableClients === undefined ? '—' : `${usableClients} / ${totalClients ?? usableClients}`,
	);
	const portTiles = $derived(ports.slice(0, 2));

	// Применённое значение exposeToPolicies — из статуса: тумблер применяется
	// только на старте процесса, и с чем тот стартовал, знает один бэкенд.
	// `undefined` — не знает и он (сервер не запускался, остановлен, процесс
	// усыновлён); тогда расхождения не показываем (SH-56 молчит).
	const exposeApplied = $derived(wdttStatus?.appliedExposeToPolicies);

	// Каталог пиров общий со «Сетью»: стор один, второго запроса не будет.
	// Отдельным эффектом, а не в onMount: асинхронный onMount отписку не вернёт.
	$effect(() => servers.subscribe((st) => (peerSnap = st.data)));

	/** Легаси-ответ без routingMode — это ещё одномодовый бэкенд, там был tproxy. */
	function allDevicesTProxy(s: SingboxRouterSettings): boolean {
		return (s.routingMode ?? 'tproxy') === 'tproxy' && s.deviceMode === 'all';
	}

	onMount(async () => {
		try {
			const segs = await api.listManagedLANSegments();
			lanOptions = segs.map((s) => ({ value: s.name, label: s.label || s.name }));
		} catch {
			/* сегменты вторичны: без них остаётся выбор без подписей */
		}
		// WAN-интерфейсы нужны режиму NAT «Интернет» (natStaticWan). Вторичны:
		// без них поле остаётся, но выбирать не из чего.
		try {
			const wans = await api.getWANInterfaces();
			wanOptions = wans.map((w) => ({ value: w.name, label: w.label || w.name }));
		} catch {
			/* список WAN вторичен */
		}
		// WS-27: фактическое состояние тумблера — из настроек роутера sing-box.
		if (!wdttDraft) return;
		try {
			const settings = await api.singboxRouterGetSettings();
			ingress = ingressOn(settings.ingressInterfaces, wdttIngressRefs(wdttDraft, wdttStatus));
			ingressForced = allDevicesTProxy(settings);
		} catch {
			/* нет ответа — тумблер остаётся выключенным */
		}
		try {
			singboxRunning = (await api.singboxGetStatus()).running;
		} catch {
			/* нет ответа — про состояние sing-box ничего не заявляем */
		}
	});

	// ─── Мутации под общим замком.

	async function locked(fn: () => Promise<void>) {
		if (mutating) return;
		mutating = true;
		try {
			await fn();
		} catch (e) {
			notifications.error(errText(e));
		} finally {
			mutating = false;
		}
	}

	function setNat(mode: NatMode) {
		if (!wdttDraft || mode === (wdttDraft.natMode ?? 'full')) return;
		void locked(async () => {
			const res = await api.setWdttServerNATMode(row.id, mode);
			if (wdttDraft) wdttDraft.natMode = res.config.natMode ?? mode;
			await onreload();
		});
	}

	function setLan(segments: string[]) {
		if (!wdttDraft) return;
		void locked(async () => {
			const res = await api.setWdttServerLANSegments(row.id, segments);
			if (wdttDraft) wdttDraft.lanSegments = res.config.lanSegments ?? segments;
			await onreload();
		});
	}

	function setPolicy(policy: string) {
		if (!wdttDraft || policy === (wdttDraft.policy || 'none')) return;
		void locked(async () => {
			const res = await api.setWdttServerPolicy(row.id, policy);
			if (wdttDraft) wdttDraft.policy = res.config.policy ?? policy;
			await onreload();
		});
	}

	/** RB-09: тумблер живёт в настройках роутера sing-box, не в конфиге сервера. */
	function toggleIngress(on: boolean) {
		if (!wdttDraft) return;
		void locked(async () => {
			await withIngressLock(async () => {
				const settings = await api.singboxRouterGetSettings();
				// Режим могли сменить на другой странице — подсказка не должна врать.
				ingressForced = allDevicesTProxy(settings);
				const refs = wdttIngressRefs(wdttDraft as WdttServerConfig, wdttStatus);
				await api.singboxRouterPutSettings({
					...settings,
					ingressInterfaces: nextIngressInterfaces(settings.ingressInterfaces, refs, on),
				});
				ingress = on;
			});
		});
	}

	// ─── Сохранение и откат правок.

	async function save() {
		const draft = wdttDraft ?? ftDraft;
		if (!draft) return;
		const sent = cloneConfig(draft);
		// SH-68: режим server.log бэкенд на живом сервере не применяет —
		// перезапуск делает форма.
		const logModeChanged =
			!!wdttDraft && (wdttServer?.statsLog ?? 'ram') !== (wdttDraft.statsLog ?? 'ram');
		await locked(async () => {
			const stored = await onsave(sent);
			if (!stored) return;
			// W-22: ответ ложится в копию, только если пользователь не правил
			// поля во время запроса — иначе его правки были бы затёрты.
			if (JSON.stringify(draft) === JSON.stringify(sent)) {
				if (wdttDraft) wdttDraft = cloneConfig(stored) as WdttServerConfig;
				else ftDraft = cloneConfig(stored) as FreeTurnServerConfig;
			}
			if (logModeChanged && running) await onrestart();
		});
	}

	/** «Отменить» — возврат к тому, что лежит на сервере. */
	function revert() {
		if (wdttServer) wdttDraft = cloneConfig(wdttServer);
		else if (ftServer) ftDraft = cloneConfig(ftServer);
	}
</script>

<Card padding="lg">
	<!-- Шапка: кто это и что с ним делают. Действия — справа группой, как на
	     карточке WG-сервера; настройки уезжают в ящик. -->
	<div class="head">
		<h2>{row.name}</h2>
		<Badge size="sm" variant={row.protocol === 'wdtt' ? 'accent' : 'purple'}>
			{row.protocol === 'wdtt' ? 'WDTT-сервер' : 'FreeTurn-сервер'}
		</Badge>
		<InstanceBadges {row} />
		<div class="head-actions">
			{#if running}
				<Button variant="secondary" size="sm" disabled={busy} onclick={onstop}>Остановить</Button>
			{:else}
				<Button
					variant="primary"
					size="sm"
					disabled={busy || !!startBlockedHint}
					onclick={onstart}
				>
					Запустить
				</Button>
				{#if startBlockedHint}
					<FieldHint text={startBlockedHint} ariaLabel="Подсказка: сервер не запускается" />
				{/if}
			{/if}
			<Button variant="ghost" size="sm" onclick={() => (settingsOpen = true)}>Настройки</Button>
			{#if onwizard}
				<Button variant="ghost" size="sm" onclick={onwizard}>Мастер</Button>
			{/if}
		</div>
	</div>

	<!-- Состояние: четыре числа, которые смотрят каждый день. -->
	<StatStrip>
		<Stat value={running ? 'Запущен' : 'Остановлен'} label="Состояние" sub={uptimeValue} />
		<Stat value={clientsValue} label="Абоненты" sub="рабочих / всего" />
		{#each portTiles as p (p.label)}
			<Stat value={`:${p.port}`} label={p.label} />
		{/each}
	</StatStrip>

	<!-- EX-01: та же форма, что у «Выхода» — ошибка живёт, пока процесс не работает. -->
	<LastErrorBox text={running ? '' : (status?.lastError ?? '')} />

	<!-- Абоненты — во всю ширину: ради них сюда и заходят. Прежде блок жил в
	     правой колонке рядом с формой, и на 1440px ему доставалась половина
	     экрана, а форма занимала вторую. -->
	<div id="share-clients" class="clients-block">
		{#if wdttServer}
			<ServerClients
				serverId={row.id}
				serverName={row.name}
				server={wdttServer}
				{running}
				busy={mutating}
				{locked}
				onusable={(count, total) => {
					usableClients = count;
					totalClients = total;
				}}
			/>
		{:else if ftServer}
			<ServerAllowlist
				serverId={row.id}
				serverName={row.name}
				server={ftServer}
				{peerConf}
				busy={mutating}
				{locked}
			/>
		{/if}
	</div>

	<LogSection
		log={status?.log}
		{routerClock}
		hint="Это вывод процесса, а не файл server.log."
		showDebug={!!ftDraft}
		debug={ftDraft?.debug ?? false}
		debugHint="Применяется при старте процесса"
		ondebug={(on) => {
			if (ftDraft) ftDraft.debug = on;
		}}
	/>
</Card>

<!-- Настройки — одноразовые: сеть, NAT, политика, тумблеры. В ящике они не
     мешают ежедневной работе, но остаются в один клик. -->
<SideDrawer open={settingsOpen} onClose={() => (settingsOpen = false)} title="Настройки раздачи">
	{#if wdttDraft}
		<div class="drawer-toggle">
			{@render ingressToggle()}
		</div>
	{:else if ftDraft}
		<div class="drawer-toggle">
			{@render peerSelect()}
		</div>
	{/if}

	<ShareNetworkSection
		bind:wdttServer={wdttDraft}
		bind:ftServer={ftDraft}
		{lanOptions}
		{exposeApplied}
		{saving}
		busy={mutating}
		onnat={setNat}
		onlan={setLan}
		onpolicy={setPolicy}
		onsave={save}
		onrevert={revert}
		onpeerconf={(conf) => (peerConf = conf)}
		bind:peer
	/>

	<ShareAdvancedSection
		bind:wdttServer={wdttDraft}
		bind:ftServer={ftDraft}
		ports={killPorts}
		{wanOptions}
	/>
</SideDrawer>

<!-- RB-12: быстрый выбор пира зеркалит тумблер RB-09 у WDTT. Механика живёт
     в «Сети»; здесь — тот же выбор под рукой. -->
{#snippet peerSelect()}
	<div class="run-toggle">
		<Dropdown
			label="Пир"
			value={peer}
			options={peerOptions}
			placeholder={peerOptions.length ? 'Выберите…' : 'Нет поднятых WG-серверов с пирами'}
			disabled={!peerOptions.length || mutating}
			onchange={(v) => (peer = v)}
		/>
		<FieldHint
			text="Пир — вход для всех абонентов этого сервера: их трафик FreeTurn отдаёт в выбранный WG-сервер роутера, и маршрутизация абонентов повторяет маршрутизацию этого пира. Ссылка абоненту собирается из конфига пира."
			ariaLabel="Подсказка: пир сервера"
		/>
	</div>
{/snippet}

{#snippet ingressToggle()}
	<div class="run-toggle">
		<Toggle
			label="Маршрутизация через sing-box"
			hint={!singboxRunning
				? 'sing-box не запущен — правило вступит в силу после его запуска'
				: ingressForced
					? 'Режим устройств «все» — трафик абонентов и так идёт через sing-box'
					: ''}
			checked={ingress}
			disabled={mutating}
			onchange={toggleIngress}
			spinner="before"
		/>
		<FieldHint
			text={ingressForced
				? 'В маршрутизации sing-box выбран режим устройств «все»: под правила попадает весь транзитный трафик роутера, включая трафик абонентов, — при любом положении этого тумблера. Положение всё равно сохраняется и вступит в силу, когда режим вернут на «по политике».'
				: 'Трафик абонентов пойдёт по правилам sing-box — тем же, что у устройств сети. Выключено — абоненты выходят напрямую, минуя правила.'}
			ariaLabel="Подсказка: маршрутизация через sing-box"
		/>
	</div>
{/snippet}

<style>
	.head {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		flex-wrap: wrap;
		min-width: 0;
		margin-bottom: 0.75rem;
	}

	/* Действия — группой справа, как на карточке WG-сервера. На узком экране
	   переносятся под имя, а не растягивают шапку. */
	.head-actions {
		display: flex;
		align-items: center;
		gap: 0.375rem;
		margin-left: auto;
	}

	.clients-block {
		margin-top: 1rem;
	}

	/* Между плитками состояния и абонентами — воздух: это две разные вещи. */
	.clients-block + :global(*) {
		margin-top: 1rem;
	}

	.drawer-toggle {
		margin-bottom: 1rem;
	}

	h2 {
		margin: 0;
		font-size: 1.125rem;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.run-toggle {
		display: flex;
		align-items: center;
		gap: 0.375rem;
	}

	/* Селект пира в строке состояния — компактный: подпись слева, поле по
	   содержимому, а не во всю строку. */
	.run-toggle :global(.field) {
		flex-direction: row;
		align-items: center;
		gap: 0.375rem;
	}

	.run-toggle :global(.field .trigger) {
		min-width: 11rem;
		max-width: 16rem;
	}

</style>
