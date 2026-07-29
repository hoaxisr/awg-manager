<script lang="ts">
	import { Button, Input, Dropdown } from '$lib/components/ui';
	import ProcessLogBox from './ProcessLogBox.svelte';
	import LinkParamsSummary from './LinkParamsSummary.svelte';
	import ProxyInstanceStatusBar from '../proxy-panel/ProxyInstanceStatusBar.svelte';
	import ProxyPanelTabs from '../proxy-panel/ProxyPanelTabs.svelte';
	import ProxyQuickStart from '../proxy-panel/ProxyQuickStart.svelte';
	import ProxyQuickStartStep from '../proxy-panel/ProxyQuickStartStep.svelte';
	import ProxyWizardGuide from '../proxy-panel/ProxyWizardGuide.svelte';
	import type { WizardGuideItem } from '../proxy-panel/ProxyWizardGuide.svelte';
	import type { QuickStartItem } from '../proxy-panel/ProxyQuickStart.svelte';
	import { guide, finalizeGuide } from '$lib/utils/proxyWizardGuides';
	import { platformOptions, modeOptions, transportOptions } from './options';
	import { api } from '$lib/api/client';
	import { proxyInOpsMode } from '$lib/utils/proxyOpsMode';
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
		onImportLink: (link: string) => void | Promise<void>;
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
		instances = [],
		selectedInstanceId = '',
		onSelectInstance
	}: Props = $props();

	let importLink = $state('');
	let importing = $state(false);
	let starting = $state(false);
	let linkParams = $state<FreeTurnLinkPayload | null>(null);
	let opsTab = $state<ClientTab>('setup');
	let quickActive = $state('import');

	const linksCount = $derived(
		client.links ? client.links.split(',').filter((s) => s.trim()).length : 0
	);

	const step1Done = $derived(!!client.peer.trim());
	const step2Done = $derived(step1Done && !!client.links?.trim());
	const step3Done = $derived(
		step2Done && client.streams > 0 && client.streamsPerCred > 0 && !!client.platform
	);
	const canSave = $derived((step1Done || step2Done) && !saving && !starting);
	const canStart = $derived(step3Done && !saving && !starting);

	const opsMode = $derived(
		proxyInOpsMode({
			running,
			startedAt: status?.startedAt,
			enabled: client.enabled
		})
	);

	const quickItems = $derived<QuickStartItem[]>([
		{ id: 'import', label: 'freeturn:// с сервера', done: step1Done },
		{ id: 'links', label: 'VK Calls (-links)', done: step2Done },
		{ id: 'streams', label: 'Потоки и браузер', done: step3Done },
		{ id: 'start', label: 'Запуск клиента', done: running }
	]);

	const quickDoneCount = $derived(quickItems.filter((i) => i.done).length);
	const listenMeta = $derived(client.listen?.trim() || '127.0.0.1:9000');

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
			guide('platform', 'Выберите режим, транспорт и платформу VK-auth (-platform)', {
				done: !!client.platform,
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
			await onImportLink(link);
			try {
				linkParams = await api.decodeFreeTurnLink(link);
			} catch {
				linkParams = null;
			}
			importLink = '';
			if (client.peer.trim()) {
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

	{#if !opsMode}
		<ProxyQuickStart
			items={quickItems}
			activeId={quickActive}
			progress={`Прогресс ${quickDoneCount}/${quickItems.length}`}
			meta={`listen ${listenMeta}`}
			onSelect={(id) => (quickActive = id)}
		>
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
						<Dropdown label="Платформа (-platform)" bind:value={client.platform} options={platformOptions} />
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
			meta={`listen ${listenMeta}`}
			{saving}
			{starting}
			{canSave}
			{canStart}
			onSave={saveOnly}
			onToggle={onToggle}
		/>
		<ProxyPanelTabs tabs={[...CLIENT_TABS]} active={opsTab} onchange={(id) => (opsTab = id as ClientTab)} />

		{#if opsTab === 'setup'}
			<section class="ops-section">
				<p class="ft-readonly">peer: <code>{client.peer}</code></p>
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
				<Dropdown label="Platform" bind:value={client.platform} options={platformOptions} />
				<p class="ft-hint">{linksCount} VK-ссылок · listen <code>{listenMeta}</code></p>
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
</style>
