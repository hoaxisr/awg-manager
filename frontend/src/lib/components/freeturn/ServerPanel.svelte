<script lang="ts">
	import { Input, Button, Dropdown } from '$lib/components/ui';
	import { RefreshCw } from 'lucide-svelte';
	import { pluralize } from '$lib/utils/pluralize';
	import type { FreeTurnServerConfig, FreeTurnProcessStatus } from '$lib/types';
	import ProcessAlerts from './ProcessAlerts.svelte';
	import ServerWgBind from './ServerWgBind.svelte';
	import ServerAllowlist from './ServerAllowlist.svelte';
	import SettingRows from './SettingRows.svelte';
	import SettingRow from './SettingRow.svelte';
	import { changedKeys } from './dirty';
	import { modeOptions, obfOptions } from './options';

	interface Props {
		server: FreeTurnServerConfig;
		/** Снапшот сохранённого конфига — для dirty-подсветки и счётчика. */
		saved: FreeTurnServerConfig | null;
		status?: FreeTurnProcessStatus;
		saving: boolean;
		installAvailable: boolean;
		installVersion?: string;
	installedVersion?: string;
	updateAvailable?: boolean;
	remoteVersion?: string;
	remoteCheckError?: string;
	installing: boolean;
	checkingUpdates?: boolean;
		generating: boolean;
		generatedLink: string;
		generatedPeer: string;
		generatedClientId: string;
		genProvider: string;
		genMTU: number;
		genWG: string;
		genClientId: string;
		genName: string;
		expanded: string | null;
		onInstall: () => void;
		onCheckUpdates?: () => void;
		onSave: () => void;
		onRevert: () => void;
		onGenerate: (provider: string, mtu: number, wg: string, clientId: string, name: string) => void;
		onCopy: (text: string) => void;
		defaultClientListenPort?: number;
		serverInstanceId?: string;
	}

	let {
		server,
		saved,
		status,
		saving,
		installAvailable,
		installVersion,
		installedVersion,
		updateAvailable,
		remoteVersion,
		remoteCheckError,
		installing,
		checkingUpdates,
		generating,
		generatedLink,
		generatedPeer,
		generatedClientId,
		genProvider = $bindable(),
		genMTU = $bindable(),
		genWG = $bindable(),
		genClientId = $bindable(),
		genName = $bindable(),
		expanded = $bindable(),
		onInstall,
		onCheckUpdates,
		onSave,
		onRevert,
		onGenerate,
		onCopy,
		defaultClientListenPort = 9000,
		serverInstanceId = 'default'
	}: Props = $props();

	let allowlistPanel: ServerAllowlist | undefined = $state();
	let savingAllowlist = $state(false);

	// WireGuard-конфиг в ссылке — свёрнут по умолчанию, раскрывается если уже заполнен.
	let wgMore = $state(genWG.trim() !== '');

	function randomClientId() {
		const bytes = new Uint8Array(16);
		crypto.getRandomValues(bytes);
		genClientId = Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('');
	}

	async function saveClientToAllowlist() {
		if (!allowlistPanel) return;
		savingAllowlist = true;
		try {
			await allowlistPanel.saveClient(genClientId, genName);
		} finally {
			savingAllowlist = false;
		}
	}

	// -obf-key: 32 байта → 64 hex-символа (#584 — ключ негде было взять).
	function randomObfKey() {
		const bytes = new Uint8Array(32);
		crypto.getRandomValues(bytes);
		server.obfKey = Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('');
	}

	const dirtyKeys = $derived(changedKeys(server, saved));
	// Ссылка собирается бэкендом из СОХРАНЁННОГО конфига — с несохранённым
	// профилем/ключом обфускации в неё молча попали бы старые значения (#584).
	const obfDirty = $derived(dirtyKeys.includes('obfProfile') || dirtyKeys.includes('obfKey'));
	const dirtyCount = $derived(dirtyKeys.length);

	function changed(...keys: (keyof FreeTurnServerConfig)[]): boolean {
		return keys.some((k) => dirtyKeys.includes(k));
	}

	const listenSummary = $derived(`${server.listen || '—'} · ${server.mode}`);

	const forwardSummary = $derived(
		[
			server.connect || '—',
			server.obfProfile,
			`allowlist ${server.clientsFile ? 'вкл' : 'выкл'}`
		].join(' · ')
	);

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

<ServerWgBind
	clientListenPort={defaultClientListenPort}
	onConnect={(addr) => {
		server.connect = addr;
	}}
	onPeerConf={(conf) => {
		genWG = conf;
		wgMore = true;
	}}
/>

<div class="ft-panel-accent">
	<div class="section-label">Ссылка для клиента</div>
	<div class="ft-gen-row">
		<div class="ft-gen-provider">
			<Input label="Провайдер" bind:value={genProvider} placeholder="vk" />
		</div>
		<div class="ft-gen-fields">
			<div class="ft-gen-cid">
				<Input
					label="Client ID"
					bind:value={genClientId}
					placeholder={server.clientsFile ? 'hex ID' : 'необязательно'}
				/>
				<Button variant="ghost" size="sm" onclick={randomClientId} title="Сгенерировать Client ID">
					<RefreshCw size={14} />
				</Button>
			</div>
			<div class="ft-gen-name">
				<Input bind:value={genName} label="Комментарий" placeholder="имя получателя" />
			</div>
			<div class="ft-gen-save">
				<Button
					variant="secondary"
					size="sm"
					loading={savingAllowlist}
					disabled={!genClientId.trim()}
					onclick={saveClientToAllowlist}
					title="Добавить Client ID в allowlist сервера"
				>
					В список
				</Button>
			</div>
			<div class="ft-gen-mtu">
				<Input
					label="MTU"
					type="number"
					value={String(genMTU)}
					onchange={(v) => (genMTU = Number(v) || 1376)}
				/>
			</div>
			<div class="ft-gen-action">
				<Button
					variant="primary"
					size="sm"
					loading={generating}
					disabled={obfDirty}
					onclick={() => onGenerate(genProvider, genMTU, genWG, genClientId, genName)}
				>
					Сгенерировать
				</Button>
			</div>
		</div>
	</div>
	{#if obfDirty}
		<p class="ft-hint">
			Профиль/ключ обфускации изменены, но не сохранены — сначала сохраните настройки,
			иначе в ссылку попадут старые значения
		</p>
	{:else}
		<p class="ft-hint">
			Соберёт freeturn:// ссылку из обфускации/ключа сервера ниже и внешнего IP роутера.
			Провайдер почти всегда <code>vk</code> — менять не нужно.
		</p>
	{/if}
	{#if server.clientsFile}
		<p class="ft-hint">
			Allowlist включён: перед выдачей ссылки нажмите «В список», чтобы сервер принял этот Client ID.
			Список клиентов — в разделе «Форвардинг и доступ» ниже.
		</p>
	{/if}

	<button type="button" class="ft-gen-more" onclick={() => (wgMore = !wgMore)}>
		{wgMore ? '−' : '+'} WireGuard-конфиг в ссылке
	</button>
	{#if wgMore}
		<textarea
			class="field-textarea ft-textarea"
			bind:value={genWG}
			placeholder="Вставьте конфиг WireGuard-клиента, если хотите передать его вместе со ссылкой..."
		></textarea>
		<p class="ft-hint">
			Конфиг (включая приватный ключ) вкладывается в ссылку в открытом виде (base64) —
			передавайте только по защищённому каналу
		</p>
	{/if}

	{#if generatedLink}
		<div class="ft-result">
			<div class="section-label">Готовая ссылка ({generatedPeer})</div>
			<div class="ft-link-box">{generatedLink}</div>
			<Button variant="ghost" size="sm" onclick={() => onCopy(generatedLink)}>
				Скопировать в буфер
			</Button>
			{#if generatedClientId && server.clientsFile}
				<p class="ft-hint" style="margin-top: 0.625rem">
					Убедитесь, что Client ID <code>{generatedClientId}</code> есть в списке ниже — иначе сервер
					отклонит подключение.
				</p>
			{/if}
		</div>
	{/if}
</div>

<SettingRows>
	<SettingRow
		id="listen"
		label="Приём подключений"
		summary={listenSummary}
		dirty={changed('listen', 'mode')}
		expanded={expanded === 'listen'}
		ontoggle={toggleSection}
	>
		<Input label="Слушать (-listen)" bind:value={server.listen} placeholder="0.0.0.0:56000" />
		<Dropdown label="Режим (-mode)" bind:value={server.mode} options={modeOptions} />
	</SettingRow>
	<SettingRow
		id="forward"
		label="Форвардинг и доступ"
		summary={forwardSummary}
		dirty={changed('connect', 'obfProfile', 'obfKey', 'clientsFile')}
		expanded={expanded === 'forward'}
		ontoggle={toggleSection}
	>
		<div class="ft-span">
			<Input label="Backend-адрес (-connect)" bind:value={server.connect} placeholder="127.0.0.1:51820" />
			<p class="ft-hint" style="margin-top: 0.375rem">
				WireGuard — обычно 127.0.0.1:51820, Xray — 127.0.0.1:443
			</p>
		</div>
		<Dropdown label="Профиль (-obf-profile)" bind:value={server.obfProfile} options={obfOptions} />
		<div>
			<Input
				label="Ключ обфускации (-obf-key)"
				type="password"
				bind:value={server.obfKey}
				placeholder="64 hex-символа"
			/>
			<div class="ft-gen-idrow">
				<Button variant="ghost" size="sm" onclick={randomObfKey}>Сгенерировать ключ</Button>
			</div>
		</div>
		<div class="ft-span">
			<ServerAllowlist bind:this={allowlistPanel} {server} serverInstanceId={serverInstanceId} />
		</div>
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
			])} — применятся после перезапуска сервера
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

	.ft-gen-row {
		display: flex;
		gap: 0.625rem;
		align-items: flex-end;
		margin-bottom: 0.5rem;
	}

	.ft-gen-provider {
		flex: 0 0 5.5rem;
		min-width: 0;
	}

	.ft-gen-fields {
		flex: 1;
		min-width: 0;
		display: flex;
		flex-wrap: wrap;
		gap: 0.625rem;
		align-items: flex-end;
	}

	.ft-gen-cid {
		display: flex;
		gap: 0.375rem;
		align-items: flex-end;
		flex: 1 1 11rem;
		min-width: 10rem;
		max-width: 16rem;
	}

	.ft-gen-cid :global(.field) {
		flex: 1;
		min-width: 0;
	}

	.ft-gen-cid :global(.input) {
		font-family: var(--font-mono);
		font-size: 0.75rem;
	}

	.ft-gen-name {
		flex: 0 0 8.5rem;
		min-width: 7rem;
	}

	.ft-gen-mtu {
		flex: 0 0 5.5rem;
		min-width: 5.5rem;
	}

	.ft-gen-save,
	.ft-gen-action {
		flex: 0 0 auto;
		display: flex;
		align-items: flex-end;
		padding-bottom: 0.125rem;
	}

	.ft-gen-action {
		margin-left: auto;
	}

	.ft-gen-more {
		display: block;
		background: none;
		border: none;
		padding: 0;
		margin: 0.625rem 0 0;
		font: inherit;
		font-size: 0.75rem;
		color: var(--color-accent);
		cursor: pointer;
	}

	.ft-gen-more:hover {
		text-decoration: underline;
	}

	.ft-gen-idrow {
		display: flex;
		justify-content: flex-end;
		margin-bottom: 0.5rem;
	}

	.ft-span {
		grid-column: 1 / -1;
		min-width: 0;
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

	.ft-result {
		margin-top: 0.875rem;
		padding: 0.875rem;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-tertiary);
	}

	.ft-link-box {
		font-family: var(--font-mono);
		font-size: 0.8125rem;
		word-break: break-all;
		margin-bottom: 0.625rem;
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

	@media (max-width: 900px) {
		.ft-gen-row {
			flex-direction: column;
			align-items: stretch;
		}

		.ft-gen-provider {
			flex: 0 0 auto;
			max-width: 8rem;
		}

		.ft-gen-cid {
			max-width: none;
		}

		.ft-gen-action {
			margin-left: 0;
		}
	}

	@media (max-width: 640px) {
		.ft-gen-fields {
			flex-direction: column;
			align-items: stretch;
		}

		.ft-gen-name,
		.ft-gen-mtu {
			flex: 1 1 auto;
			max-width: none;
		}
	}
</style>
