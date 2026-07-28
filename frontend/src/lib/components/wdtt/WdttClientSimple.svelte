<script lang="ts">
	import { Button, Input, Dropdown } from '$lib/components/ui';
	import ProcessLogBox from '../freeturn/ProcessLogBox.svelte';
	import ProxyInstanceStatusBar from '../proxy-panel/ProxyInstanceStatusBar.svelte';
	import ProxyPanelTabs from '../proxy-panel/ProxyPanelTabs.svelte';
	import ProxyQuickStart from '../proxy-panel/ProxyQuickStart.svelte';
	import ProxyQuickStartStep from '../proxy-panel/ProxyQuickStartStep.svelte';
	import ProxyWizardGuide from '../proxy-panel/ProxyWizardGuide.svelte';
	import type { WizardGuideItem } from '../proxy-panel/ProxyWizardGuide.svelte';
	import type { QuickStartItem } from '../proxy-panel/ProxyQuickStart.svelte';
	import { guide, finalizeGuide } from '$lib/utils/proxyWizardGuides';
	import type { LogInstanceItem } from '../freeturn/LogInstanceSwitcher.svelte';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { proxyInOpsMode } from '$lib/utils/proxyOpsMode';
	import { peersEqual } from '$lib/utils/wdttPeer';
	import type {
		WdttClientConfig,
		WdttImportPayload,
		WdttLinkDecodeResult,
		WdttProcessStatus,
		WdttSubscriptionPreview
	} from '$lib/types';

	const CLIENT_TABS = [
		{ id: 'setup', label: 'Настройка' },
		{ id: 'log', label: 'Журнал' }
	] as const;

	type ClientTab = (typeof CLIENT_TABS)[number]['id'];

	interface Props {
		client: WdttClientConfig;
		running?: boolean;
		saving?: boolean;
		importing?: boolean;
		status?: WdttProcessStatus;
		routerClock?: string;
		onSave: (cfg: WdttClientConfig) => void | Promise<void>;
		onRevert?: () => void;
		onToggle: (on: boolean) => void | Promise<void>;
		onImportPayload: (
			payload: WdttImportPayload,
			meta?: { subUrl?: string; clientName?: string; andStart?: boolean }
		) => void | Promise<void>;
		onRefreshSubscription?: () => void | Promise<void>;
		refreshingSub?: boolean;
		subscriptionTick?: number;
		onEnsureWg?: () => void | Promise<void>;
		ensuringWg?: boolean;
		instances?: LogInstanceItem[];
		selectedInstanceId?: string;
		onSelectInstance?: (id: string) => void;
	}

	let {
		client,
		running = false,
		saving = false,
		importing = false,
		status,
		routerClock,
		onSave,
		onRevert,
		onToggle,
		onImportPayload,
		onRefreshSubscription,
		refreshingSub = false,
		subscriptionTick = 0,
		onEnsureWg,
		ensuringWg = false,
		instances = [],
		selectedInstanceId = '',
		onSelectInstance
	}: Props = $props();

	let importLink = $state('');
	let starting = $state(false);
	let linkParams = $state<WdttImportPayload | null>(null);
	let subscriptionPreview = $state<WdttSubscriptionPreview | null>(null);
	let selectedProfileIdx = $state(0);
	let loadingSubList = $state(false);
	let subLoadKey = $state('');
	let fileInput: HTMLInputElement | undefined = $state();
	let opsTab = $state<ClientTab>('setup');
	let quickActive = $state('import');

	const hashCount = $derived(
		client.vkHashes ? client.vkHashes.split(',').filter((s) => s.trim()).length : 0
	);

	const profileApplied = $derived(!!client.peer.trim() && !!client.password.trim());
	const step1Done = $derived(profileApplied);
	const step2Done = $derived(profileApplied);
	const step3Done = $derived(step2Done && hashCount > 0 && client.workers > 0);
	const canSave = $derived(profileApplied && !saving && !starting);
	const canStart = $derived(step3Done && !saving && !starting);

	const wizardStepOrder = ['import', 'peer', 'vk', 'start'] as const;
	const wizardStepIdx = $derived(wizardStepOrder.indexOf(quickActive as (typeof wizardStepOrder)[number]));
	const vkFromProfile = $derived(hashCount > 0);

	const importPrimaryLabel = $derived.by(() => {
		if (!profileApplied) return '';
		if (vkFromProfile && client.workers > 0) return 'Далее: запуск';
		return 'Далее: VK-хеши';
	});

	function goAfterImportApply() {
		if (opsMode) return;
		if (vkFromProfile && client.workers > 0) quickActive = 'start';
		else quickActive = 'vk';
	}

	const opsMode = $derived(
		proxyInOpsMode({
			running,
			startedAt: status?.startedAt,
			enabled: client.enabled
		})
	);

	const quickItems = $derived<QuickStartItem[]>([
		{ id: 'import', label: 'Импорт — ссылка или подписка', done: profileApplied },
		{ id: 'peer', label: 'Peer + пароль', done: profileApplied && wizardStepIdx >= 1 },
		{ id: 'vk', label: 'VK-хеши и потоки', done: step3Done && wizardStepIdx >= 2 },
		{ id: 'start', label: 'Запуск клиента', done: running }
	]);

	const quickDoneCount = $derived(quickItems.filter((i) => i.done).length);
	const listenMeta = $derived(client.listen?.trim() || '127.0.0.1:9000');

	const importGuideItems = $derived.by((): WizardGuideItem[] =>
		finalizeGuide([
			guide('paste', 'Вставьте wdtt://, qwdtt://, HTTPS-подписку или файл .qwdtt', {
				done: !!importLink.trim() || profileApplied
			}),
			guide('import', 'Нажмите «Импорт» — для подписки откроется выбор страны/профиля', {
				done: !!subscriptionPreview || profileApplied,
				pending: !importLink.trim() && !profileApplied
			}),
			guide('sub', 'Выберите сервер из подписки и нажмите «Применить и запустить»', {
				done: profileApplied,
				pending: !subscriptionPreview
			}),
			...(profileApplied
				? vkFromProfile
					? [
							guide('next', 'Нажмите «Далее: запуск» — VK-хеши уже в профиле подписки', {
								done: wizardStepIdx >= 3 || running
							})
						]
					: [
							guide('next', 'Нажмите «Далее: VK-хеши»', {
								done: wizardStepIdx >= 2
							})
						]
				: [])
		])
	);

	const peerGuideItems = $derived.by((): WizardGuideItem[] =>
		finalizeGuide([
			guide('peer', 'Проверьте peer (VPS:DTLS-порт) и пароль из wdtt://', { done: step2Done, pending: !step1Done }),
			guide('listen', 'Listen (127.0.0.1:9000) — порт AWG Endpoint на роутере', {
				done: !!client.listen.trim(),
				pending: !step1Done
			}),
			guide('next', 'Нажмите «Далее: VK-хеши»', { done: step2Done, pending: !step1Done })
		])
	);

	const vkGuideItems = $derived.by((): WizardGuideItem[] =>
		finalizeGuide([
			guide('vk', 'Проверьте VK-хеши (через запятую) — маскировка VK Calls', {
				done: hashCount > 0,
				pending: !step2Done
			}),
			guide('workers', 'Укажите число потоков (workers, мин. 12)', {
				done: client.workers > 0,
				pending: !step2Done
			}),
			guide('next', 'Нажмите «Далее: запуск»', { done: step3Done, pending: !step2Done })
		])
	);

	const startGuideItems = $derived.by((): WizardGuideItem[] =>
		finalizeGuide([
			guide('start', 'Нажмите «Сохранить и запустить» — AWG-туннель создастся автоматически', {
				done: running,
				pending: !step3Done
			})
		])
	);

	function syncSelectedProfileIdx() {
		if (!subscriptionPreview || !client.peer?.trim()) return;
		const idx = subscriptionPreview.profiles.findIndex((p) => peersEqual(p.peer, client.peer));
		if (idx >= 0) selectedProfileIdx = idx;
	}

	async function loadSubscriptionFromUrl(url: string) {
		const trimmed = url.trim();
		if (!trimmed) return;
		loadingSubList = true;
		try {
			const decoded = await api.decodeWdttLink(trimmed);
			if (decoded.subscription) {
				subscriptionPreview = decoded.subscription;
				importLink = trimmed;
				syncSelectedProfileIdx();
			} else if (decoded.profile) {
				linkParams = decoded.profile;
				importLink = trimmed;
			}
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Не удалось загрузить подписку');
		} finally {
			loadingSubList = false;
		}
	}

	async function refreshSubscriptionList() {
		const url = client.sub?.trim() || importLink.trim();
		if (!url) {
			notifications.info('Сначала укажите URL подписки');
			return;
		}
		await loadSubscriptionFromUrl(url);
		if (subscriptionPreview) notifications.success('Список серверов обновлён');
	}

	$effect(() => {
		const sub = client.sub?.trim();
		const tick = subscriptionTick;
		if (!sub) return;
		const key = `${sub}\0${tick}`;
		if (key === subLoadKey) return;
		subLoadKey = key;
		if (!importLink.trim()) importLink = sub;
		void loadSubscriptionFromUrl(sub);
	});

	$effect(() => {
		if (opsMode) return;
		if (!profileApplied && quickActive !== 'import' && quickActive !== 'peer') quickActive = 'import';
	});

	function profileLabel(p: WdttImportPayload, idx: number): string {
		const name = p.name?.trim() || `Профиль ${idx + 1}`;
		const peer = p.peer?.split(':')[0] ?? '';
		return peer ? `${name} (${peer})` : name;
	}

	async function applyDecoded(result: WdttLinkDecodeResult, subUrlHint?: string) {
		subscriptionPreview = result.subscription ?? null;
		if (result.subscription && result.subscription.profiles.length > 1) {
			selectedProfileIdx = 0;
			linkParams = result.subscription.profiles[0];
			return;
		}
		const payload = result.profile ?? result.subscription?.profiles[0];
		if (!payload) return;
		subscriptionPreview = null;
		linkParams = payload;
		const subUrl = payload.subUrl || subUrlHint || result.subscription?.subUrl;
		await onImportPayload(payload, {
			subUrl,
			clientName: payload.name || result.subscription?.name
		});
	}

	async function applyImport() {
		const link = importLink.trim();
		if (!link) return;
		try {
			const decoded = await api.decodeWdttLink(link);
			const isSub = /^https?:\/\//i.test(link);
			await applyDecoded(decoded, isSub ? link : undefined);
			if (!decoded.subscription || decoded.subscription.profiles.length <= 1) {
				if (!decoded.subscription) importLink = '';
				if (profileApplied) goAfterImportApply();
			}
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Не удалось разобрать ссылку');
		}
	}

	async function applySelectedProfile() {
		if (!subscriptionPreview) return;
		const idx = selectedProfileIdx;
		const payload = subscriptionPreview.profiles[idx];
		if (!payload) return;
		if (client.peer?.trim() && peersEqual(payload.peer, client.peer)) {
			notifications.info('Этот профиль уже применён');
			if (!running) await onToggle(true);
			return;
		}
		linkParams = payload;
		try {
			await onImportPayload(payload, {
				subUrl: payload.subUrl || subscriptionPreview.subUrl,
				clientName: payload.name,
				andStart: true
			});
			importLink = subscriptionPreview.subUrl || importLink;
			syncSelectedProfileIdx();
			if (!opsMode) goAfterImportApply();
			if (running) await onToggle(false);
			await onToggle(true);
		} catch {
			/* parent notifies */
		}
	}

	async function importFromFile(file: File) {
		const text = (await file.text()).trim();
		if (!text) return;
		importLink = text;
		await applyImport();
	}

	function onFileInputChange(e: Event) {
		const input = e.currentTarget as HTMLInputElement;
		const file = input.files?.[0];
		if (file) void importFromFile(file);
		input.value = '';
	}

	function onDropFile(e: DragEvent) {
		e.preventDefault();
		const file = e.dataTransfer?.files?.[0];
		if (file) void importFromFile(file);
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

<div class="wdtt-simple-wrap">
	<p class="wdtt-simple-lead">
		WDTT-клиент: импорт wdtt://, qwdtt://, .qwdtt или HTTPS-подписки → peer → запуск.
	</p>

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
						title="Ссылка, .qwdtt или подписка"
						hint="wdtt://, qwdtt://, HTTPS _wdtt.json"
						primaryLabel={importPrimaryLabel}
						primaryDisabled={!profileApplied}
						onPrimary={goAfterImportApply}
					>
						<ProxyWizardGuide items={importGuideItems} />
						<!-- svelte-ignore a11y_no_static_element_interactions -->
						<div class="wdtt-drop-zone" ondrop={onDropFile} ondragover={(e) => e.preventDefault()}>
							<div class="wdtt-import-row">
								<Input bind:value={importLink} placeholder="https://…/_wdtt.json, wdtt://…" />
								<Button variant="primary" size="sm" loading={importing} disabled={!importLink.trim()} onclick={applyImport}>
									Импорт
								</Button>
								<Button variant="secondary" size="sm" loading={importing} onclick={() => fileInput?.click()}>
									Файл .qwdtt
								</Button>
							</div>
							<input bind:this={fileInput} type="file" accept=".qwdtt,application/json,text/plain" class="wdtt-file-input" onchange={onFileInputChange} />
						</div>
						{#if subscriptionPreview}
							<div class="wdtt-sub-box">
								<p class="wdtt-sub-title">{subscriptionPreview.name}</p>
								<label class="wdtt-field wdtt-field--tight">
									<span>Сервер из подписки</span>
									<select class="wdtt-sub-select" bind:value={selectedProfileIdx}>
										{#each subscriptionPreview.profiles as p, idx (idx)}
											<option value={idx}>{profileLabel(p, idx)}</option>
										{/each}
									</select>
								</label>
								<div class="wdtt-sub-actions">
									<Button variant="primary" size="sm" loading={importing || starting} onclick={applySelectedProfile}>
										Применить и запустить
									</Button>
								</div>
							</div>
						{/if}
					</ProxyQuickStartStep>
				{:else if stepId === 'peer'}
					<ProxyQuickStartStep
						title="Сервер (peer)"
						hint="VPS:DTLS-порт"
						primaryLabel="Далее: VK-хеши"
						primaryDisabled={!step2Done}
						onPrimary={() => (quickActive = 'vk')}
					>
						<ProxyWizardGuide items={peerGuideItems} />
						<Input bind:value={client.peer} placeholder="1.2.3.4:56000" />
						<label class="wdtt-field">
							<span>Пароль</span>
							<Input type="password" bind:value={client.password} />
						</label>
						<label class="wdtt-field">
							<span>Listen (AWG Endpoint)</span>
							<Input bind:value={client.listen} placeholder="127.0.0.1:9000" />
						</label>
					</ProxyQuickStartStep>
				{:else if stepId === 'vk'}
					<ProxyQuickStartStep
						title="VK-хеши и потоки"
						primaryLabel="Далее: запуск"
						primaryDisabled={!step3Done}
						onPrimary={() => (quickActive = 'start')}
					>
						<ProxyWizardGuide items={vkGuideItems} />
						<Input bind:value={client.vkHashes} placeholder="hash1,hash2" />
						<Input
							type="number"
							value={String(client.workers)}
							onchange={(v) => (client.workers = Math.max(12, Number(v) || 24))}
						/>
						<Dropdown
							label="Капча (-captcha-mode)"
							bind:value={client.captchaMode}
							options={[
								{ value: 'rjs', label: 'rjs (рекомендуется)' },
								{ value: 'auto', label: 'auto' },
								{ value: 'wv', label: 'wv' }
							]}
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
						{#if status?.wgConfig}
							<p class="wdtt-wg-hint">WireGuard-конфиг получен — AWG-туннель создаётся автоматически.</p>
						{/if}
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
			onSave={() => onSave(client)}
			onToggle={onToggle}
		/>
		<ProxyPanelTabs tabs={[...CLIENT_TABS]} active={opsTab} onchange={(id) => (opsTab = id as ClientTab)} />

		{#if opsTab === 'setup'}
			<section class="ops-section">
				{#if client.sub?.trim() || subscriptionPreview}
					<div class="wdtt-sub-box">
						<p class="wdtt-sub-title">Подписка — смена сервера</p>
						{#if subscriptionPreview}
							<label class="wdtt-field wdtt-field--tight">
								<span>Сервер из подписки</span>
								<select class="wdtt-sub-select" bind:value={selectedProfileIdx}>
									{#each subscriptionPreview.profiles as p, idx (idx)}
										<option value={idx}>{profileLabel(p, idx)}</option>
									{/each}
								</select>
							</label>
							<div class="wdtt-sub-actions">
								<Button variant="primary" size="sm" loading={importing || starting} onclick={applySelectedProfile}>
									Применить и запустить
								</Button>
								{#if onRefreshSubscription}
									<Button variant="ghost" size="sm" loading={refreshingSub} onclick={() => onRefreshSubscription?.()}>
										Обновить список
									</Button>
								{/if}
							</div>
						{:else if loadingSubList}
							<p class="wdtt-sub-loading">Загрузка списка серверов…</p>
						{/if}
					</div>
				{/if}
				<Input bind:value={client.peer} placeholder="peer host:port" />
				<Input type="password" bind:value={client.password} />
				<Input bind:value={client.listen} placeholder="127.0.0.1:9000" />
				<Input bind:value={client.vkHashes} placeholder="VK-хеши" />
				<Input
					type="number"
					value={String(client.workers)}
					onchange={(v) => (client.workers = Math.max(12, Number(v) || 24))}
				/>
				<Dropdown label="Капча" bind:value={client.captchaMode} options={[
					{ value: 'rjs', label: 'rjs' },
					{ value: 'auto', label: 'auto' },
					{ value: 'wv', label: 'wv' }
				]} />
				<div class="wdtt-actions">
					<Button variant="secondary" disabled={!canSave || saving} onclick={() => onSave(client)}>Сохранить</Button>
					{#if onRevert}
						<Button variant="ghost" onclick={onRevert}>Отменить</Button>
					{/if}
				</div>
			</section>
		{:else}
			<section class="ops-section">
				<div class="wdtt-actions">
					{#if onEnsureWg && (running || status?.wgConfig)}
						<Button variant="secondary" loading={ensuringWg} onclick={() => onEnsureWg?.()}>
							Создать AWG из лога
						</Button>
					{/if}
				</div>
				<ProcessLogBox log={status?.log} {routerClock} {instances} {selectedInstanceId} {onSelectInstance} />
			</section>
		{/if}
	{/if}
</div>

<style>
	.wdtt-simple-wrap {
		display: flex;
		flex-direction: column;
		gap: 1rem;
	}
	.wdtt-simple-lead {
		margin: 0;
		color: var(--color-text-secondary);
		font-size: 0.875rem;
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
	.wdtt-import-row {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
		align-items: center;
	}
	.wdtt-drop-zone {
		display: flex;
		flex-direction: column;
		gap: 0.35rem;
	}
	.wdtt-file-input {
		display: none;
	}
	.wdtt-field {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
	}
	.wdtt-field--tight {
		margin-top: 0;
	}
	.wdtt-sub-box {
		margin-top: 0.5rem;
		padding: 0.75rem;
		border: 1px solid var(--color-accent-border);
		border-radius: var(--radius-sm);
		background: var(--color-bg-primary);
	}
	.wdtt-sub-title {
		margin: 0 0 0.5rem;
		font-weight: 600;
	}
	.wdtt-sub-loading {
		margin: 0;
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
	}
	.wdtt-sub-select {
		width: 100%;
		padding: 0.5rem;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
	}
	.wdtt-sub-actions {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
		margin-top: 0.5rem;
	}
	.wdtt-actions {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
	}
	.wdtt-wg-hint {
		margin: 0;
		font-size: 0.8125rem;
		color: var(--color-success, #2e7d32);
	}
</style>
