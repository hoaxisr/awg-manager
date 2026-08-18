<script lang="ts">
	import { untrack } from 'svelte';
	import { RefreshCw } from 'lucide-svelte';
	import { Button, Input, Dropdown, Toggle } from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { proxyInOpsMode, proxyServerOpsMode } from '$lib/utils/proxyOpsMode';
	import ServerWgBind from './ServerWgBind.svelte';
	import ProcessLogBox from './ProcessLogBox.svelte';
	import FreeturnLinkShare from './FreeturnLinkShare.svelte';
	import ServerAllowlist from './ServerAllowlist.svelte';
	import ProxyInstanceStatusBar from '../proxy-panel/ProxyInstanceStatusBar.svelte';
	import ProxyPanelTabs from '../proxy-panel/ProxyPanelTabs.svelte';
	import ProxyQuickStart from '../proxy-panel/ProxyQuickStart.svelte';
	import ProxyQuickStartStep from '../proxy-panel/ProxyQuickStartStep.svelte';
	import ProxyWizardGuide from '../proxy-panel/ProxyWizardGuide.svelte';
	import type { WizardGuideItem } from '../proxy-panel/ProxyWizardGuide.svelte';
	import type { QuickStartItem } from '../proxy-panel/ProxyQuickStart.svelte';
	import { guide, finalizeGuide } from '$lib/utils/proxyWizardGuides';
	import { obfOptions } from './options';
	import { obfProfileHints, randomObfKeyHex } from './obfHints';
	import { setListenPort, listenPortNumber } from '$lib/utils/listenPortUtils';
	import ListenPortKillButton from '../proxy-panel/ListenPortKillButton.svelte';
	import SensitiveInput from '../proxy-panel/SensitiveInput.svelte';
	import { proxyPanelMode } from '../proxy-panel/modeStore';
	import type { FreeTurnLinkPayload, FreeTurnProcessStatus, FreeTurnServerConfig } from '$lib/types';
	import type { LogInstanceItem } from './LogInstanceSwitcher.svelte';

	const SERVER_TABS = [
		{ id: 'main', label: 'Основное' },
		{ id: 'links', label: 'Раздача' },
		{ id: 'log', label: 'Журнал' }
	] as const;

	type ServerTab = (typeof SERVER_TABS)[number]['id'];

	interface Props {
		server: FreeTurnServerConfig;
		running?: boolean;
		saving?: boolean;
		generating?: boolean;
		status?: FreeTurnProcessStatus;
		defaultClientListenPort?: number;
		serverInstanceId?: string;
		generatedLink?: string;
		generatedPeer?: string;
		genProvider: string;
		genPeer: string;
		genMTU: number;
		genWG: string;
		genClientId: string;
		genName: string;
		onSave: (cfg: FreeTurnServerConfig) => void | Promise<void>;
		onToggle: (on: boolean) => void | Promise<void>;
		onGenerate: (
			provider: string,
			peer: string,
			mtu: number,
			wg: string,
			clientId: string,
			name: string
		) => Promise<import('$lib/types').FreeTurnGenerateLinkResult | null>;
		onCopy: (text: string) => void;
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
		defaultClientListenPort = 9000,
		serverInstanceId = 'default',
		generatedLink = '',
		generatedPeer = '',
		genProvider = $bindable(),
		genPeer = $bindable(),
		genMTU = $bindable(),
		genWG = $bindable(),
		genClientId = $bindable(),
		genName = $bindable(),
		onSave,
		onToggle,
		onGenerate,
		onCopy,
		instances = [],
		selectedInstanceId = '',
		onSelectInstance
	}: Props = $props();

	let starting = $state(false);
	let loadingWanPeer = $state(false);
	let savingAllowlist = $state(false);
	let linkParams = $state<FreeTurnLinkPayload | null>(null);
	let allowlistPanel: ServerAllowlist | undefined = $state();
	let opsTab = $state<ServerTab>('main');
	let quickActive = $state('wg');
	let keeneticPeerSelected = $state(false);
	let wizardOpen = $state(false);

	const listenPort = $derived.by(() => String(listenPortNumber(server.listen ?? '', 56000)));

	function applyListenPort(portStr: string) {
		const port = Math.max(1, Math.min(65535, Number(portStr) || 56000));
		server.listen = setListenPort(server.listen || '0.0.0.0:56000', port, '0.0.0.0');
	}

	const serverListenProto = $derived((server.mode === 'tcp' ? 'tcp' : 'udp') as 'tcp' | 'udp');

	const step1Done = $derived(!!server.connect.trim());
	const step2Done = $derived(step1Done && !!server.listen.trim());
	const obfReady = $derived(
		step2Done && (server.obfProfile !== 'none' ? !!server.obfKey?.trim() : true)
	);
	const wgStepDone = $derived(
		obfReady && (!keeneticPeerSelected || !!genWG.trim())
	);
	const linkReady = $derived(obfReady && !!genWG.trim());
	const canSave = $derived(step1Done && !saving && !starting);
	const canStart = $derived(obfReady && !saving && !starting);

	/** Ops после первого запуска (startedAt) или когда ссылка сгенерирована в этой сессии. */
	const opsMode = $derived(
		proxyServerOpsMode({
			running,
			startedAt: status?.startedAt,
			enabled: server.enabled,
			generatedLink,
			setupComplete: step1Done && obfReady
		})
	);
	const isExpert = $derived($proxyPanelMode === 'expert');
	const showWizard = $derived((!opsMode && !isExpert) || (opsMode && wizardOpen));

	const quickItems = $derived<QuickStartItem[]>([
		{ id: 'wg', label: 'WireGuard · obf', done: wgStepDone },
		{ id: 'launch', label: 'Запуск и freeturn://', done: !!generatedLink && running }
	]);

	const quickDoneCount = $derived(quickItems.filter((i) => i.done).length);
	const obfHint = $derived(obfProfileHints[server.obfProfile] ?? '');

	const wgGuideItems = $derived.by((): WizardGuideItem[] => {
		const peerReady = step1Done;
		const confReady = !!genWG.trim();
		const obfProfileOk = server.obfProfile !== 'none';
		const obfKeyOk = obfProfileOk ? !!server.obfKey?.trim() : true;

		return finalizeGuide([
			guide('peer', 'Выберите WG-сервер и пира в «Сервер · пир»', { done: peerReady }),
			...(keeneticPeerSelected
				? [
						guide('conf', 'Вставьте WG-конфиг клиента Keenetic в поле «WG-конфиг клиента»', {
							done: confReady,
							pending: !peerReady
						})
					]
				: [
						guide('conf', 'Дождитесь автоподстановки конфига пира — он нужен для freeturn://', {
							done: confReady,
							pending: !peerReady
						})
					]),
			guide(
				'endpoint',
				`Проверьте порт Endpoint (${defaultClientListenPort}) — локальный порт FreeTurn-клиента, который смотрит на этот сервер`,
				{ done: peerReady, pending: !peerReady }
			),
			...(obfProfileOk
				? [
						guide('obf-profile', 'Obf-профиль выбран', { done: true, pending: !peerReady }),
						guide('obf-key', 'Нажмите «Сгенерировать ключ» или вставьте obf-ключ (64 hex)', {
							done: obfKeyOk,
							pending: !peerReady || !confReady
						})
					]
				: [
						guide(
							'obf-profile',
							'Выберите obf-профиль (rtpopus3 …) — «none» только для отладки',
							{ done: false, pending: !peerReady }
						)
					]),
			guide('firewall', 'При доступе с WAN включите «Открыть порт в firewall» (по желанию)', {
				done: false,
				pending: !peerReady
			}),
			guide('go', 'Нажмите «Далее: запуск и ссылка»', {
				done: quickActive === 'launch' || !!generatedLink,
				pending: !peerReady || !confReady || (obfProfileOk && !obfKeyOk)
			})
		]);
	});

	const launchGuideItems = $derived.by((): WizardGuideItem[] =>
		finalizeGuide([
			guide('launch', 'Нажмите «Запустить и получить ссылку» — сохранение, запуск сервера и freeturn://', {
				done: !!generatedLink && running,
				pending: !wgStepDone
			}),
			guide('wan', 'Укажите WAN IP (кнопка «WAN IP») или оставьте пустым для авто', {
				done: !!genPeer.trim() || !!generatedLink,
				pending: !wgStepDone
			}),
			guide('copy', 'Скопируйте freeturn:// или откройте QR — передайте на клиент', {
				done: false,
				pending: !generatedLink,
				active: !!generatedLink
			}),
			guide('android', 'Android: FreeTurn app → импорт ссылки/QR → добавьте VK Calls (их нет в ссылке)', {
				done: false,
				pending: !generatedLink,
				active: !!generatedLink
			}),
			guide('client', 'Роутер: вкладка FreeTurn «Клиент» → «Импорт»', {
				done: false,
				pending: !generatedLink,
				active: !!generatedLink
			})
		])
	);

	$effect(() => {
		if (opsMode) return;
		if (!wgStepDone && quickActive !== 'wg') quickActive = 'wg';
	});

	/** Запущенный сервер: «Раздача» при смене инстанса, кроме вкладки «Журнал». */
	$effect(() => {
		serverInstanceId;
		untrack(() => {
			if (opsMode && running && opsTab !== 'log') opsTab = 'links';
		});
	});

	const mainTabNext = $derived(opsMode && running && opsTab === 'main' && step1Done);
	const statusSaveLabel = $derived(mainTabNext ? 'Далее' : 'Сохранить');

	function ensureObfKey() {
		if (server.obfProfile !== 'none' && !server.obfKey?.trim()) {
			server.obfKey = randomObfKeyHex();
		}
	}

	function randomClientId() {
		const bytes = new Uint8Array(16);
		crypto.getRandomValues(bytes);
		genClientId = Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('');
	}

	async function saveClientToAllowlist(silent = false): Promise<boolean> {
		if (!allowlistPanel) return false;
		if (!genClientId.trim()) {
			if (!silent) notifications.error('Укажите Client ID');
			return false;
		}
		savingAllowlist = true;
		try {
			return await allowlistPanel.saveClient(genClientId, genName);
		} finally {
			savingAllowlist = false;
		}
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
		ensureObfKey();
		await onSave(server);
	}

	async function saveAndGoToLinks() {
		await saveOnly();
		if (mainTabNext) opsTab = 'links';
	}

	async function startOnly() {
		if (!canStart || running) return;
		starting = true;
		try {
			ensureObfKey();
			await onSave(server);
			await onToggle(true);
		} finally {
			starting = false;
		}
	}

	async function generateLinkNow(): Promise<boolean> {
		let peerParam = genPeer.trim();
		if (peerParam && !peerParam.includes(':')) {
			peerParam = `${peerParam}:${listenPort}`;
		}
		const result = await onGenerate(genProvider, peerParam, genMTU, genWG, genClientId, genName);
		if (result?.link) {
			try {
				linkParams = await api.decodeFreeTurnLink(result.link);
			} catch {
				linkParams = null;
			}
			return true;
		}
		return false;
	}

	async function saveStartAndLink() {
		if (!canStart || !linkReady) return;
		starting = true;
		try {
			ensureObfKey();
			await onSave(server);
			if (!running) await onToggle(true);
			if (await generateLinkNow()) {
				quickActive = 'launch';
				opsTab = 'links';
				notifications.success('Сервер запущен, ссылка freeturn:// готова');
			}
		} finally {
			starting = false;
		}
	}

	function goLaunchStep() {
		if (!wgStepDone) return;
		quickActive = 'launch';
	}
</script>

<div class="ft-simple-wrap">
	<p class="ft-simple-lead">FreeTurn-сервер: WG-пир → obf → ссылка freeturn:// для клиентов.</p>

	{#if showWizard}
		<ProxyQuickStart
			items={quickItems}
			activeId={quickActive}
			progress={`Прогресс ${quickDoneCount}/${quickItems.length}`}
			meta={`listen :${listenPort}`}
			onSelect={(id) => (quickActive = id)}
			onBack={opsMode ? () => (wizardOpen = false) : undefined}
		>
			{#snippet metaExtra()}
				<ListenPortKillButton listen={server.listen || `0.0.0.0:${listenPort}`} proto={serverListenProto} defaultHost="0.0.0.0" />
			{/snippet}
			{#snippet content(stepId)}
				{#if stepId === 'wg'}
					<ProxyQuickStartStep
						title="WireGuard · obf"
						hint="Backend (-connect) и конфиг пира для ссылки клиента"
						primaryLabel="Далее: запуск и ссылка"
						primaryDisabled={!wgStepDone}
						onPrimary={goLaunchStep}
					>
						<ProxyWizardGuide items={wgGuideItems} />
						<ServerWgBind
							autoApply
							compact
							bind:wgConf={genWG}
							bind:keeneticSelected={keeneticPeerSelected}
							clientListenPort={defaultClientListenPort}
							onConnect={(addr) => {
								server.connect = addr;
							}}
							onPeerConf={(conf) => {
								genWG = conf;
							}}
						/>
						<Dropdown label="Профиль (-obf-profile)" bind:value={server.obfProfile} options={obfOptions} />
						{#if obfHint}
							<p class="ft-hint">{obfHint}</p>
						{/if}
						<SensitiveInput label="Ключ (-obf-key)" bind:value={server.obfKey} placeholder="64 hex" />
						<Button variant="ghost" size="sm" onclick={() => (server.obfKey = randomObfKeyHex())}>
							Сгенерировать ключ
						</Button>
						<Toggle
							label="Открыть порт в firewall"
							checked={server.openFirewall !== false}
							onchange={async (v) => {
								server.openFirewall = v;
								await onSave(server);
							}}
						/>
						<Input
							label="Listen-порт сервера (-listen)"
							type="number"
							value={listenPort}
							onchange={applyListenPort}
							hint="Хост 0.0.0.0 задаётся автоматически; меняется только порт WAN"
						/>
					</ProxyQuickStartStep>
				{:else}
					<ProxyQuickStartStep
						title="Запуск и ссылка freeturn://"
						hint="Порт {server.listen || '0.0.0.0:56000'} назначен автоматически"
						primaryLabel="Запустить и получить ссылку"
						primaryDisabled={!linkReady || !canStart}
						primaryLoading={starting || generating}
						onPrimary={saveStartAndLink}
					>
						{#snippet footer()}
							<Button
								variant="ghost"
								size="sm"
								disabled={!canStart || running}
								loading={starting}
								onclick={startOnly}
							>
								Только запустить сервер
							</Button>
						{/snippet}
						<ProxyWizardGuide items={launchGuideItems} title="Следующий шаг" />
						<div class="ft-gen-peer-row">
							<Input
								label="Peer для клиента (WAN)"
								bind:value={genPeer}
								placeholder={`пусто — авто · :${listenPort}`}
							/>
							<Button variant="ghost" size="sm" loading={loadingWanPeer} onclick={fillWanPeer}>WAN IP</Button>
						</div>
						{#if generatedLink}
							<FreeturnLinkShare link={generatedLink} peer={generatedPeer} payload={linkParams} {onCopy} />
						{:else if !linkReady}
							<p class="ft-hint ft-hint-warn">
								{#if keeneticPeerSelected && !genWG.trim()}
									Вернитесь на шаг 1 и вставьте WG-конфиг клиента Keenetic — без него ссылку не собрать.
								{:else}
									Заполните шаг 1: выберите пира и obf-ключ.
								{/if}
							</p>
						{/if}
					</ProxyQuickStartStep>
				{/if}
			{/snippet}
		</ProxyQuickStart>
	{:else}
		<ProxyInstanceStatusBar
			{running}
			enabled={server.enabled ?? false}
			meta={`listen :${listenPort}`}
			{saving}
			{starting}
			{canSave}
			{canStart}
			saveLabel={statusSaveLabel}
			showWizardButton={opsMode}
			onOpenWizard={() => (wizardOpen = true)}
			onSave={mainTabNext ? saveAndGoToLinks : saveOnly}
			onToggle={onToggle}
		/>
		<ProxyPanelTabs tabs={[...SERVER_TABS]} active={opsTab} onchange={(id) => (opsTab = id as ServerTab)} />

		{#if opsTab === 'main'}
			<section class="ops-section">
				<ServerWgBind
					autoApply
					compact
					bind:wgConf={genWG}
					bind:keeneticSelected={keeneticPeerSelected}
					clientListenPort={defaultClientListenPort}
					onConnect={(addr) => {
						server.connect = addr;
					}}
					onPeerConf={(conf) => {
						genWG = conf;
					}}
				/>
				<Dropdown label="Obf profile" bind:value={server.obfProfile} options={obfOptions} />
				<SensitiveInput label="Obf key" bind:value={server.obfKey} placeholder="64 hex" />
				<p class="ft-readonly">
					Listen: <code>{server.listen || '0.0.0.0:56000'}</code>
					<ListenPortKillButton listen={server.listen || `0.0.0.0:${listenPort}`} proto={serverListenProto} defaultHost="0.0.0.0" />
				</p>
				<Input
					label="Listen-порт сервера"
					type="number"
					value={listenPort}
					onchange={applyListenPort}
				/>
				<Toggle
					label="Firewall"
					checked={server.openFirewall !== false}
					onchange={async (v) => {
						server.openFirewall = v;
						await onSave(server);
					}}
				/>
				{#if isExpert}
					<Input label="Connect (-connect)" bind:value={server.connect} placeholder="peer:port для WG" />
					<Dropdown label="Mode (-mode)" bind:value={server.mode} options={[
						{ value: 'udp', label: 'udp' },
						{ value: 'tcp', label: 'tcp' }
					]} />
				{/if}
				<Button variant="secondary" disabled={!canSave} loading={saving} onclick={saveAndGoToLinks}>
					{mainTabNext ? 'Далее' : 'Сохранить'}
				</Button>
			</section>
		{:else if opsTab === 'links'}
			<section class="ops-section">
				<div class="ft-gen-peer-row">
					<Input label="Peer (WAN)" bind:value={genPeer} />
					<Button variant="ghost" size="sm" loading={loadingWanPeer} onclick={fillWanPeer}>WAN IP</Button>
				</div>
				<div class="ft-gen-row">
					<Input label="Provider" bind:value={genProvider} />
					<Input label="MTU" type="number" value={String(genMTU)} onchange={(v) => (genMTU = Number(v) || 1280)} />
					<div class="ft-gen-cid">
						<Input label="Client ID" bind:value={genClientId} />
						<Button variant="ghost" size="sm" onclick={randomClientId}><RefreshCw size={14} /></Button>
					</div>
					<Input bind:value={genName} label="Комментарий" />
					<Button
						variant="secondary"
						loading={savingAllowlist}
						disabled={!genClientId.trim()}
						onclick={() => saveClientToAllowlist()}
					>
						В список
					</Button>
				</div>
				<div class="ft-simple-actions">
					<Button variant="primary" loading={generating} disabled={!linkReady} onclick={generateLinkNow}>
						Сгенерировать ссылку
					</Button>
				</div>
				{#if generatedLink}
					<FreeturnLinkShare link={generatedLink} peer={generatedPeer} payload={linkParams} {onCopy} />
				{/if}
				<ServerAllowlist bind:this={allowlistPanel} {serverInstanceId} {server} />
			</section>
		{:else}
			<section class="ops-section">
				<ProcessLogBox
					log={status?.log}
					bind:debug={server.debug}
					showDebugToggle
					{instances}
					{selectedInstanceId}
					{onSelectInstance}
				/>
				{#if !running && server.debug}
					<p class="ft-debug-hint">Сохраните настройки и запустите сервер — флаг -debug применяется при старте процесса.</p>
				{/if}
			</section>
		{/if}
	{/if}
</div>

<style>
	.ft-simple-wrap {
		display: flex;
		flex-direction: column;
		gap: 1rem;
	}
	.ft-simple-lead {
		margin: 0;
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
	}
	.ops-section {
		padding: 1rem;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		background: var(--color-bg-secondary);
		display: flex;
		flex-direction: column;
		gap: 0.75rem;
	}
	.ft-hint {
		font-size: 0.75rem;
		color: var(--color-text-secondary);
		margin: 0;
	}
	.ft-hint-warn {
		padding: 0.5rem 0.625rem;
		border-radius: var(--radius-sm);
		border: 1px solid color-mix(in srgb, var(--color-warning, #d97706) 40%, var(--color-border));
		background: color-mix(in srgb, var(--color-warning, #d97706) 8%, var(--color-bg-primary));
	}
	.ft-readonly {
		margin: 0;
		font-family: var(--font-mono);
		font-size: 0.8125rem;
	}
	.ft-gen-peer-row {
		display: flex;
		gap: 0.5rem;
		align-items: flex-end;
	}
	.ft-gen-row {
		display: flex;
		flex-wrap: wrap;
		gap: 0.625rem;
		align-items: flex-end;
	}
	.ft-gen-cid {
		display: flex;
		gap: 0.375rem;
		align-items: flex-end;
	}
	.ft-simple-actions {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
	}
</style>
