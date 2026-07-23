<script lang="ts">
	import { Button, Input, Dropdown } from '$lib/components/ui';
	import StepPill from '$lib/components/sb-router/StepPill.svelte';
	import WizardStep from '$lib/components/sb-router/WizardStep.svelte';
	import ProcessLogBox from '../freeturn/ProcessLogBox.svelte';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import type {
		WdttClientConfig,
		WdttImportPayload,
		WdttLinkDecodeResult,
		WdttProcessStatus,
		WdttSubscriptionPreview
	} from '$lib/types';

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
		onImportPayload: (payload: WdttImportPayload, meta?: { subUrl?: string; clientName?: string }) => void | Promise<void>;
		onRefreshSubscription?: () => void | Promise<void>;
		refreshingSub?: boolean;
		subscriptionTick?: number;
		onEnsureWg?: () => void | Promise<void>;
		ensuringWg?: boolean;
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
		ensuringWg = false
	}: Props = $props();

	let importLink = $state('');
	let starting = $state(false);
	let linkParams = $state<WdttImportPayload | null>(null);
	let subscriptionPreview = $state<WdttSubscriptionPreview | null>(null);
	let selectedProfileIdx = $state(0);
	let loadingSubList = $state(false);
	let subLoadKey = $state('');
	let fileInput: HTMLInputElement | undefined = $state();

	function peersEqual(a: string, b: string): boolean {
		const norm = (p: string) => {
			const t = p.trim();
			if (!t) return '';
			return t.includes(':') ? t : `${t}:56000`;
		};
		return norm(a) === norm(b);
	}

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
		if (subscriptionPreview) {
			notifications.success('Список серверов обновлён');
		}
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
			await applyDecoded(decoded, isSub ? link.split('?')[0] : undefined);
			if (!decoded.subscription || decoded.subscription.profiles.length <= 1) {
				if (!decoded.subscription) importLink = '';
			}
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Не удалось разобрать ссылку');
		}
	}

	async function applySelectedProfile(andStart = false) {
		if (!subscriptionPreview) return;
		const idx = selectedProfileIdx;
		const payload = subscriptionPreview.profiles[idx];
		if (!payload) return;
		if (client.peer?.trim() && peersEqual(payload.peer, client.peer) && !andStart) {
			notifications.info('Этот профиль уже применён');
			return;
		}
		linkParams = payload;
		try {
			await onImportPayload(payload, {
				subUrl: payload.subUrl || subscriptionPreview.subUrl,
				clientName: payload.name
			});
			importLink = subscriptionPreview.subUrl || importLink;
			syncSelectedProfileIdx();
			if (!andStart) {
				notifications.info('Профиль применён. Для AWG-туннеля нажмите «Применить и запустить» или запустите клиент на шаге 4.');
			}
			if (andStart) {
				if (running) await onToggle(false);
				await onToggle(true);
				notifications.info('Клиент запущен — AWG-туннель создастся после получения WireGuard в логе');
			}
		} catch {
			// parent notifies
		}
	}

	const hashCount = $derived(
		client.vkHashes ? client.vkHashes.split(',').filter((s) => s.trim()).length : 0
	);

	const step1Done = $derived(
		!!importLink.trim() || !!client.peer.trim() || !!subscriptionPreview || !!client.sub?.trim()
	);
	const step2Done = $derived(!!client.peer.trim() && !!client.password.trim());
	const step3Done = $derived(step2Done && hashCount > 0 && client.workers > 0);
	const canSave = $derived((step1Done || step2Done) && !saving && !starting);
	const canStart = $derived(step3Done && !saving && !starting);

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
		Импорт wdtt://, qwdtt://, файла <code>.qwdtt</code> (JSON) или HTTPS-подписки.
	</p>

	<div class="wdtt-simple-steps">
		<StepPill n={1} label="Импорт" active={true} done={step1Done} />
		<StepPill n={2} label="Peer + пароль" active={step1Done} done={step2Done} />
		<StepPill n={3} label="VK-хеши" active={step2Done} done={step3Done} />
		<StepPill n={4} label="Запуск" active={step3Done} done={running} />
	</div>

	<WizardStep n={1} title="Ссылка, .qwdtt или подписка" hint="wdtt://, qwdtt://, HTTPS _wdtt.json" active={true}>
		<!-- svelte-ignore a11y_no_static_element_interactions -->
		<div
			class="wdtt-drop-zone"
			ondrop={onDropFile}
			ondragover={(e) => e.preventDefault()}
		>
			<div class="wdtt-import-row">
				<Input bind:value={importLink} placeholder="https://…/_wdtt.json, wdtt://… или JSON" />
				<Button variant="primary" size="sm" loading={importing} disabled={!importLink.trim()} onclick={applyImport}>
					Импорт
				</Button>
				<Button variant="secondary" size="sm" loading={importing} onclick={() => fileInput?.click()}>
					Файл .qwdtt
				</Button>
			</div>
			<p class="wdtt-drop-hint">или перетащите файл <code>.qwdtt</code> сюда</p>
			<input
				bind:this={fileInput}
				type="file"
				accept=".qwdtt,application/json,text/plain"
				class="wdtt-file-input"
				onchange={onFileInputChange}
			/>
		</div>
		{#if subscriptionPreview}
			<div class="wdtt-sub-box">
				<p class="wdtt-sub-title">{subscriptionPreview.name}</p>
				{#if subscriptionPreview.description}
					<p class="wdtt-sub-desc">{subscriptionPreview.description}</p>
				{/if}
				{#if subscriptionPreview.subUrl || client.sub}
					<p class="wdtt-sub-url" title={subscriptionPreview.subUrl || client.sub}>
						{subscriptionPreview.subUrl || client.sub}
					</p>
				{/if}
				<label class="wdtt-field wdtt-field--tight">
					<span>Сервер из подписки</span>
					<select class="wdtt-sub-select" bind:value={selectedProfileIdx}>
						{#each subscriptionPreview.profiles as p, idx (idx)}
							<option value={idx}>{profileLabel(p, idx)}</option>
						{/each}
					</select>
				</label>
				<div class="wdtt-sub-actions">
					<Button variant="primary" size="sm" loading={importing} onclick={() => applySelectedProfile(false)}>
						Применить профиль
					</Button>
					<Button
						variant="secondary"
						size="sm"
						loading={importing || starting}
						disabled={!canStart && !running}
						onclick={() => applySelectedProfile(true)}
					>
						Применить и запустить
					</Button>
					<Button variant="ghost" size="sm" loading={loadingSubList || refreshingSub} onclick={refreshSubscriptionList}>
						Обновить список
					</Button>
					{#if onRefreshSubscription}
						<Button variant="ghost" size="sm" loading={refreshingSub} onclick={() => onRefreshSubscription?.()}>
							Обновить параметры
						</Button>
					{/if}
				</div>
			</div>
		{:else if client.sub?.trim() && loadingSubList}
			<p class="wdtt-readonly">Загрузка подписки…</p>
		{/if}
		{#if linkParams && !subscriptionPreview}
			<p class="wdtt-readonly">
				peer: <code>{linkParams.peer}</code>
				{#if linkParams.name} · {linkParams.name}{/if}
			</p>
		{/if}
	</WizardStep>

	<WizardStep n={2} title="Сервер (peer)" hint="VPS:DTLS-порт" active={step1Done}>
		<Input bind:value={client.peer} placeholder="1.2.3.4:56000" />
		<label class="wdtt-field">
			<span>Пароль подключения</span>
			<Input type="password" bind:value={client.password} />
		</label>
		<label class="wdtt-field">
			<span>Локальный listen (AWG Endpoint)</span>
			<Input bind:value={client.listen} placeholder="127.0.0.1:9000" />
		</label>
	</WizardStep>

	<WizardStep n={3} title="VK-хеши и потоки" active={step2Done}>
		<label class="wdtt-field">
			<span>VK-хеши (-vk), через запятую</span>
			<Input bind:value={client.vkHashes} placeholder="hash1,hash2" />
		</label>
		<label class="wdtt-field">
			<span>Воркеры (-n, кратно 12)</span>
			<Input
				type="number"
				value={String(client.workers)}
				onchange={(v) => (client.workers = Math.max(12, Number(v) || 24))}
			/>
		</label>
		<label class="wdtt-field">
			<span>Капча (на роутере — rjs)</span>
			<Dropdown label="Режим (-captcha-mode)" bind:value={client.captchaMode} options={[
				{ value: 'rjs', label: 'rjs — Go solver (рекомендуется)' },
				{ value: 'auto', label: 'auto' },
				{ value: 'wv', label: 'wv — WebView (не для роутера)' }
			]} />
		</label>
	</WizardStep>

	<WizardStep n={4} title="Запуск" active={step3Done}>
		<div class="wdtt-actions">
			<Button variant="secondary" disabled={!canSave || saving} loading={saving} onclick={() => onSave(client)}>
				Сохранить
			</Button>
			{#if onRevert}
				<Button variant="ghost" disabled={saving} onclick={onRevert}>Отменить</Button>
			{/if}
			<Button variant="primary" disabled={!canStart || running} loading={starting} onclick={saveAndStart}>
				{running ? 'Работает' : 'Сохранить и запустить'}
			</Button>
			{#if running}
				<Button variant="danger" onclick={() => onToggle(false)}>Остановить</Button>
			{/if}
			{#if onEnsureWg && (running || status?.wgConfig)}
				<Button variant="secondary" loading={ensuringWg} disabled={ensuringWg} onclick={() => onEnsureWg?.()}>
					Создать AWG из лога
				</Button>
			{/if}
		</div>
		{#if status?.wgConfig}
			<p class="wdtt-wg-hint">WireGuard-конфиг получен от сервера — AWG-туннель создаётся автоматически.</p>
		{:else if step3Done && !running}
			<p class="wdtt-wg-hint wdtt-wg-hint--warn">
				AWG-туннель появится после запуска wdtt-client, когда сервер пришлёт WireGuard-конфиг в лог.
				Используйте «Применить и запустить» или кнопку на шаге 4.
			</p>
		{:else if running && !status?.log?.trim()}
			<p class="wdtt-wg-hint wdtt-wg-hint--warn">
				Процесс помечен как запущенный, но лог пуст — нажмите «Остановить», затем «Сохранить и запустить».
			</p>
		{/if}
		<ProcessLogBox log={status?.log} {routerClock} />
	</WizardStep>
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
	}
	.wdtt-simple-steps {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
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
	.wdtt-drop-hint {
		margin: 0;
		font-size: 0.75rem;
		color: var(--color-text-secondary);
	}
	.wdtt-file-input {
		display: none;
	}
	.wdtt-field {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
		margin-top: 0.75rem;
	}
	.wdtt-field--tight {
		margin-top: 0;
	}
	.wdtt-sub-url {
		margin: 0;
		font-family: var(--font-mono);
		font-size: 0.6875rem;
		color: var(--color-text-secondary);
		word-break: break-all;
	}
	.wdtt-sub-actions {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
	}
	.wdtt-sub-box {
		margin-top: 0.75rem;
		padding: 0.75rem;
		border: 1px solid var(--color-accent-border);
		border-radius: var(--radius-sm);
		background: var(--color-bg-secondary);
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
	}
	.wdtt-sub-title {
		margin: 0;
		font-weight: 600;
	}
	.wdtt-sub-desc {
		margin: 0;
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
	}
	.wdtt-sub-select {
		width: 100%;
		padding: 0.5rem;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-primary);
	}
	.wdtt-readonly {
		margin: 0.5rem 0 0;
		font-size: 0.875rem;
		color: var(--color-text-secondary);
	}
	.wdtt-actions {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
		margin-bottom: 0.75rem;
	}
	.wdtt-wg-hint {
		margin: 0 0 0.75rem;
		font-size: 0.8125rem;
		color: var(--color-success, #2e7d32);
	}
	.wdtt-wg-hint--warn {
		color: var(--color-warning, #b8860b);
	}
</style>
