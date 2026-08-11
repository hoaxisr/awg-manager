<script lang="ts">
	import { Button, Input, Dropdown, Toggle } from '$lib/components/ui';
	import ProcessLogBox from './ProcessLogBox.svelte';
	import LinkParamsSummary from './LinkParamsSummary.svelte';
	import ProxyInstanceStatusBar from '../proxy-panel/ProxyInstanceStatusBar.svelte';
	import ProxyPanelTabs from '../proxy-panel/ProxyPanelTabs.svelte';
	import ProxyQuickStart from '../proxy-panel/ProxyQuickStart.svelte';
	import ProxyQuickStartStep from '../proxy-panel/ProxyQuickStartStep.svelte';
	import ProxyWizardGuide from '../proxy-panel/ProxyWizardGuide.svelte';
	import SensitiveInput from '../proxy-panel/SensitiveInput.svelte';
	import WgConfExportPanel from '../proxy-panel/WgConfExportPanel.svelte';
	import { proxyPanelMode } from '../proxy-panel/modeStore';
	import type { WizardGuideItem } from '../proxy-panel/ProxyWizardGuide.svelte';
	import type { QuickStartItem } from '../proxy-panel/ProxyQuickStart.svelte';
	import { guide, finalizeGuide } from '$lib/utils/proxyWizardGuides';
	import {
		linkHasBundledWg,
		linkedTunnelListenPort,
		patchWgConfEndpoint
	} from '$lib/utils/serverPeerOptions';
	import { dnsModeOptions, platformOptions, modeOptions, transportOptions } from './options';
	import { proxyClientOpsMode } from '$lib/utils/proxyOpsMode';
	import ListenPortKillButton from '../proxy-panel/ListenPortKillButton.svelte';
	import type { FreeTurnClientConfig, FreeTurnLinkPayload, FreeTurnProcessStatus } from '$lib/types';
	import type { LogInstanceItem } from './LogInstanceSwitcher.svelte';

	const CLIENT_TABS = [
		{ id: 'setup', label: 'Настройка' },
		{ id: 'log', label: 'Журнал' }
	] as const;

	type ClientTab = (typeof CLIENT_TABS)[number]['id'];

	interface Props {
		client: FreeTurnClientConfig;
		running?: boolean;
		saving?: boolean;
		status?: FreeTurnProcessStatus;
		routerClock?: string;
		onSave: (cfg: FreeTurnClientConfig) => void | Promise<void>;
		onToggle: (on: boolean) => void | Promise<void>;
		onImportLink: (link: string) => FreeTurnLinkPayload | null | Promise<FreeTurnLinkPayload | null>;
		onImportManualWg?: (wg: string) => void | Promise<void>;
		onImportWgTunnel?: (wg: string) => void | Promise<void>;
		importingWg?: boolean;
		instances?: LogInstanceItem[];
		selectedInstanceId?: string;
		onSelectInstance?: (id: string) => void;
	}

	let {
		client,
		running = false,
		saving = false,
		status,
		routerClock,
		onSave,
		onToggle,
		onImportLink,
		onImportManualWg,
		onImportWgTunnel,
		importingWg = false,
		instances = [],
		selectedInstanceId = '',
		onSelectInstance
	}: Props = $props();

	let importLink = $state('');
	let importing = $state(false);
	let starting = $state(false);
	let linkParams = $state<FreeTurnLinkPayload | null>(null);
	let manualWgConf = $state('');
	let manualWgApplied = $state(false);
	let opsTab = $state<ClientTab>('setup');
	let quickActive = $state('import');
	let wizardOpen = $state(false);

	const linksCount = $derived(
		client.links ? client.links.split(',').filter((s) => s.trim()).length : 0
	);

	const step1Done = $derived(!!client.peer.trim());
	const step2Done = $derived(step1Done && !!client.links?.trim());
	const step3Done = $derived(
		step2Done &&
			client.streams > 0 &&
			client.streamsPerCred > 0 &&
			!!client.platform &&
			!!client.dnsMode
	);
	const canSave = $derived((step1Done || step2Done) && !saving && !starting);
	const canStart = $derived(step3Done && !saving && !starting);

	const opsMode = $derived(
		proxyClientOpsMode({
			running,
			startedAt: status?.startedAt,
			enabled: client.enabled,
			setupComplete: step3Done
		})
	);
	const isExpert = $derived($proxyPanelMode === 'expert');
	const showWizard = $derived((!opsMode && !isExpert) || (opsMode && wizardOpen));

	const bundledWgRaw = $derived.by(() =>
		linkHasBundledWg(linkParams?.wg) ? linkParams!.wg!.trim() : ''
	);

	const displayWgConf = $derived.by(() => {
		if (!bundledWgRaw) return '';
		const port = linkedTunnelListenPort(client.listen, linkParams?.listen);
		if (port == null) return bundledWgRaw;
		return patchWgConfEndpoint(bundledWgRaw, port);
	});

	const wgExportFilename = $derived.by(() => {
		const base = (linkParams?.name || client.peer.split(':')[0] || 'freeturn-client').trim();
		return `${base.replace(/[^\w.-]+/g, '_')}.conf`;
	});

	const canImportWgTunnel = $derived(
		!!displayWgConf && linkedTunnelListenPort(client.listen, linkParams?.listen) != null
	);

	const quickItems = $derived<QuickStartItem[]>([
		{ id: 'import', label: 'freeturn:// с сервера', done: step1Done },
		{ id: 'links', label: 'VK Calls (-links)', done: step2Done },
		{ id: 'streams', label: 'Потоки и браузер', done: step3Done },
		{ id: 'start', label: 'Запуск клиента', done: running }
	]);

	const quickDoneCount = $derived(quickItems.filter((i) => i.done).length);
	const listenMeta = $derived(client.listen?.trim() || '127.0.0.1:9000');
	const showManualWg = $derived(
		!!linkParams && !linkHasBundledWg(linkParams.wg) && !manualWgApplied
	);

	async function loadWgFile(file: File) {
		try {
			manualWgConf = await file.text();
		} catch {
			manualWgConf = '';
		}
	}

	async function applyManualWg() {
		const wg = manualWgConf.trim();
		if (!wg || !onImportManualWg) return;
		await onImportManualWg(wg);
		manualWgApplied = true;
	}

	const importGuideItems = $derived.by(() =>
		finalizeGuide([
			guide('paste', 'Вставьте freeturn:// с сервера в поле ниже', { done: !!importLink.trim() || step1Done }),
			guide('import', 'Нажмите «Импорт» — заполнятся peer и параметры', { done: step1Done, pending: !importLink.trim() && !step1Done })
		])
	);

	const linksGuideItems = $derived.by(() =>
		finalizeGuide([
			guide('vk', 'Вставьте VK Calls ссылки (https://vk.com/call/join/…) — по одной на строку', {
				done: step2Done,
				pending: !step1Done
			}),
			guide('next', 'Нажмите «Далее: потоки»', { done: step2Done, pending: !step1Done })
		])
	);

	const streamsGuideItems = $derived.by(() =>
		finalizeGuide([
			guide('streams', 'Укажите число потоков (-n) и на кред (-streams-per-cred)', {
				done: client.streams > 0 && client.streamsPerCred > 0,
				pending: !step2Done
			}),
			guide('platform', 'Выберите режим, транспорт, платформу VK-auth и DNS mode', {
				done: !!client.platform && !!client.dnsMode,
				pending: !step2Done
			}),
			guide('next', 'Нажмите «Далее: запуск»', { done: step3Done, pending: !step2Done })
		])
	);

	const startGuideItems = $derived.by(() =>
		finalizeGuide([
			guide('start', 'Нажмите «Сохранить и запустить» — клиент подключится к серверу', {
				done: running,
				pending: !step3Done
			})
		])
	);

	$effect(() => {
		if (opsMode) return;
		if (!step1Done && quickActive !== 'import') quickActive = 'import';
	});

	async function applyImport() {
		const link = importLink.trim();
		if (!link) return;
		importing = true;
		try {
			const payload = await onImportLink(link);
			linkParams = payload;
			manualWgApplied = false;
			importLink = '';
			const hasWg = linkHasBundledWg(payload?.wg);
			if (client.peer.trim() && (hasWg || step2Done)) {
				quickActive = 'links';
			}
		} finally {
			importing = false;
		}
	}

	async function saveOnly() {
		if (!canSave) return;
		await onSave(client);
	}

	async function saveAndStart() {
		if (!canStart) return;
		starting = true;
		try {
			await onSave(client);
			if (!running) await onToggle(true);
		} finally {
			starting = false;
		}
	}
</script>

<div class="ft-simple-wrap">
	<p class="ft-simple-lead">FreeTurn-клиент: freeturn:// → VK-ссылки → потоки → запуск.</p>

	{#if showWizard}
		<ProxyQuickStart
			items={quickItems}
			activeId={quickActive}
			progress={`Прогресс ${quickDoneCount}/${quickItems.length}`}
			meta={`listen ${listenMeta}`}
			onSelect={(id) => (quickActive = id)}
			onBack={opsMode ? () => (wizardOpen = false) : undefined}
		>
			{#snippet metaExtra()}
				<ListenPortKillButton listen={listenMeta} proto="udp" />
			{/snippet}
			{#snippet content(stepId)}
				{#if stepId === 'import'}
					<ProxyQuickStartStep
						title="Ссылка freeturn://"
						hint="Скопируйте с сервера (шаг «Запуск и freeturn://»)"
						primaryLabel="Далее: VK Calls"
						primaryDisabled={!step1Done}
						onPrimary={() => { quickActive = 'links'; }}
					>
						<ProxyWizardGuide items={importGuideItems} />
						<div class="ft-import-row">
							<Input bind:value={importLink} placeholder="freeturn://…" />
							<Button variant="primary" size="sm" loading={importing} disabled={!importLink.trim()} onclick={applyImport}>
								Импорт
							</Button>
						</div>
						{#if client.peer.trim()}
							<p class="ft-readonly">peer: <code>{client.peer}</code></p>
						{/if}
						<LinkParamsSummary payload={linkParams} peer={client.peer} />
						{#if displayWgConf}
							<WgConfExportPanel
								wgConf={displayWgConf}
								filename={wgExportFilename}
								onImportTunnel={
									onImportWgTunnel || onImportManualWg
										? () => (onImportWgTunnel ?? onImportManualWg)?.(displayWgConf)
										: undefined
								}
								importingTunnel={importingWg}
								importDisabled={!canImportWgTunnel}
							/>
						{/if}
						{#if showManualWg && onImportManualWg}
							<div class="ft-wg-manual">
								<p class="ft-hint">
									В ссылке нет WireGuard-конфига — вставьте клиентский .conf вручную для AWG-туннеля.
								</p>
								<textarea
									class="ft-simple-textarea"
									bind:value={manualWgConf}
									placeholder="[Interface]&#10;PrivateKey = …&#10;[Peer]&#10;…"
									rows="6"
								></textarea>
								<div class="ft-import-row">
									<label class="ft-file-label">
										<input
											type="file"
											accept=".conf,text/plain"
											class="ft-file-input"
											onchange={(e) => {
												const f = (e.currentTarget as HTMLInputElement).files?.[0];
												if (f) void loadWgFile(f);
											}}
										/>
										Загрузить .conf
									</label>
									<Button
										variant="secondary"
										size="sm"
										loading={importingWg}
										disabled={!manualWgConf.trim()}
										onclick={applyManualWg}
									>
										Создать AWG-туннель
									</Button>
								</div>
							</div>
						{/if}
					</ProxyQuickStartStep>
				{:else if stepId === 'links'}
					<ProxyQuickStartStep
						title="VK Calls (-links)"
						hint="Маскировка трафика через VK"
						primaryLabel="Далее: потоки"
						primaryDisabled={!step2Done}
						onPrimary={() => { quickActive = 'streams'; }}
					>
						<ProxyWizardGuide items={linksGuideItems} />
						<textarea class="ft-simple-textarea" bind:value={client.links} placeholder="https://vk.com/call/join/…" rows="4"></textarea>
					</ProxyQuickStartStep>
				{:else if stepId === 'streams'}
					<ProxyQuickStartStep
						title="Потоки и браузер"
						primaryLabel="Далее: запуск"
						primaryDisabled={!step3Done}
						onPrimary={() => { quickActive = 'start'; }}
					>
						<ProxyWizardGuide items={streamsGuideItems} />
						<div class="ft-simple-grid">
							<Input
								label="Потоков (-n)"
								type="number"
								value={String(client.streams)}
								onchange={(v) => (client.streams = Math.max(1, Number(v) || 0))}
							/>
							<Input
								label="На кред (-streams-per-cred)"
								type="number"
								value={String(client.streamsPerCred)}
								onchange={(v) => (client.streamsPerCred = Math.max(1, Number(v) || 0))}
							/>
						</div>
						<Dropdown label="Режим (-mode)" bind:value={client.mode} options={modeOptions} />
						<Dropdown label="Транспорт (-transport)" bind:value={client.transport} options={transportOptions} />
						<div class="ft-simple-grid">
							<Dropdown label="Платформа (-platform)" bind:value={client.platform} options={platformOptions} />
							<Dropdown label="DNS mode (-dns-mode)" bind:value={client.dnsMode} options={dnsModeOptions} />
						</div>
						<Input
							label="DNS servers (-dns-servers)"
							bind:value={client.dnsServers}
							placeholder="77.88.8.8,8.8.8.8 — пусто = встроенные"
						/>
					</ProxyQuickStartStep>
				{:else}
					<ProxyQuickStartStep
						title="Запуск"
						primaryLabel={running ? 'Работает' : 'Сохранить и запустить'}
						primaryDisabled={!canStart || running}
						primaryLoading={starting}
						onPrimary={saveAndStart}
					>
						<ProxyWizardGuide items={startGuideItems} />
					</ProxyQuickStartStep>
				{/if}
			{/snippet}
		</ProxyQuickStart>
	{:else}
		<ProxyInstanceStatusBar
			{running}
			enabled={client.enabled ?? false}
			meta={`listen ${listenMeta}`}
			{saving}
			{starting}
			{canSave}
			{canStart}
			showWizardButton={opsMode}
			onOpenWizard={() => (wizardOpen = true)}
			onSave={saveOnly}
			onToggle={onToggle}
		>
			{#snippet metaExtra()}
				<ListenPortKillButton listen={listenMeta} proto="udp" />
			{/snippet}
		</ProxyInstanceStatusBar>
		<ProxyPanelTabs tabs={[...CLIENT_TABS]} active={opsTab} onchange={(id) => (opsTab = id as ClientTab)} />

		{#if opsTab === 'setup'}
			<section class="ops-section">
				{#if isExpert}
					<div class="ft-import-row">
						<Input bind:value={importLink} placeholder="freeturn://…" />
						<Button variant="primary" size="sm" loading={importing} disabled={!importLink.trim()} onclick={applyImport}>
							Импорт
						</Button>
					</div>
				{/if}
				{#if isExpert}
					<Input label="Peer (сервер FT)" bind:value={client.peer} placeholder="95.79.35.140:56000" />
				{:else}
					<p class="ft-readonly">peer: <code>{client.peer}</code></p>
				{/if}
				<textarea class="ft-simple-textarea" bind:value={client.links} rows="3"></textarea>
				<div class="ft-simple-grid">
					<Input
						type="number"
						label="Потоков"
						value={String(client.streams)}
						onchange={(v) => (client.streams = Math.max(1, Number(v) || 0))}
					/>
					<Input
						type="number"
						label="На кред"
						value={String(client.streamsPerCred)}
						onchange={(v) => (client.streamsPerCred = Math.max(1, Number(v) || 0))}
					/>
				</div>
				<Dropdown label="Mode" bind:value={client.mode} options={modeOptions} />
				<Dropdown label="Transport" bind:value={client.transport} options={transportOptions} />
				<div class="ft-simple-grid">
					<Dropdown label="Platform" bind:value={client.platform} options={platformOptions} />
					<Dropdown label="DNS mode" bind:value={client.dnsMode} options={dnsModeOptions} />
				</div>
				<Input
					label="DNS servers"
					bind:value={client.dnsServers}
					placeholder="ip[:port],… — пусто = встроенные"
				/>
				{#if isExpert}
					<div class="ft-simple-grid">
						<Input label="Provider" bind:value={client.provider} />
						<Input label="Client ID" bind:value={client.clientId} />
						<SensitiveInput label="Obf key (-obf-key)" bind:value={client.obfKey} placeholder="64 hex" />
						<Dropdown label="Obf profile" bind:value={client.obfProfile} options={[
							{ value: 'none', label: 'none' },
							{ value: 'rtpopus', label: 'rtpopus' },
							{ value: 'rtpopus2', label: 'rtpopus2' },
							{ value: 'rtpopus3', label: 'rtpopus3' }
						]} />
					</div>
					<Input label="Listen" bind:value={client.listen} placeholder="127.0.0.1:9000" />
					<Input label="URL подписки (-sub)" bind:value={client.sub} />
					<Toggle label="Bond (-bond)" checked={!!client.bond} onchange={(v) => (client.bond = v)} />
					{#if displayWgConf}
						<WgConfExportPanel
							wgConf={displayWgConf}
							filename={wgExportFilename}
							onImportTunnel={
								onImportWgTunnel || onImportManualWg
									? () => (onImportWgTunnel ?? onImportManualWg)?.(displayWgConf)
									: undefined
							}
							importingTunnel={importingWg}
							importDisabled={!canImportWgTunnel}
						/>
					{/if}
					{#if onImportManualWg || onImportWgTunnel}
						<div class="ft-wg-manual">
							<label class="ft-wg-conf-label" for="ft-wg-conf-expert">WG-конфиг клиента</label>
							<textarea
								id="ft-wg-conf-expert"
								class="ft-simple-textarea"
								bind:value={manualWgConf}
								placeholder="[Interface]&#10;PrivateKey = …&#10;[Peer]&#10;…"
								rows="8"
							></textarea>
							<div class="ft-import-row">
								<label class="ft-file-label">
									<input
										type="file"
										accept=".conf,text/plain"
										class="ft-file-input"
										onchange={(e) => {
											const f = (e.currentTarget as HTMLInputElement).files?.[0];
											if (f) void loadWgFile(f);
										}}
									/>
									Загрузить .conf
								</label>
								<Button
									variant="secondary"
									size="sm"
									loading={importingWg}
									disabled={!manualWgConf.trim()}
									onclick={applyManualWg}
								>
									Создать AWG-туннель
								</Button>
							</div>
						</div>
					{/if}
				{/if}
				<p class="ft-hint">
					{linksCount} VK-ссылок · listen <code>{listenMeta}</code>
					<ListenPortKillButton listen={listenMeta} proto="udp" />
					{#if client.dnsMode === 'plain'}
						· DNS: UDP/53 (рекомендуется на роутере)
					{:else if client.dnsMode === 'doh'}
						· DNS: DoH
					{:else}
						· DNS: auto (UDP → DoH при сбое)
					{/if}
				</p>
				<Button variant="secondary" loading={saving} disabled={!canSave} onclick={saveOnly}>Сохранить</Button>
			</section>
		{:else}
			<section class="ops-section">
				<ProcessLogBox
					log={status?.log}
					{routerClock}
					bind:debug={client.debug}
					showDebugToggle
					{instances}
					{selectedInstanceId}
					{onSelectInstance}
				/>
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
	.ft-import-row {
		display: grid;
		grid-template-columns: 1fr auto;
		gap: 0.5rem;
	}
	.ft-simple-textarea {
		width: 100%;
		font-family: var(--font-mono);
		font-size: 0.75rem;
		padding: 0.5rem 0.625rem;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		background: var(--color-bg-primary);
		resize: vertical;
	}
	.ft-simple-grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(10rem, 1fr));
		gap: 0.625rem;
	}
	.ft-readonly {
		margin: 0;
		font-family: var(--font-mono);
		font-size: 0.8125rem;
	}
	.ft-hint {
		font-size: 0.75rem;
		color: var(--color-text-secondary);
		margin: 0;
	}
	.ft-wg-manual {
		margin-top: 0.75rem;
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
	}
	.ft-wg-conf-label {
		font-size: 0.75rem;
		color: var(--color-text-secondary);
	}
	.ft-file-label {
		font-size: 0.75rem;
		cursor: pointer;
		color: var(--color-accent, #3b82f6);
	}
	.ft-file-input {
		display: none;
	}
</style>
