<script lang="ts">
	import AmneziaConfEditor from './AmneziaConfEditor.svelte';
	import {
		classifyVpnLink,
		decodeVpnLink,
		isVpnLink,
		vpnLinkUnsupportedPortalReason
	} from '$lib/utils/vpnlink';
	import { shouldShowPremiumChrome } from '$lib/utils/amneziaPremiumVpnPaste';

	interface Props {
		/** vpn:// ввод */
		value?: string;
		/** Распознанный .conf клиентской ссылки */
		configContent?: string;
		linkPreview?: string;
		variant?: 'page' | 'modal';
		placeholder?: string;
		/** Клиентская vpn:// с декодированным конфигом */
		onregularconfig?: (meta: { suggestedName?: string }) => void;
	}

	let {
		value = $bindable(''),
		configContent = $bindable(''),
		linkPreview = $bindable(''),
		variant = 'page',
		placeholder = 'Вставьте vpn:// — клиентский конфиг или ключ Amnezia Premium',
		onregularconfig
	}: Props = $props();

	let linkError = $state('');

	let vpnDebounceTimer: ReturnType<typeof setTimeout> | null = null;
	let vpnAnalysisGen = 0;

	let showPremiumChrome = $derived(shouldShowPremiumChrome(value));

	const previewVariant = $derived(variant === 'modal' ? 'modal-preview' : 'preview');

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

	async function runVpnPasteAnalysis() {
		if (vpnDebounceTimer !== null) {
			clearTimeout(vpnDebounceTimer);
			vpnDebounceTimer = null;
		}

		const gen = ++vpnAnalysisGen;

		const raw = value.trim();
		linkError = '';
		linkPreview = '';
		configContent = '';

		if (!raw) return;

		if (!isVpnLink(raw)) {
			linkError = 'Ожидается ссылка вида vpn://…';
			return;
		}

		if (classifyVpnLink(raw) === 'regular') {
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

		// Ключ Amnezia Premium. Отсюда в портал не ходим и ключ никуда не
		// отправляем: подпиской занимается отдельный мастер, у которого ключ
		// и сессия портала не покидают бэкенд.
		const portalBlock = vpnLinkUnsupportedPortalReason(raw);
		if (portalBlock) linkError = portalBlock;
	}
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
		<p class="premium-banner">
			Это ключ Amnezia Premium. Список стран и выдачу конфигураций обслуживает мастер подписки —
			откройте его.
		</p>
		<p class="premium-cp-note">
			Если распознан ключ для получения параметров подписки, приложение может обратиться к внешнему сервису по вашей инициативе.<br>
			AWG Manager не связан с операторами таких сервисов, не проверяет и не гарантирует ключи, доступность их API а так же стабильность работы данного функционала.<br>
			В рамках использования приложения и связанных решений вы принимаете на себя ответственность за соблюдение законодательства страны, в которой находитесь.<br>
			Данный функционал не является официальной интеграцией и не подлежит технической поддержке.
		</p>
	{/if}
	{#if linkError}
		<p class="link-error">{linkError}</p>
	{/if}
	{#if linkPreview}
		<AmneziaConfEditor bind:value={linkPreview} variant={previewVariant} readonly />
	{/if}
</div>

<style>
	.vpn-import-stack {
		display: flex;
		flex-direction: column;
		gap: 0;
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
		border-radius: 8px;
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

	.premium-banner {
		font-size: 0.8125rem;
		line-height: 1.45;
		color: var(--text-secondary, var(--color-text-secondary));
		margin: 10px 0 0;
		padding: 8px 12px;
		background: var(--color-accent-tint);
		border: 1px solid var(--accent, var(--color-accent));
		border-radius: 8px;
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
</style>
