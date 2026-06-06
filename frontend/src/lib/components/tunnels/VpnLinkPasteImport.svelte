<script lang="ts">
	import { onMount } from 'svelte';
	import { Button, ConfirmModal } from '$lib/components/ui';
	import DownloadRouteNote from '$lib/components/downloads/DownloadRouteNote.svelte';
	import DownloadErrorNotice from '$lib/components/downloads/DownloadErrorNotice.svelte';
	import { downloadErrorToText } from '$lib/utils/downloadError';
	import AmneziaConfEditor from './AmneziaConfEditor.svelte';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import type { AmneziaPremiumCountry, AmneziaPremiumIssuedConfig } from '$lib/types';
	import {
		classifyVpnLink,
		decodeVpnLink,
		isVpnLink,
		vpnLinkUnsupportedPortalReason
	} from '$lib/utils/vpnlink';
	import {
		clearStoredPremiumVpnKeyFromStorage,
		isPremiumCountryConfigStale,
		isPremiumCountryIssued,
		premiumActiveDevicesForCountry,
		readStoredPremiumVpnKey,
		savePremiumVpnKeyToStorage
	} from '$lib/utils/amneziaPremiumVpnPaste';

	interface CountryConfigMeta {
		suggestedName?: string;
		countryCode: string;
		countryLabel: string;
	}

	interface Props {
		/** vpn:// ввод */
		value?: string;
		/** Распознанный .conf (клиентская ссылка или выбранная страна Premium) */
		configContent?: string;
		linkPreview?: string;
		/** localStorage для «Запомнить ключ»; без ключа — кнопки сохранения скрыты */
		storageKey?: string;
		variant?: 'page' | 'modal';
		placeholder?: string;
		/** После скачивания конфига страны Premium (импорт / замена — на стороне родителя) */
		oncountryconfig?: (config: string, meta: CountryConfigMeta) => void | Promise<void>;
		/** Клиентская vpn:// с декодированным конфигом */
		onregularconfig?: (meta: { suggestedName?: string }) => void;
		/** Авто-загрузка каталога при монтировании, если есть сохранённый ключ */
		loadStoredKeyOnMount?: boolean;
	}

	let {
		value = $bindable(''),
		configContent = $bindable(''),
		linkPreview = $bindable(''),
		storageKey,
		variant = 'page',
		placeholder = 'Вставьте vpn:// — клиентский конфиг или ключ Amnezia Premium',
		oncountryconfig,
		onregularconfig,
		loadStoredKeyOnMount = true
	}: Props = $props();

	let linkError = $state('');
	let premiumSid = $state('');
	let premiumCountries = $state<AmneziaPremiumCountry[]>([]);
	let premiumIssuedConfigs = $state<AmneziaPremiumIssuedConfig[]>([]);
	let premiumBusy = $state(false);
	let premiumBusyDepth = 0;
	let premiumPickBusy = $state('');
	let premiumError = $state<unknown>(null);
	let premiumReissueConfirm = $state<{ code: string; label: string } | null>(null);
	let premiumConfirmBusy = $state(false);
	let hasStoredPremiumKey = $state(false);

	let vpnDebounceTimer: ReturnType<typeof setTimeout> | null = null;
	let vpnAnalysisGen = 0;

	let showPremiumChrome = $derived.by(() => {
		const raw = value.trim();
		if (!raw || !isVpnLink(raw)) return false;
		return classifyVpnLink(raw) !== 'regular';
	});

	const previewVariant = $derived(variant === 'modal' ? 'modal-preview' : 'preview');

	function resetPremiumCatalogState() {
		premiumSid = '';
		premiumCountries = [];
		premiumIssuedConfigs = [];
	}

	function beginPremiumBusy() {
		premiumBusyDepth++;
		premiumBusy = true;
	}

	function endPremiumBusy() {
		premiumBusyDepth = Math.max(0, premiumBusyDepth - 1);
		premiumBusy = premiumBusyDepth > 0;
	}

	function savePremiumVpnKey() {
		const key = value.trim();
		if (!storageKey) return;
		if (!key) {
			notifications.error('Вставьте ключ vpn://');
			return;
		}
		if (!isVpnLink(key)) {
			notifications.error('Ожидается ссылка вида vpn://…');
			return;
		}
		if (classifyVpnLink(key) === 'regular') {
			notifications.error('Сохраняется только ключ Amnezia Premium');
			return;
		}
		const portalBlock = vpnLinkUnsupportedPortalReason(key);
		if (portalBlock) {
			notifications.error(portalBlock);
			return;
		}
		try {
			savePremiumVpnKeyToStorage(storageKey, key);
			hasStoredPremiumKey = true;
			notifications.success('Ключ сохранён');
		} catch {
			notifications.error('Не удалось сохранить ключ');
		}
	}

	function clearStoredPremiumVpnKey() {
		if (!storageKey) return;
		try {
			clearStoredPremiumVpnKeyFromStorage(storageKey);
		} catch {
			/* ignore */
		}
		hasStoredPremiumKey = false;
		value = '';
		linkError = '';
		linkPreview = '';
		configContent = '';
		premiumError = null;
		premiumBusyDepth = 0;
		premiumBusy = false;
		resetPremiumCatalogState();
		notifications.success('Сохранённый ключ удалён');
	}

	function scheduleVpnPasteAnalysis() {
		if (vpnDebounceTimer) clearTimeout(vpnDebounceTimer);
		vpnDebounceTimer = setTimeout(() => void runVpnPasteAnalysis(), 420);
	}

	/** Немедленный анализ (вкладка / вставка из буфера) */
	export async function analyzeNow() {
		if (vpnDebounceTimer) {
			clearTimeout(vpnDebounceTimer);
			vpnDebounceTimer = null;
		}
		await runVpnPasteAnalysis();
	}

	async function fetchPremiumCatalogForGen(gen: number) {
		const key = value.trim();
		if (!key || !isVpnLink(key)) return;
		if (classifyVpnLink(key) === 'regular') return;

		beginPremiumBusy();
		premiumError = null;
		try {
			const { sid } = await api.amneziaPremiumLogin(key);
			if (gen !== vpnAnalysisGen) return;
			premiumSid = sid;
			const info = await api.amneziaPremiumAccountInfo(sid);
			if (gen !== vpnAnalysisGen) return;
			premiumCountries = info.available_countries ?? [];
			premiumIssuedConfigs = info.issued_configs ?? [];
			if (!premiumCountries.length) {
				notifications.warning('Список стран пуст — проверьте подписку');
			}
		} catch (e) {
			if (gen !== vpnAnalysisGen) return;
			if (e instanceof DOMException && e.name === 'AbortError') return;
			resetPremiumCatalogState();
			premiumError = e;
			notifications.error(downloadErrorToText(e));
		} finally {
			endPremiumBusy();
		}
	}

	async function fetchPremiumCatalog() {
		const gen = ++vpnAnalysisGen;
		await fetchPremiumCatalogForGen(gen);
	}

	async function runVpnPasteAnalysis() {
		if (vpnDebounceTimer !== null) {
			clearTimeout(vpnDebounceTimer);
			vpnDebounceTimer = null;
		}

		const gen = ++vpnAnalysisGen;

		const raw = value.trim();
		linkError = '';
		linkPreview = '';

		if (!raw) {
			resetPremiumCatalogState();
			configContent = '';
			premiumError = null;
			premiumBusyDepth = 0;
			premiumBusy = false;
			return;
		}

		if (!isVpnLink(raw)) {
			resetPremiumCatalogState();
			configContent = '';
			linkError = 'Ожидается ссылка вида vpn://…';
			return;
		}

		const flavor = classifyVpnLink(raw);

		if (flavor === 'regular') {
			resetPremiumCatalogState();
			configContent = '';
			try {
				const result = decodeVpnLink(raw);
				if (gen !== vpnAnalysisGen) return;
				linkPreview = result.config;
				configContent = result.config;
				if (result.name) {
					onregularconfig?.({ suggestedName: result.name });
				}
			} catch (e) {
				configContent = '';
				linkError = e instanceof Error ? e.message : 'Ошибка декодирования';
			}
			return;
		}

		const portalBlock = vpnLinkUnsupportedPortalReason(raw);
		if (portalBlock) {
			resetPremiumCatalogState();
			linkPreview = '';
			configContent = '';
			linkError = portalBlock;
			return;
		}

		linkPreview = '';
		configContent = '';
		resetPremiumCatalogState();
		await fetchPremiumCatalogForGen(gen);
	}

	function requestPremiumCountryPick(code: string, label: string) {
		if (!premiumSid) {
			notifications.error('Сначала загрузите список стран');
			return;
		}
		if (isPremiumCountryIssued(premiumIssuedConfigs, code)) {
			premiumReissueConfirm = { code, label };
			return;
		}
		void downloadPremiumCountry(code, label);
	}

	async function confirmPremiumReissue() {
		const ctx = premiumReissueConfirm;
		if (!ctx || !premiumSid) return;
		premiumConfirmBusy = true;
		try {
			await downloadPremiumCountry(ctx.code, ctx.label);
			premiumReissueConfirm = null;
		} finally {
			premiumConfirmBusy = false;
		}
	}

	async function downloadPremiumCountry(code: string, label: string) {
		premiumPickBusy = code;
		try {
			const { config } = await api.amneziaPremiumDownloadConfig(premiumSid, code);
			configContent = config;
			linkPreview = config;
			if (oncountryconfig) {
				await oncountryconfig(config, {
					suggestedName: label || code.toUpperCase(),
					countryCode: code,
					countryLabel: label
				});
			}
		} catch (e) {
			notifications.error(downloadErrorToText(e));
		} finally {
			premiumPickBusy = '';
		}
	}

	onMount(() => {
		if (!storageKey || !loadStoredKeyOnMount) return;
		const stored = readStoredPremiumVpnKey(storageKey);
		if (!stored) return;
		hasStoredPremiumKey = true;
		value = stored;
		void runVpnPasteAnalysis();
	});
</script>

<div class="vpn-import-stack" class:vpn-import-stack--modal={variant === 'modal'}>
	<textarea
		class="config-textarea vpn-paste-input"
		class:vpn-paste-input--modal={variant === 'modal'}
		bind:value
		oninput={scheduleVpnPasteAnalysis}
		onpaste={() => queueMicrotask(() => void runVpnPasteAnalysis())}
		{placeholder}
		spellcheck="false"
	></textarea>
	{#if showPremiumChrome}
		<DownloadRouteNote text="Список стран и конфигурации Amnezia Premium будут получены через" />
		<p class="premium-cp-note">
			Если распознан ключ для получения параметров подписки, приложение может обратиться к внешнему сервису по вашей инициативе.<br>
			AWG Manager не связан с операторами таких сервисов, не проверяет и не гарантирует ключи, доступность их API а так же стабильность работы данного функционала.<br>
			В рамках использования приложения и связанных решений вы принимаете на себя ответственность за соблюдение законодательства страны, в которой находитесь.<br>
			Данный функционал не является официальной интеграцией и не подлежит технической поддержке.
		</p>
		<div class="premium-actions">
			<Button variant="secondary" size="md" onclick={() => fetchPremiumCatalog()} loading={premiumBusy} disabled={premiumBusy}>
				Обновить список стран
			</Button>
			{#if storageKey}
				{#if hasStoredPremiumKey}
					<Button
						variant="outline-danger"
						size="md"
						onclick={clearStoredPremiumVpnKey}
						disabled={premiumBusy}
					>
						Забыть ключ
					</Button>
				{:else}
					<Button
						variant="secondary"
						size="md"
						onclick={savePremiumVpnKey}
						disabled={!value.trim() || premiumBusy}
					>
						Запомнить ключ
					</Button>
				{/if}
			{/if}
		</div>
	{/if}
	{#if premiumError}
		<div class="link-error">
			<DownloadErrorNotice error={premiumError} />
		</div>
	{/if}
	{#if premiumCountries.length}
		<p class="field-label premium-countries-label">Страны</p>
		<ul class="premium-country-list" role="listbox">
			{#each premiumCountries as c (c.server_country_code)}
				{@const issued = isPremiumCountryIssued(premiumIssuedConfigs, c.server_country_code)}
				{@const stale = isPremiumCountryConfigStale(premiumIssuedConfigs, c.server_country_code)}
				{@const activeDevices = premiumActiveDevicesForCountry(premiumIssuedConfigs, c.server_country_code)}
				<li>
					<button
						type="button"
						class="premium-country-row"
						class:premium-country-row--issued={issued && !stale}
						class:premium-country-row--stale={stale}
						disabled={!!premiumPickBusy}
						onclick={() => requestPremiumCountryPick(c.server_country_code, c.server_country_name)}
					>
						<span class="premium-country-name">{c.server_country_name}</span>
						{#if stale}
							<span
								class="premium-country-stale-badge"
								title="Адрес был изменён, обновите конфигурацию"
							>ТРЕБУЕТСЯ ПЕРЕВЫПУСК</span>
						{:else if issued}
							<span class="premium-country-issued-badge">Выдано</span>
						{/if}
						{#if activeDevices.length > 0}
							<span
								class="premium-country-active-device-badge"
								title="В Amnezia Premium есть активное устройство в этой стране; это не конфигурация для перевыпуска"
							>
								Активное устройство
							</span>
						{/if}
						<span class="premium-country-code">{c.server_country_code.toUpperCase()}</span>
						{#if premiumPickBusy === c.server_country_code}
							<span class="premium-country-spinner">…</span>
						{/if}
					</button>
				</li>
			{/each}
		</ul>
	{/if}
	{#if linkError}
		<p class="link-error">{linkError}</p>
	{/if}
	{#if linkPreview}
		<AmneziaConfEditor bind:value={linkPreview} variant={previewVariant} readonly />
	{/if}
</div>

{#if premiumReissueConfirm}
	<ConfirmModal
		open={true}
		title="Новый файл конфигурации"
		message={`Сгенерировать новый файл конфигурации — «${premiumReissueConfirm.label}»?`}
		secondary="Ранее созданный файл перестанет действовать. Подключиться с его помощью будет невозможно. Сгенерированные файлы конфигурации также учитываются в лимите устройств."
		confirmLabel="Сгенерировать"
		cancelLabel="Отмена"
		variant="danger"
		busy={premiumConfirmBusy}
		onClose={() => {
			if (!premiumConfirmBusy) premiumReissueConfirm = null;
		}}
		onConfirm={confirmPremiumReissue}
	/>
{/if}

<style>
	.vpn-import-stack {
		display: flex;
		flex-direction: column;
		gap: 0;
	}

	.vpn-import-stack--modal .vpn-paste-input--modal {
		border-top: none;
		border-radius: 0 0 8px 8px;
	}

	.config-textarea {
		width: 100%;
		min-height: 100px;
		padding: 12px;
		font-family: monospace;
		font-size: 0.75rem;
		line-height: 1.5;
		background: var(--bg-primary, var(--color-bg-primary));
		border: 1px solid var(--border, var(--color-border));
		color: var(--text-primary, var(--color-text-primary));
		resize: vertical;
	}

	.vpn-paste-input {
		min-height: 100px;
		border-radius: 0 0 8px 8px;
		margin-bottom: 0;
	}

	.config-textarea:focus {
		outline: none;
		border-color: var(--accent, var(--color-accent));
	}

	.config-textarea::placeholder {
		color: var(--text-muted, var(--color-text-muted));
	}

	.link-error {
		font-size: 0.75rem;
		color: var(--error, var(--color-error));
		margin: 8px 0 0;
		padding: 0 2px;
	}

	.field-label {
		display: block;
		font-size: 0.8125rem;
		font-weight: 500;
		color: var(--text-secondary, var(--color-text-secondary));
		margin-bottom: 6px;
	}

	.premium-cp-note {
		font-size: 12px;
		line-height: 1.45;
		color: var(--text-muted, var(--color-text-muted));
		margin: 10px 0 0;
		padding: 8px 12px;
		background: var(--bg-secondary, var(--color-bg-secondary));
		border: 1px solid var(--border, var(--color-border));
		border-radius: 8px;
	}

	.premium-actions {
		display: flex;
		flex-wrap: wrap;
		gap: 10px;
		padding: 12px 0;
	}

	.premium-countries-label {
		margin-top: 8px;
	}

	.premium-country-list {
		list-style: none;
		margin: 0;
		padding: 0;
		max-height: 280px;
		overflow-y: auto;
		border: 1px solid var(--border, var(--color-border));
		border-radius: 8px;
		background: var(--bg-secondary, var(--color-bg-secondary));
	}

	.premium-country-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 12px;
		width: 100%;
		padding: 10px 14px;
		border: none;
		border-bottom: 1px solid var(--border, var(--color-border));
		background: transparent;
		cursor: pointer;
		text-align: left;
		font-size: 14px;
		color: var(--text-primary, var(--color-text-primary));
		transition: background 0.12s;
	}

	.premium-country-row--issued {
		background: var(--color-warning-tint);
		box-shadow: inset 3px 0 0 var(--color-warning);
	}

	.premium-country-row--issued:hover:not(:disabled) {
		background: color-mix(in srgb, var(--color-warning) 20%, var(--color-bg-tertiary));
	}

	.premium-country-issued-badge {
		flex-shrink: 0;
		font-size: 11px;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.02em;
		color: var(--color-warning);
		padding: 2px 8px;
		border-radius: 4px;
		background: color-mix(in srgb, var(--color-warning) 14%, transparent);
		border: 1px solid var(--color-warning-border);
	}

	.premium-country-row--stale {
		background: var(--color-error-tint);
		box-shadow: inset 3px 0 0 var(--color-error);
	}

	.premium-country-row--stale:hover:not(:disabled) {
		background: color-mix(in srgb, var(--color-error) 20%, var(--color-bg-tertiary));
	}

	.premium-country-stale-badge {
		flex-shrink: 0;
		font-size: 11px;
		font-weight: 600;
		letter-spacing: 0.02em;
		color: var(--color-error);
		padding: 2px 8px;
		border-radius: 4px;
		background: color-mix(in srgb, var(--color-error) 14%, transparent);
		border: 1px solid var(--color-error-border);
	}

	.premium-country-list li:last-child .premium-country-row {
		border-bottom: none;
	}

	.premium-country-row:hover:not(:disabled) {
		background: var(--bg-tertiary, var(--color-bg-tertiary));
	}

	.premium-country-row:disabled {
		opacity: 0.65;
		cursor: wait;
	}

	.premium-country-name {
		flex: 1;
		min-width: 0;
	}

	.premium-country-code {
		font-family: monospace;
		font-size: 12px;
		color: var(--text-muted, var(--color-text-muted));
		flex-shrink: 0;
	}

	.premium-country-spinner {
		font-size: 13px;
		color: var(--accent, var(--color-accent));
		flex-shrink: 0;
	}

	.premium-country-active-device-badge {
		flex-shrink: 0;
		font-size: 11px;
		font-weight: 600;
		letter-spacing: 0.02em;
		color: var(--text-muted, var(--color-text-muted));
		padding: 2px 8px;
		border-radius: 4px;
		background: var(--bg-tertiary, var(--color-bg-tertiary));
		border: 1px solid var(--border, var(--color-border));
	}
</style>
