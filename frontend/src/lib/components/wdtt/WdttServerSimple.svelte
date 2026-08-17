<script lang="ts">
	import { onMount, untrack } from 'svelte';
	import { Button, Input, Toggle, SegmentedControl, ChipMultiSelect, Dropdown } from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { createIngressMutationLock } from '$lib/utils/ingressMutation';
	import { proxyInOpsMode, proxyServerOpsMode } from '$lib/utils/proxyOpsMode';
	import type { NatMode } from '$lib/utils/network';
	import ProcessLogBox from '../freeturn/ProcessLogBox.svelte';
	import WdttServerUsers from './WdttServerUsers.svelte';
	import WdttLinkShare from './WdttLinkShare.svelte';
	import { ServerAccessPolicyDropdown } from '$lib/components/servers';
	import ProxyInstanceStatusBar from '../proxy-panel/ProxyInstanceStatusBar.svelte';
	import ProxyPanelTabs from '../proxy-panel/ProxyPanelTabs.svelte';
	import ProxyQuickStart from '../proxy-panel/ProxyQuickStart.svelte';
	import ProxyQuickStartStep from '../proxy-panel/ProxyQuickStartStep.svelte';
	import ProxyWizardGuide from '../proxy-panel/ProxyWizardGuide.svelte';
	import type { WizardGuideItem } from '../proxy-panel/ProxyWizardGuide.svelte';
	import type { QuickStartItem } from '../proxy-panel/ProxyQuickStart.svelte';
	import { guide, finalizeGuide } from '$lib/utils/proxyWizardGuides';
	import { setListenPort, listenPortNumber } from '$lib/utils/listenPortUtils';
	import ListenPortKillButton from '../proxy-panel/ListenPortKillButton.svelte';
	import SensitiveInput from '../proxy-panel/SensitiveInput.svelte';
	import { proxyPanelMode } from '../proxy-panel/modeStore';
	import type { LogInstanceItem } from '../freeturn/LogInstanceSwitcher.svelte';
	import type { WdttPanelUserEntry, WdttProcessStatus, WdttServerConfig } from '$lib/types';

	const withIngressLock = createIngressMutationLock();

	const SERVER_TABS = [
		{ id: 'main', label: 'Основное' },
		{ id: 'links', label: 'Раздача' },
		{ id: 'network', label: 'Сеть' },
		{ id: 'log', label: 'Журнал' }
	] as const;

	type ServerTab = (typeof SERVER_TABS)[number]['id'];

	type StatsLogMode = 'ram' | 'off' | 'disk';

	const statsLogOptions: { value: StatsLogMode; label: string }[] = [
		{ value: 'ram', label: 'RAM' },
		{ value: 'off', label: 'Выкл' },
		{ value: 'disk', label: 'Flash' }
	];

	const natModeOptions: { value: NatMode; label: string }[] = [
		{ value: 'full', label: 'Полный' },
		{ value: 'internet-only', label: 'Интернет' },
		{ value: 'none', label: 'Без NAT' }
	];

	interface Props {
		server: WdttServerConfig;
		running?: boolean;
		saving?: boolean;
		generating?: boolean;
		status?: WdttProcessStatus;
		serverInstanceId?: string;
		generatedLink?: string;
		generatedLinkQwdtt?: string;
		genPeer?: string;
		genVKHashes?: string;
		onSave: (cfg: WdttServerConfig) => void | Promise<void>;
		onAccessUpdated?: (cfg: WdttServerConfig) => void | Promise<void>;
		onToggle: (on: boolean) => void | Promise<void>;
		onGenerate: (
			peer: string,
			vkHashes: string[],
			opts?: { password?: string; name?: string }
		) => Promise<{ link: string; linkQwdtt?: string; peer: string } | null>;
		instances?: LogInstanceItem[];
		selectedInstanceId?: string;
		onSelectInstance?: (id: string) => void;
		opsTab?: ServerTab;
	}

	let {
		server,
		running = false,
		saving = false,
		generating = false,
		status,
		serverInstanceId = 'default',
		generatedLink = '',
		generatedLinkQwdtt = '',
		genPeer = $bindable(''),
		genVKHashes = $bindable(''),
		onSave,
		onAccessUpdated,
		onToggle,
		onGenerate,
		instances = [],
		selectedInstanceId = '',
		onSelectInstance,
		opsTab = $bindable('main')
	}: Props = $props();

	const wdttIface = $derived(server.wgIface?.trim() || 'wdtt0');
	const rawServerIface = 'wdttraw0';
	const wdttIngressRefs = $derived([`iface:${wdttIface}`, `iface:${rawServerIface}`]);

	let starting = $state(false);
	let loadingWanPeer = $state(false);
	let linkPassword = $state('');
	let togglingIngress = $state(false);
	let togglingNAT = $state(false);
	let policyChanging = $state(false);
	let settingLAN = $state(false);
	let lanSegmentOptions = $state<{ value: string; label: string }[]>([]);
	let quickActive = $state('secret');
	let wizardOpen = $state(false);

	const listenPort = $derived.by(() => String(listenPortNumber(server.listen ?? '', 56002)));
	const wgPortStr = $derived(String(server.wgPort || 56001));
	const directListenPort = $derived.by(() =>
		String(listenPortNumber(server.directListen ?? '', Number(listenPort) || 56002))
	);
	const rawListenPort = $derived.by(() =>
		String(listenPortNumber(server.rawListen ?? '', Number(listenPort) + 1 || 56003))
	);
	const rawListenAddr = $derived.by(() => {
		if (server.rawListen?.trim()) return server.rawListen.trim();
		const host = (server.listen ?? '0.0.0.0:56002').split(':')[0] || '0.0.0.0';
		const port = Number(listenPort) + 1 || 56003;
		return setListenPort(`${host}:${port}`, port, host);
	});
	const linkPort = $derived(
		server.directListen?.trim() ? directListenPort : listenPort
	);
	const directDiffersFromDTLS = $derived.by(() => {
		if (!server.directListen?.trim()) return false;
		return (
			listenPortNumber(server.directListen, 0) !== listenPortNumber(server.listen ?? '', 56002)
		);
	});
	const statusMeta = $derived.by(() => {
		let meta = `DTLS :${listenPort} · Raw :${rawListenPort}`;
		if (directDiffersFromDTLS) meta += ` · Direct :${directListenPort}`;
		return meta;
	});
	const portsGuideText = $derived.by(() => {
		let text = `Порты: DTLS ${server.listen || '0.0.0.0:56002'}, Raw ${rawListenAddr}`;
		if (directDiffersFromDTLS) {
			text += `, Direct ${server.directListen?.trim()}`;
		}
		return text;
	});

	function applyListenPort(portStr: string) {
		const port = Math.max(1, Math.min(65535, Number(portStr) || 56002));
		server.listen = setListenPort(server.listen || '0.0.0.0:56002', port, '0.0.0.0');
		if (!server.rawListen?.trim()) {
			server.rawListen = setListenPort('0.0.0.0:56003', port + 1, '0.0.0.0');
		}
	}

	function applyRawListenPort(portStr: string) {
		const port = Math.max(1, Math.min(65535, Number(portStr) || 56003));
		const host = (server.rawListen ?? server.listen ?? '0.0.0.0:56003').split(':')[0] || '0.0.0.0';
		server.rawListen = setListenPort(`${host}:${port}`, port, host);
	}

	function applyWgPort(portStr: string) {
		server.wgPort = Math.max(1, Math.min(65535, Number(portStr) || 56001));
	}

	function applyDirectListenPort(portStr: string) {
		const port = Math.max(1, Math.min(65535, Number(portStr) || 56002));
		const host = (server.directListen ?? server.listen ?? '0.0.0.0:56002').split(':')[0] || '0.0.0.0';
		server.directListen = setListenPort(`${host}:${port}`, port, host);
	}

	const step1Done = $derived(!!server.password.trim());
	const step2Done = $derived(step1Done && !!server.listen.trim());
	const canSave = $derived(step1Done && !saving && !starting);
	const canStart = $derived(step2Done && !saving && !starting);

	/** Ops после первого запуска (startedAt) или когда ссылка сгенерирована в этой сессии. */
	const opsMode = $derived(
		proxyServerOpsMode({
			running,
			startedAt: status?.startedAt,
			enabled: server.enabled,
			generatedLink,
			setupComplete: step2Done
		})
	);
	const isExpert = $derived($proxyPanelMode === 'expert');
	const showWizard = $derived((!opsMode && !isExpert) || (opsMode && wizardOpen));
	const serverStarted = $derived(
		proxyInOpsMode({
			running,
			startedAt: status?.startedAt,
			enabled: server.enabled
		})
	);

	const quickItems = $derived<QuickStartItem[]>([
		{ id: 'secret', label: 'Секрет — пароль сервера', done: step1Done },
		{ id: 'link', label: 'Первая ссылка — wdtt:// для клиента', done: !!generatedLink },
		{ id: 'start', label: 'Запуск — поднять wdtt-server', done: serverStarted }
	]);

	const quickDoneCount = $derived(quickItems.filter((i) => i.done).length);
	const quickProgress = $derived(`Прогресс ${quickDoneCount}/${quickItems.length}`);

	const natMode = $derived((server.natMode || 'full') as NatMode);
	const statsLogMode = $derived((server.statsLog?.trim() || 'ram') as StatsLogMode);

	const secretGuideItems = $derived.by((): WizardGuideItem[] =>
		finalizeGuide([
			guide('password', 'Введите или сгенерируйте пароль (-password) — он попадёт в wdtt://', {
				done: step1Done
			}),
			guide('firewall', 'Включите «Открыть DTLS-порт в firewall» для доступа с WAN', {
				done: server.openFirewall !== false,
				pending: !step1Done
			}),
			guide('next', 'Нажмите «Далее: ссылка»', {
				done: quickActive !== 'secret',
				pending: !step1Done
			})
		])
	);

	const linkGuideItems = $derived.by((): WizardGuideItem[] => {
		const vkFilled = !!genVKHashes.trim();
		return finalizeGuide([
			guide('wan', 'Укажите peer = WAN IP : DTLS-порт (кнопка «WAN IP») или оставьте пустым', {
				done: !!genPeer.trim() || !!generatedLink,
				pending: !step1Done
			}),
			...(vkFilled
				? [
						guide('vk', 'VK-хеши указаны — при необходимости отредактируйте', {
							done: true,
							pending: !step1Done
						})
					]
				: [
						guide('vk', 'При необходимости добавьте VK-хеши — это не пароль wdtt://', {
							done: false,
							pending: !step1Done
						})
					]),
			guide('gen', 'Нажмите «Сохранить · запустить · ссылка»', {
				done: !!generatedLink,
				pending: !step1Done
			}),
			guide('copy', 'Скопируйте wdtt:// и передайте клиенту (WDTT «Клиент» → импорт)', {
				done: false,
				pending: !generatedLink,
				active: !!generatedLink
			}),
			guide('start-step', 'Сервер запустится вместе с генерацией ссылки', {
				done: serverStarted,
				pending: !generatedLink
			})
		]);
	});

	const startGuideItems = $derived.by((): WizardGuideItem[] =>
		finalizeGuide([
			guide('ports', portsGuideText, {
				done: step2Done,
				pending: !step1Done
			}),
			guide('start', 'Нажмите «Запустить сервер»', { done: serverStarted, pending: !step2Done })
		])
	);

	$effect(() => {
		if (opsMode) return;
		if (!step1Done && quickActive !== 'secret') quickActive = 'secret';
	});

	/** Запущенный сервер: «Раздача» при смене инстанса, кроме вкладки «Журнал». */
	$effect(() => {
		serverInstanceId;
		untrack(() => {
			if (opsMode && running && opsTab !== 'log') opsTab = 'links';
		});
	});

	onMount(async () => {
		try {
			const segs = await api.listManagedLANSegments();
			lanSegmentOptions = segs.map((s) => ({ value: s.name, label: s.label || s.name }));
		} catch {
			lanSegmentOptions = [];
		}
		try {
			const s = await api.singboxRouterGetSettings();
			const refs = s.ingressInterfaces ?? [];
			server.ingressEnabled = wdttIngressRefs.some((ref) => refs.includes(ref));
		} catch {
			/* ignore */
		}
	});

	async function handleSetNATMode(mode: NatMode) {
		if (mode === natMode) return;
		if (!serverInstanceId) {
			server.natMode = mode;
			await onSave(server);
			return;
		}
		togglingNAT = true;
		try {
			const result = await api.setWdttServerNATMode(serverInstanceId, mode);
			server.natMode = result.config.natMode ?? mode;
			if (onAccessUpdated) {
				await onAccessUpdated(result.config);
			}
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка изменения режима NAT');
		} finally {
			togglingNAT = false;
		}
	}

	async function handlePolicyChange(policy: string) {
		if (policy === (server.policy ?? 'none')) return;
		if (!serverInstanceId) {
			server.policy = policy;
			await onSave(server);
			return;
		}
		policyChanging = true;
		try {
			const result = await api.setWdttServerPolicy(serverInstanceId, policy);
			server.policy = result.config.policy ?? policy;
			if (onAccessUpdated) {
				await onAccessUpdated(result.config);
			}
			notifications.success('Политика обновлена');
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка изменения политики');
		} finally {
			policyChanging = false;
		}
	}

	async function handleSetLANSegments(segments: string[]) {
		if (settingLAN) return;
		if (!serverInstanceId) {
			server.lanSegments = segments;
			await onSave(server);
			return;
		}
		settingLAN = true;
		try {
			const result = await api.setWdttServerLANSegments(serverInstanceId, segments);
			server.lanSegments = result.config.lanSegments ?? segments;
			if (onAccessUpdated) {
				await onAccessUpdated(result.config);
			}
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка изменения доступа в LAN');
		} finally {
			settingLAN = false;
		}
	}

	async function handleStatsLogChange(mode: StatsLogMode) {
		server.statsLog = mode;
		await onSave(server);
		if (running) {
			await onToggle(false);
			await onToggle(true);
			notifications.success('server.log: настройка применена (сервер перезапущен)');
		}
	}

	async function handleToggleIngress(enabled: boolean) {
		togglingIngress = true;
		try {
			await withIngressLock(async () => {
				const s = await api.singboxRouterGetSettings();
				const set = new Set(s.ingressInterfaces ?? []);
				if (enabled) {
					for (const ref of wdttIngressRefs) set.add(ref);
				} else {
					for (const ref of wdttIngressRefs) set.delete(ref);
				}
				const next = [...set];
				await api.singboxRouterPutSettings({ ...s, ingressInterfaces: next });
				server.ingressEnabled = enabled;
			});
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Не удалось изменить sing-box ingress');
		} finally {
			togglingIngress = false;
		}
	}

	function randomPassword() {
		const bytes = new Uint8Array(16);
		crypto.getRandomValues(bytes);
		server.password = Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('');
	}

	async function fillWanPeer() {
		loadingWanPeer = true;
		try {
			const ip = await api.getWANIP();
			genPeer = ip.includes(':') ? ip : `${ip}:${linkPort}`;
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Не удалось определить WAN IP');
		} finally {
			loadingWanPeer = false;
		}
	}

	async function saveOnly() {
		if (!canSave) return;
		await onSave(server);
	}

	async function startOnly() {
		if (!canStart || running) return;
		starting = true;
		try {
			await onSave(server);
			await onToggle(true);
		} finally {
			starting = false;
		}
	}

	async function generateLinkNow(): Promise<boolean> {
		if (!step1Done || generating) return false;
		await onSave(server);
		let peerParam = genPeer.trim();
		if (peerParam && !peerParam.includes(':')) {
			peerParam = `${peerParam}:${listenPort}`;
		}
		const hashes = genVKHashes
			.split(/[,;\s]+/)
			.map((h) => h.trim())
			.filter(Boolean);
		const result = await onGenerate(peerParam, hashes, {
			password: linkPassword.trim() || undefined,
			name: undefined
		});
		if (result?.link) {
			notifications.success('Ссылка wdtt:// сгенерирована');
			return true;
		}
		return false;
	}

	function generateLinkForUser(user: WdttPanelUserEntry) {
		// Пустой linkPassword = «основной пароль сервера» — так подпись под
		// ссылкой остаётся честной, а бэкенд подставляет тот же пароль.
		linkPassword = user.isMainPassword ? '' : user.password;
		if (user.vkHash) genVKHashes = user.vkHash;
		void generateLinkNow();
	}

	async function saveStartAndLink() {
		if (!canStart) return;
		starting = true;
		try {
			await onSave(server);
			if (!running) await onToggle(true);
			let peerParam = genPeer.trim();
			if (peerParam && !peerParam.includes(':')) {
				peerParam = `${peerParam}:${listenPort}`;
			}
			const hashes = genVKHashes
				.split(/[,;\s]+/)
				.map((h) => h.trim())
				.filter(Boolean);
			const result = await onGenerate(peerParam, hashes, {
				password: linkPassword.trim() || undefined,
				name: undefined
			});
			if (result?.link) {
				quickActive = 'link';
				opsTab = 'links';
				notifications.success('Сервер запущен, ссылка wdtt:// готова');
			}
		} finally {
			starting = false;
		}
	}

</script>

<div class="wdtt-server-wrap">
	<p class="wdtt-server-lead">
		WDTT-сервер: DTLS на WAN, WireGuard <code>{wdttIface}</code> и raw <code>{rawServerIface}</code> для
		клиентов. NAT, LAN и политика на вкладке «Сеть» действуют для обоих режимов подключения.
	</p>

	{#if showWizard}
		<ProxyQuickStart
			items={quickItems}
			activeId={quickActive}
			progress={quickProgress}
			meta={statusMeta}
			onSelect={(id) => (quickActive = id)}
			onBack={opsMode ? () => (wizardOpen = false) : undefined}
		>
			{#snippet metaExtra()}
				<ListenPortKillButton listen={server.listen || `0.0.0.0:${listenPort}`} proto="udp" defaultHost="0.0.0.0" />
				<ListenPortKillButton listen={rawListenAddr} proto="udp" defaultHost="0.0.0.0" />
			{/snippet}
			{#snippet content(stepId)}
				{#if stepId === 'secret'}
					<ProxyQuickStartStep
						title="Пароль (-password)"
						hint="WRAP-ключ подключения: попадает в wdtt:// как пароль. Это не VK-хеш."
						primaryLabel="Далее: ссылка"
						primaryDisabled={!step1Done}
						onPrimary={async () => {
							await onSave(server);
							quickActive = 'link';
						}}
					>
						<ProxyWizardGuide items={secretGuideItems} />
						<div class="wdtt-row">
							<SensitiveInput bind:value={server.password} placeholder="секретный пароль" />
							<Button variant="secondary" onclick={randomPassword}>Сгенерировать</Button>
						</div>
						<Toggle
							label="Открыть DTLS-порт в firewall"
							hint="iptables INPUT на Keenetic для WAN-порта"
							checked={server.openFirewall !== false}
							onchange={async (v) => {
								server.openFirewall = v;
								await onSave(server);
							}}
						/>
						<p class="wdtt-hint">
							Сервер слушает WG Direct и Raw одновременно (как qWDTT). Режим клиента — в ссылке
							(<code>mode=raw</code> или WG).
						</p>
						<div class="wdtt-row wdtt-port-row">
							<Input
								label="DTLS-порт (-listen)"
								type="number"
								value={listenPort}
								onchange={applyListenPort}
							/>
							{#if directDiffersFromDTLS}
								<Input
									label="Direct-порт (-listen-direct)"
									type="number"
									value={directListenPort}
									onchange={applyDirectListenPort}
								/>
							{/if}
							<Input
								label="Raw-порт (-listen-raw)"
								type="number"
								value={rawListenPort}
								onchange={applyRawListenPort}
							/>
						</div>
					</ProxyQuickStartStep>
				{:else if stepId === 'link'}
					<ProxyQuickStartStep
						title="Первая ссылка для клиента"
						hint="Ссылку можно получить до запуска сервера. peer = WAN IP : DTLS-порт."
						primaryLabel="Сохранить · запустить · ссылка"
						primaryDisabled={!canStart || generating || starting}
						primaryLoading={generating || starting}
						onPrimary={saveStartAndLink}
					>
						<ProxyWizardGuide items={linkGuideItems} />
						<div class="wdtt-row">
							<Input bind:value={genPeer} placeholder="203.0.113.1:56000 или пусто → WAN" />
							<Button variant="secondary" disabled={loadingWanPeer} onclick={fillWanPeer}>WAN IP</Button>
						</div>
						<Input
							label="VK-хеши (необязательно)"
							bind:value={genVKHashes}
							placeholder="https://vk.com/call/join/… — не пароль wdtt://"
						/>
						<p class="wdtt-hint">
							VK-хеши добавляются в ссылку как <code>vk=…</code> для маскировки. Пароль
							подключения — это поле «Пароль» на шаге 1 (или отдельный пароль клиента в panel.db).
						</p>
						{#if generatedLink || generatedLinkQwdtt}
							<WdttLinkShare linkWdtt={generatedLink} linkQwdtt={generatedLinkQwdtt} />
						{/if}
					</ProxyQuickStartStep>
				{:else}
					<ProxyQuickStartStep
						title="Запуск wdtt-server"
						hint="После получения ссылки поднимите сервер. NAT и LAN — на вкладке «Сеть»."
						primaryLabel="Запустить сервер"
						primaryDisabled={!canStart}
						primaryLoading={starting}
						onPrimary={startOnly}
					>
						<ProxyWizardGuide items={startGuideItems} />
						<p class="wdtt-readonly">
							DTLS: <code>{server.listen || '0.0.0.0:56002'}</code>
							{#if directDiffersFromDTLS}
								· Direct: <code>{server.directListen?.trim()}</code>
							{/if}
							· Raw: <code>{rawListenAddr}</code>
						</p>
					</ProxyQuickStartStep>
				{/if}
			{/snippet}
		</ProxyQuickStart>
	{:else}
		<ProxyInstanceStatusBar
			{running}
			enabled={server.enabled ?? false}
			meta={statusMeta}
			{saving}
			{starting}
			{canSave}
			{canStart}
			showWizardButton={opsMode}
			onOpenWizard={() => (wizardOpen = true)}
			onSave={saveOnly}
			onToggle={onToggle}
		/>

		<ProxyPanelTabs tabs={[...SERVER_TABS]} active={opsTab} onchange={(id) => (opsTab = id as ServerTab)} />

		{#if opsTab === 'main'}
		<section class="ops-section">
				<label class="wdtt-field">
					<span class="section-label">Пароль (-password)</span>
					<div class="wdtt-row">
						<SensitiveInput bind:value={server.password} />
						<Button variant="secondary" size="sm" onclick={randomPassword}>Сгенерировать</Button>
					</div>
				</label>
				<p class="wdtt-hint">
					WG Direct и Raw слушаются одновременно. Режим клиента — в ссылке (<code>mode=raw</code> или WG).
				</p>
				<div class="wdtt-row wdtt-port-row">
					<Input label="DTLS-порт" type="number" value={listenPort} onchange={applyListenPort} />
					{#if directDiffersFromDTLS}
						<Input
							label="Direct-порт"
							type="number"
							value={directListenPort}
							onchange={applyDirectListenPort}
						/>
					{/if}
					<Input label="Raw-порт" type="number" value={rawListenPort} onchange={applyRawListenPort} />
				</div>
				<p class="wdtt-readonly">
					DTLS: <code>{server.listen || '0.0.0.0:56002'}</code>
					<ListenPortKillButton listen={server.listen || `0.0.0.0:${listenPort}`} proto="udp" defaultHost="0.0.0.0" />
					· Raw: <code>{rawListenAddr}</code>
					<ListenPortKillButton listen={rawListenAddr} proto="udp" defaultHost="0.0.0.0" />
				</p>
				<Toggle
					label="Открыть DTLS-порт в firewall"
					checked={server.openFirewall !== false}
					onchange={async (v) => {
						server.openFirewall = v;
						await onSave(server);
					}}
				/>
				{#if isExpert}
					<Input label="Config dir (-config-dir)" bind:value={server.configDir} placeholder="/opt/etc/wdtt-server" />
					<Input label="Admin ID (-admin-id)" bind:value={server.adminId} placeholder="Telegram admin" />
					<SensitiveInput label="Bot token (-bot-token)" bind:value={server.botToken} />
				{/if}
		</section>
		{/if}

		{#if opsTab === 'links'}
		<section class="ops-section">
				<div class="wdtt-row">
					<Input bind:value={genPeer} placeholder="203.0.113.1:56000" />
					<Button variant="secondary" disabled={loadingWanPeer} onclick={fillWanPeer}>WAN IP</Button>
				</div>
				<Input
					label="VK-хеши (необязательно)"
					bind:value={genVKHashes}
					placeholder="https://vk.com/call/join/… — не пароль wdtt://"
				/>
				<p class="wdtt-hint">
					VK-хеши — маскировка в wdtt:// (<code>vk=…</code>), не пароль подключения. Пароль в ссылке:
					{#if linkPassword.trim()}
						отдельный клиент <code>{linkPassword.slice(0, 8)}…</code>
						<Button variant="ghost" size="sm" onclick={() => (linkPassword = '')}>основной</Button>
					{:else}
						основной пароль сервера (шаг «Основное»)
					{/if}
				</p>
				<WdttServerUsers
					{serverInstanceId}
					serverMainPassword={server.password}
					canManage={step1Done}
					onGenerateForUser={generateLinkForUser}
				/>
				<div class="wdtt-actions">
					<Button disabled={!canSave || generating} onclick={generateLinkNow}>Сгенерировать ссылку</Button>
				</div>
				{#if generatedLink || generatedLinkQwdtt}
					<WdttLinkShare linkWdtt={generatedLink} linkQwdtt={generatedLinkQwdtt} />
				{/if}
		</section>
		{/if}

		{#if opsTab === 'network'}
		<section class="ops-section server-detail-card wdtt-access-settings">
				<div class="setting-row">
					<div class="setting-copy">
						<span class="setting-title">WG internal (-wg-port)</span>
						<span class="setting-description">
							Внутренний WireGuard-порт на <code>127.0.0.1</code> — «труба» между декодером UDP
							(DTLS/Direct) и WireGuard. Клиенты с WAN сюда не подключаются; меняйте только при
							конфликте с другим сервисом на роутере.
						</span>
					</div>
					<div class="setting-control">
						<Input
							label="Порт"
							type="number"
							value={wgPortStr}
							onchange={async (v) => {
								applyWgPort(v);
								await onSave(server);
							}}
						/>
					</div>
				</div>
				<div class="setting-row">
					<div class="setting-copy">
						<span class="setting-title">Direct-порт (-listen-direct)</span>
						<span class="setting-description">
							Отдельный WAN-порт для WRAP без DTLS (быстрее WG). Если совпадает с DTLS или пуст —
							не используется; клиенты идут через DTLS-порт.
						</span>
					</div>
					<div class="setting-control">
						<Input
							label="Порт"
							type="number"
							value={directListenPort}
							onchange={async (v) => {
								applyDirectListenPort(v);
								await onSave(server);
							}}
						/>
					</div>
				</div>
				<div class="setting-row">
					<div class="setting-copy">
						<span class="setting-title">NAT</span>
						<span class="setting-description">Режим выхода клиентов в интернет и видимость в LAN.</span>
					</div>
					<div class="setting-control">
						<SegmentedControl
							value={natMode}
							options={natModeOptions}
							ariaLabel="Режим NAT"
							disabled={saving || togglingNAT}
							onchange={handleSetNATMode}
						/>
					</div>
				</div>
				<div class="setting-row">
					<div class="setting-copy">
						<span class="setting-title">Доступ в LAN</span>
					</div>
					<div class="setting-control">
						<ChipMultiSelect
							values={server.lanSegments ?? []}
							options={lanSegmentOptions}
							onchange={handleSetLANSegments}
							disabled={saving || settingLAN}
						/>
					</div>
				</div>
				<div class="setting-row setting-row-toggle">
					<div class="setting-copy">
						<span class="setting-title">Маршрутизация через sing-box</span>
						<span class="setting-description">
							Весь трафик клиентов этого сервера (WireGuard и raw) пойдёт через sing-box и
							маршрутизируется его правилами; в режиме FakeIP их DNS-запросы перехватываются
							резолвером туннеля. Следствия в FakeIP: выше нагрузка на процессор, у клиентов не
							работает ping (ICMP), при остановленном sing-box они остаются без сети.
						</span>
					</div>
					<div class="setting-control setting-control-toggle">
						<Toggle
							checked={!!server.ingressEnabled}
							onchange={handleToggleIngress}
							disabled={togglingIngress}
							spinner="before"
						/>
					</div>
				</div>
				<ServerAccessPolicyDropdown
					policy={server.policy ?? 'none'}
					disabled={saving || policyChanging}
					onchange={handlePolicyChange}
				/>
		</section>
		{/if}

		{#if opsTab === 'log'}
		<section class="ops-section server-detail-card wdtt-access-settings">
				<div class="setting-row">
					<div class="setting-copy">
						<span class="setting-title">Запись server.log</span>
						<span class="setting-description">
							Файл статистики wdtt-server (JSON каждые ~2&nbsp;с). Не путать с логом процесса
							ниже — это stdout сервера для панели. RAM — symlink в <code>/tmp</code>, не изнашивает
							flash; Выкл — <code>/dev/null</code>; Flash — писать в config-dir на накопитель.
						</span>
					</div>
					<div class="setting-control">
						<SegmentedControl
							value={statsLogMode}
							options={statsLogOptions}
							ariaLabel="Режим server.log"
							disabled={saving || starting}
							onchange={handleStatsLogChange}
						/>
					</div>
				</div>
				<div class="wdtt-log-process">
				<ProcessLogBox
					log={status?.log}
					title="Лог процесса (stdout)"
					{instances}
					{selectedInstanceId}
					{onSelectInstance}
				/>
				</div>
		</section>
		{/if}
	{/if}
</div>

<style>
	.wdtt-server-wrap {
		display: flex;
		flex-direction: column;
		gap: 1rem;
	}

	.wdtt-server-lead {
		margin: 0;
		font-size: 0.875rem;
		color: var(--color-text-secondary);
	}

	.ops-section {
		padding: 1rem;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		background: var(--color-bg-secondary);
		display: flex;
		flex-direction: column;
		gap: 0.875rem;
	}

	.wdtt-row {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
		align-items: center;
	}

	.wdtt-actions {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
	}

	.wdtt-readonly {
		margin: 0;
		font-size: 0.875rem;
	}

	.wdtt-field {
		display: flex;
		flex-direction: column;
		gap: 0.375rem;
	}

	.wdtt-hint {
		font-size: 0.75rem;
		color: var(--color-text-secondary);
		margin: 0;
	}

	.wdtt-access-settings {
		gap: 0;
	}

	.wdtt-log-process {
		padding-top: 0.875rem;
		border-top: 1px solid var(--color-border);
	}
</style>
