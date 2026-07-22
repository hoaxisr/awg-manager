<script lang="ts">
	import { Input, Button, Dropdown, FormToggle } from '$lib/components/ui';
	import { pluralize } from '$lib/utils/pluralize';
	import type { FreeTurnClientConfig, FreeTurnCaptchaClientStatus, FreeTurnProcessStatus } from '$lib/types';
	import ProcessAlerts from './ProcessAlerts.svelte';
	import SettingRows from './SettingRows.svelte';
	import SettingRow from './SettingRow.svelte';
	import { changedKeys } from './dirty';
	import { modeOptions, transportOptions, obfOptions } from './options';

	interface Props {
		client: FreeTurnClientConfig;
		/** Снапшот сохранённого конфига — для dirty-подсветки и счётчика. */
		saved: FreeTurnClientConfig | null;
		status?: FreeTurnProcessStatus;
		captchaStatus?: FreeTurnCaptchaClientStatus | null;
		saving: boolean;
		importing: boolean;
		importedWG: string | null;
	installAvailable: boolean;
	installVersion?: string;
	installedVersion?: string;
	updateAvailable?: boolean;
	remoteVersion?: string;
	remoteCheckError?: string;
	installing: boolean;
	checkingUpdates?: boolean;
		expanded: string | null;
		onInstall: () => void;
		onCheckUpdates?: () => void;
		onSave: () => void;
		onRevert: () => void;
		onImport: (link: string) => void;
		onCopy: (text: string) => void;
		onOpenCaptcha?: () => void;
		captchaAutoOpen?: boolean;
	}

	let {
		client,
		saved,
		status,
		captchaStatus = null,
		saving,
		importing,
		importedWG,
		installAvailable,
		installVersion,
		installedVersion,
		updateAvailable,
		remoteVersion,
		remoteCheckError,
		installing,
		checkingUpdates,
		expanded = $bindable(),
		onInstall,
		onCheckUpdates,
		onSave,
		onRevert,
		onImport,
		onCopy,
		onOpenCaptcha,
		captchaAutoOpen = true
	}: Props = $props();

	let importLink = $state('');

	const dirtyKeys = $derived(changedKeys(client, saved));
	const dirtyCount = $derived(dirtyKeys.length);

	function changed(...keys: (keyof FreeTurnClientConfig)[]): boolean {
		return keys.some((k) => dirtyKeys.includes(k));
	}

	const linksCount = $derived(
		client.links ? client.links.split(',').filter((s) => s.trim()).length : 0
	);

	const turnSummary = $derived(
		[
			client.peer || '—',
			client.provider || '—',
			linksCount ? pluralize(linksCount, ['ссылка', 'ссылки', 'ссылок']) : 'без ссылок'
		].join(' · ')
	);

	const tunnelSummary = $derived(
		[
			`${client.mode} / ${client.transport}`,
			pluralize(client.streams, ['поток', 'потока', 'потоков']),
			client.bond ? 'бондинг' : '',
			client.listen || ''
		]
			.filter(Boolean)
			.join(' · ')
	);

	const obfSummary = $derived(`${client.obfProfile}${client.obfKey ? ' · ключ задан' : ''}`);

	const logLines = $derived(status?.log ? status.log.trim().split('\n').length : 0);

	function toggleSection(id: string) {
		expanded = expanded === id ? null : id;
	}
</script>

<ProcessAlerts
	{status}
	{installAvailable}
	{installVersion}
	{installedVersion}
	{updateAvailable}
	{remoteVersion}
	{remoteCheckError}
	{installing}
	{checkingUpdates}
	{onInstall}
	{onCheckUpdates}
/>

{#if captchaStatus?.waiting && captchaAutoOpen}
	<div class="ft-captcha-banner">
		<span>
			{#if captchaStatus.pendingStreams && captchaStatus.pendingStreams > 1}
				Нужна капча для {captchaStatus.pendingStreams} потоков — решайте по одному, не закрывайте окно.
				{#if captchaStatus.portContention}
					Сейчас порт 8765 занят другим потоком; остальные ждут очереди freeturn.
				{/if}
			{:else if captchaStatus.queued}
				Требуется капча, но сейчас активен другой клиент — дождитесь очереди.
			{:else if captchaStatus.canOpen}
				Требуется прохождение VK-капчи для этого клиента.
			{:else}
				Клиент ждёт решения капчи.
			{/if}
		</span>
		<div class="ft-captcha-banner-actions">
			{#if captchaStatus.canOpen && onOpenCaptcha}
				<Button variant="secondary" size="sm" onclick={onOpenCaptcha}>Открыть капчу</Button>
			{/if}
		</div>
	</div>
{:else if captchaStatus?.waiting && !captchaAutoOpen && onOpenCaptcha}
	<p class="ft-captcha-manual">
		Капча нужна, авто-окно отключено.
		<Button variant="ghost" size="sm" onclick={onOpenCaptcha}>Открыть вручную</Button>
	</p>
{/if}

<div class="ft-panel-accent">
	<div class="section-label">Импорт по ссылке freeturn://</div>
	<div class="ft-import-row">
		<Input bind:value={importLink} placeholder="freeturn://..." />
		<Button variant="primary" size="sm" loading={importing} onclick={() => onImport(importLink)}>
			Применить
		</Button>
	</div>
	<p class="ft-hint">
		Заполнит поля ниже (сохранение — кнопкой «Сохранить») и, если в ссылке есть
		WireGuard-конфиг, сразу создаст из него туннель во вкладке «AWG»
	</p>
	{#if importedWG}
		<div class="section-label">WireGuard-конфиг из ссылки</div>
		<textarea class="field-textarea ft-textarea" readonly value={importedWG}></textarea>
		<Button variant="ghost" size="sm" onclick={() => onCopy(importedWG!)}>Скопировать конфиг</Button>
	{/if}
</div>

<SettingRows>
	<SettingRow
		id="turn"
		label="TURN-сервер и провайдер"
		summary={turnSummary}
		dirty={changed('peer', 'provider', 'links', 'streamsPerCred', 'clientId')}
		expanded={expanded === 'turn'}
		ontoggle={toggleSection}
	>
		<Input label="Адрес сервера (-peer)" bind:value={client.peer} placeholder="vinvanvlad.com:56000" />
		<Input label="Провайдер (-provider)" bind:value={client.provider} placeholder="vk" />
		<div class="ft-span">
			<label class="ft-label" for="ft-c-links">Ссылки VK Calls, через запятую (-links)</label>
			<textarea
				id="ft-c-links"
				class="field-textarea ft-textarea"
				style="min-height: 70px"
				bind:value={client.links}
				placeholder="https://vk.ru/call/join/...,https://vk.ru/call/join/..."
			></textarea>
			<p class="ft-hint">
				Обязательны, если -provider = vk. Несколько ссылок дают несколько независимых пулов
				TURN-кредов — больше суммарных потоков и меньше риск бана одного звонка
			</p>
		</div>
		<Input
			label="Потоков на кред (-streams-per-cred)"
			type="number"
			value={String(client.streamsPerCred)}
			onchange={(v) => (client.streamsPerCred = Number(v) || 0)}
		/>
		<p class="ft-hint ft-span">
			Если авто-капча не проходит, freeturn откроет локальный сервер на порту 8765.
			awg-manager покажет окно прохождения автоматически (включите «Авто-открытие» в модалке).
			При нескольких клиентах порт 8765 один — капчи решаются по очереди.
		</p>
		<div class="ft-span">
			<Input
				label="Client ID (-client-id)"
				bind:value={client.clientId}
				placeholder="оставьте пустым — сгенерируется и сохранится автоматически"
			/>
			<p class="ft-hint" style="margin-top: 0.375rem">
				Заполняется автоматически из ссылки freeturn:// (поле «cid»), если она с ним пришла. Если
				сервер использует allowlist (-clients-file), этот же ID должен быть добавлен там владельцем
				сервера — иначе сервер отклонит подключение с «Unauthorized Client ID»
			</p>
		</div>
	</SettingRow>
	<SettingRow
		id="tunnel"
		label="Туннелирование"
		summary={tunnelSummary}
		dirty={changed('mode', 'transport', 'listen', 'streams', 'bond')}
		expanded={expanded === 'tunnel'}
		ontoggle={toggleSection}
	>
		<Dropdown label="Режим (-mode)" bind:value={client.mode} options={modeOptions} />
		<Dropdown
			label="Транспорт до TURN (-transport)"
			bind:value={client.transport}
			options={transportOptions}
		/>
		<Input label="Локальный адрес (-listen)" bind:value={client.listen} placeholder="127.0.0.1:9000" />
		<Input
			label="Потоков TURN (-n)"
			type="number"
			value={String(client.streams)}
			onchange={(v) => (client.streams = Number(v) || 0)}
		/>
		<div class="ft-toggle-slot ft-span">
			<FormToggle
				bind:checked={client.bond}
				label="Бондинг через все smux-сессии (-bond, только mode=tcp)"
			/>
		</div>
	</SettingRow>
	<SettingRow
		id="obf"
		label="Обфускация"
		summary={obfSummary}
		dirty={changed('obfProfile', 'obfKey')}
		expanded={expanded === 'obf'}
		ontoggle={toggleSection}
	>
		<Dropdown label="Профиль (-obf-profile)" bind:value={client.obfProfile} options={obfOptions} />
		<Input
			label="Ключ обфускации (-obf-key)"
			type="password"
			bind:value={client.obfKey}
			placeholder="64 hex-символа"
		/>
	</SettingRow>
	<SettingRow
		id="log"
		label="Лог процесса"
		summary={logLines ? pluralize(logLines, ['строка', 'строки', 'строк']) : 'пусто'}
		expanded={expanded === 'log'}
		ontoggle={toggleSection}
	>
		<pre class="ft-log-box ft-span">{status?.log || 'лог пуст'}</pre>
	</SettingRow>
</SettingRows>

<div class="ft-footer">
	{#if dirtyCount > 0}
		<span class="ft-dirty-note">
			{pluralize(dirtyCount, [
				'несохранённое изменение',
				'несохранённых изменения',
				'несохранённых изменений'
			])} — применятся после перезапуска клиента
		</span>
		<Button variant="ghost" size="sm" onclick={onRevert}>Отменить</Button>
	{/if}
	<Button variant="primary" size="sm" loading={saving} onclick={onSave}>Сохранить</Button>
</div>

<style>
	.ft-panel-accent {
		padding: 0.875rem 1rem;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-accent-border);
		border-radius: var(--radius);
		margin-bottom: 0.875rem;
	}

	.ft-import-row {
		display: grid;
		grid-template-columns: 1fr auto;
		gap: 0.5rem;
		align-items: center;
		margin-bottom: 0.5rem;
	}

	.ft-span {
		grid-column: 1 / -1;
		min-width: 0;
	}

	.ft-label {
		display: block;
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
		margin-bottom: 0.25rem;
	}

	.ft-toggle-slot {
		display: flex;
		align-items: flex-end;
		padding-bottom: 0.25rem;
	}

	.ft-hint {
		font-size: 0.75rem;
		color: var(--color-text-secondary);
		margin: 0;
	}

	/* Поверх глобального .field-textarea: mono + вертикальный resize. */
	.ft-textarea {
		min-height: 100px;
		font-family: var(--font-mono);
		resize: vertical;
		white-space: pre;
		margin: 0.375rem 0;
	}

	.ft-log-box {
		max-height: 160px;
		overflow-y: auto;
		padding: 0.5rem 0.625rem;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-primary);
		color: var(--color-text-secondary);
		font-family: var(--font-mono);
		font-size: 0.75rem;
		white-space: pre-wrap;
		word-break: break-all;
		margin: 0;
	}

	.ft-footer {
		display: flex;
		align-items: center;
		justify-content: flex-end;
		flex-wrap: wrap;
		gap: 0.625rem;
	}

	.ft-dirty-note {
		font-size: 0.75rem;
		color: var(--color-warning);
	}

	.ft-captcha-banner {
		display: flex;
		align-items: center;
		justify-content: space-between;
		flex-wrap: wrap;
		gap: 0.625rem;
		padding: 0.625rem 0.75rem;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-warning);
		background: var(--color-warning-tint);
		font-size: 0.8125rem;
		margin-bottom: 0.875rem;
		color: var(--color-text-primary);
	}

	.ft-captcha-banner-actions {
		display: inline-flex;
		align-items: center;
		flex-wrap: wrap;
		gap: 0.375rem;
	}

	.ft-captcha-manual {
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: 0.375rem;
		font-size: 0.75rem;
		color: var(--color-text-secondary);
		margin: 0 0 0.875rem;
	}

	@media (max-width: 640px) {
		.ft-import-row {
			grid-template-columns: 1fr;
		}
	}
</style>
