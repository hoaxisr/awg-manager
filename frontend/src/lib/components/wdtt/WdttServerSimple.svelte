<script lang="ts">
	import { onMount, untrack } from 'svelte';
	import { Button, Input, Toggle, SegmentedControl, ChipMultiSelect } from '$lib/components/ui';
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
		onToggle: (on: boolean) => void | Promise<void>;
		onGenerate: (
			peer: string,
			vkHashes: string[],
			opts?: { password?: string; name?: string }
		) => Promise<{ link: string; linkQwdtt?: string; peer: string } | null>;
		instances?: LogInstanceItem[];
		selectedInstanceId?: string;
		onSelectInstance?: (id: string) => void;
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
		onToggle,
		onGenerate,
		instances = [],
		selectedInstanceId = '',
		onSelectInstance
	}: Props = $props();

	const wdttIface = $derived(server.wgIface?.trim() || 'wdtt0');

	let starting = $state(false);
	let loadingWanPeer = $state(false);
	let showPassword = $state(false);
	let linkPassword = $state('');
	let togglingIngress = $state(false);
	let lanSegmentOptions = $state<{ value: string; label: string }[]>([]);
	let opsTab = $state<ServerTab>('main');
	let quickActive = $state('secret');

	const listenPort = $derived.by(() => String(listenPortNumber(server.listen ?? '', 56002)));
	const wgPortStr = $derived(String(server.wgPort || 56001));

	function applyListenPort(portStr: string) {
		const port = Math.max(1, Math.min(65535, Number(portStr) || 56002));
		server.listen = setListenPort(server.listen || '0.0.0.0:56002', port, '0.0.0.0');
	}

	function applyWgPort(portStr: string) {
		server.wgPort = Math.max(1, Math.min(65535, Number(portStr) || 56001));
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
			generatedLink
		})
	);
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
	const isRawRelay = $derived((server.relayMode ?? 'wg') === 'raw');
	const statusMeta = $derived(
		isRawRelay ? `DTLS :${listenPort} · Raw` : `DTLS :${listenPort} · WG :${server.wgPort || 56001}`
	);

	const natMode = $derived((server.natMode || 'full') as NatMode);

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
			guide('ports', isRawRelay
				? `Проверьте DTLS-порт: ${server.listen || '0.0.0.0:56002'} (Raw — WG-порт не используется)`
				: `Проверьте порты: DTLS ${server.listen || '0.0.0.0:56002'}, WG ${server.wgPort || 56001}`, {
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

	/** Запущенный сервер: сразу «Раздача» (при возврате на страницу или смене инстанса). */
	$effect(() => {
		serverInstanceId;
		// Только на смену инстанса: иначе рестарт сервера (running false→true из
		// поллинга) утаскивал бы пользователя с «Сети» или «Журнала».
		untrack(() => {
			if (opsMode && running) opsTab = 'links';
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
			server.ingressEnabled = refs.includes(`iface:${wdttIface}`);
		} catch {
			/* ignore */
		}
	});

	async function handleSetNATMode(mode: NatMode) {
		server.natMode = mode;
		await onSave(server);
	}

	async function handlePolicyChange(policy: string) {
		server.policy = policy;
		await onSave(server);
	}

	async function handleSetLANSegments(segments: string[]) {
		server.lanSegments = segments;
		await onSave(server);
	}

	async function handleToggleIngress(enabled: boolean) {
		togglingIngress = true;
		try {
			await withIngressLock(async () => {
				const s = await api.singboxRouterGetSettings();
				const set = new Set(s.ingressInterfaces ?? []);
				const ref = `iface:${wdttIface}`;
				if (enabled) set.add(ref);
				else set.delete(ref);
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
			genPeer = ip.includes(':') ? ip : `${ip}:${listenPort}`;
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
		linkPassword = user.isMain ? '' : user.password;
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
		WDTT-сервер: DTLS на WAN, WireGuard <code>{wdttIface}</code> для клиентов. NAT и LAN — на вкладке
		«Сеть» (или при первом запуске по ссылке).
	</p>

	{#if !opsMode}
		<ProxyQuickStart
			items={quickItems}
			activeId={quickActive}
			progress={quickProgress}
			meta={statusMeta}
			onSelect={(id) => (quickActive = id)}
		>
			{#snippet metaExtra()}
				<ListenPortKillButton listen={server.listen || `0.0.0.0:${listenPort}`} proto="udp" defaultHost="0.0.0.0" />
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
							<Input
								type={showPassword ? 'text' : 'password'}
								bind:value={server.password}
								placeholder="секретный пароль"
							/>
							<Button variant="secondary" onclick={() => (showPassword = !showPassword)}>
								{showPassword ? 'Скрыть' : 'Показать'}
							</Button>
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
						<SegmentedControl
							ariaLabel="Режим relay сервера"
							value={(server.relayMode ?? 'wg') as 'wg' | 'raw'}
							options={[
								{ value: 'wg', label: 'WG' },
								{ value: 'raw', label: 'Raw' }
							]}
							onchange={(v) => (server.relayMode = v)}
						/>
						<p class="wdtt-hint">
							{#if (server.relayMode ?? 'wg') === 'raw'}
								Raw — без WireGuard на сервере (qWDTT 1.4+). После смены режима перезапустите сервер.
							{:else}
								WG — совместимость с прежними клиентами и AWG-туннелем на роутере.
							{/if}
						</p>
						<div class="wdtt-row wdtt-port-row">
							<Input
								label="DTLS-порт (-listen)"
								type="number"
								value={listenPort}
								onchange={applyListenPort}
							/>
							{#if !isRawRelay}
								<Input
									label="WG-порт (-wg-port)"
									type="number"
									value={wgPortStr}
									onchange={applyWgPort}
								/>
							{/if}
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
							{#if !isRawRelay}
								· WG: <code>{server.wgPort || 56001}</code>
							{:else}
								· Raw (WG-порт не используется)
							{/if}
						</p>
					</ProxyQuickStartStep>
				{/if}
			{/snippet}
		</ProxyQuickStart>
	{:else}
		<ProxyInstanceStatusBar
			{running}
			meta={statusMeta}
			{saving}
			{starting}
			{canSave}
			{canStart}
			onSave={saveOnly}
			onToggle={onToggle}
		/>

		<ProxyPanelTabs tabs={[...SERVER_TABS]} active={opsTab} onchange={(id) => (opsTab = id as ServerTab)} />

		{#if opsTab === 'main'}
		<section class="ops-section">
				<label class="wdtt-field">
					<span class="section-label">Пароль (-password)</span>
					<div class="wdtt-row">
						<Input type={showPassword ? 'text' : 'password'} bind:value={server.password} />
						<Button variant="secondary" size="sm" onclick={() => (showPassword = !showPassword)}>
							{showPassword ? 'Скрыть' : 'Показать'}
						</Button>
						<Button variant="secondary" size="sm" onclick={randomPassword}>Сгенерировать</Button>
					</div>
				</label>
				<SegmentedControl
					ariaLabel="Режим relay сервера"
					value={(server.relayMode ?? 'wg') as 'wg' | 'raw'}
					options={[
						{ value: 'wg', label: 'WG' },
						{ value: 'raw', label: 'Raw' }
					]}
					onchange={(v) => (server.relayMode = v)}
				/>
				<div class="wdtt-row wdtt-port-row">
					<Input label="DTLS-порт" type="number" value={listenPort} onchange={applyListenPort} />
					{#if (server.relayMode ?? 'wg') !== 'raw'}
						<Input label="WG-порт" type="number" value={wgPortStr} onchange={applyWgPort} />
					{/if}
				</div>
				<p class="wdtt-readonly">
					Listen: <code>{server.listen || '0.0.0.0:56002'}</code>
					<ListenPortKillButton listen={server.listen || `0.0.0.0:${listenPort}`} proto="udp" defaultHost="0.0.0.0" />
				</p>
				<Toggle
					label="Открыть DTLS-порт в firewall"
					checked={server.openFirewall !== false}
					onchange={async (v) => {
						server.openFirewall = v;
						await onSave(server);
					}}
				/>
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
						<span class="setting-title">NAT</span>
						<span class="setting-description">Режим выхода клиентов в интернет и видимость в LAN.</span>
					</div>
					<div class="setting-control">
						<SegmentedControl
							value={natMode}
							options={natModeOptions}
							ariaLabel="Режим NAT"
							disabled={saving}
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
							disabled={saving}
						/>
					</div>
				</div>
				<div class="setting-row setting-row-toggle">
					<div class="setting-copy">
						<span class="setting-title">Маршрутизация через sing-box</span>
						<span class="setting-description">
							Весь трафик клиентов этого сервера пойдёт через sing-box и маршрутизируется его
							правилами; в режиме FakeIP их DNS-запросы перехватываются резолвером туннеля.
							Следствия в FakeIP: выше нагрузка на процессор, у клиентов не работает ping (ICMP),
							при остановленном sing-box они остаются без сети.
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
					disabled={saving}
					onchange={handlePolicyChange}
				/>
		</section>
		{/if}

		{#if opsTab === 'log'}
		<section class="ops-section">
				<ProcessLogBox
					log={status?.log}
					{instances}
					{selectedInstanceId}
					{onSelectInstance}
				/>
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
</style>
