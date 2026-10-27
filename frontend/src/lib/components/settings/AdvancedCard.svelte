<!--
  Карточка «Дополнительно»: API-ключ, кнопка отзыва (develop), ключи Happ и
  NDMS Proxy для sing-box. Ключ выпускает страница (ongenerateApiKey) — он
  меняет общие настройки; NDMS Proxy живёт в сторе sing-box, поэтому его
  переключение — здесь. id="feedback-fab" — цель прокрутки со страницы.
-->
<script lang="ts">
	import { api } from '$lib/api/client';
	import { m } from '$lib/i18n';
	import { Button, ConfirmModal, Toggle } from '$lib/components/ui';
	import RichText from '$lib/components/ui/RichText.svelte';
	import HappKeysModal from '$lib/components/subscriptions/HappKeysModal.svelte';
	import { notifications } from '$lib/stores/notifications';
	import { singboxStatus } from '$lib/stores/singbox';
	import { developFeedbackFabVisible } from '$lib/stores/developFeedbackFab';
	import { copyToClipboard } from '$lib/utils/clipboard';
	import { Wrench } from 'lucide-svelte';
	import SettingsSectionLabel from './SettingsSectionLabel.svelte';

	interface Props {
		apiKey: string | undefined;
		/** Origin веб-интерфейса — для адреса API в описании ключа. */
		origin: string;
		saving?: boolean;
		/** Тумблер кнопки отзыва — только на канале develop. */
		showFeedback: boolean;
		showNdmsProxy: boolean;
		highlighted?: boolean;
		ongenerateApiKey: () => void;
	}

	let { apiKey, origin, saving = false, showFeedback, showNdmsProxy, highlighted = false, ongenerateApiKey }: Props = $props();

	let showHappKeysModal = $state(false);

	const singboxStatusValue = $derived($singboxStatus.data ?? null);
	const ndmsProxyEnabled = $derived(singboxStatusValue?.ndmsProxyEnabled ?? true);
	let ndmsProxyBusy = $state(false);
	let ndmsProxyConfirmOpen = $state(false);
	let ndmsProxyConfirmEnable = $state(false); // true = подтверждение включения; false = выключения

	async function copyApiKey() {
		const key = (apiKey ?? '').trim();
		if (!key) {
			notifications.info(m.settings_page_apikey_generate_first());
			return;
		}
		if (await copyToClipboard(key)) {
			notifications.success(m.settings_page_apikey_copied());
		} else {
			notifications.error(m.settings_page_apikey_copy_failed());
		}
	}

	function handleNDMSProxyToggleClick(next: boolean) {
		// next — желаемое состояние после клика. Открываем confirm-modal
		// с предупреждением (warning-only — мы не сканим NDMS-policies).
		ndmsProxyConfirmEnable = next;
		ndmsProxyConfirmOpen = true;
	}

	async function applyNDMSProxyToggle() {
		const enabled = ndmsProxyConfirmEnable;
		ndmsProxyBusy = true;
		try {
			const res = await api.singboxToggleNDMSProxy(enabled);
			ndmsProxyConfirmOpen = false;
			// Обновим стор статуса оптимистично — SSE invalidate тоже придёт.
			if (singboxStatusValue) {
				singboxStatus.applyMutationResponse({ ...singboxStatusValue, ndmsProxyEnabled: res.enabled });
			}
			notifications.success(
				res.migrated
					? (enabled ? m.settings_page_ndms_proxy_enabled_toast() : m.settings_page_ndms_proxy_disabled_toast())
					: m.settings_page_state_unchanged(),
			);
		} catch (e) {
			const msg = e instanceof Error ? e.message : m.settings_page_ndms_proxy_toggle_failed();
			if (msg.includes('PROXY_COMPONENT_MISSING') || msg.includes("'proxy'")) {
				notifications.error(m.settings_page_ndms_component_missing());
			} else {
				notifications.error(msg);
			}
		} finally {
			ndmsProxyBusy = false;
		}
	}
</script>

<div class="settings-block">
	<div
		id="feedback-fab"
		class="card settings-highlight-target"
		class:highlighted
	>
	<SettingsSectionLabel label={m.settings_page_advanced_title()} icon={Wrench} tone="indigo" header />
	<div class="setting-row api-key-setting">
		<div class="flex flex-col gap-1">
			<span class="font-medium">API Key</span>
			<span class="setting-description">
				<RichText
					text={m.settings_page_apikey_description({
						endpoint: `${origin}/api/`,
						token: m.settings_page_apikey_token(),
					})}
				/>
			</span>
		</div>
		<div class="api-key-controls">
			<input
				type="text"
				class="api-key-input"
				value={apiKey ?? ''}
				readonly
				placeholder={m.settings_page_apikey_placeholder()}
				onclick={copyApiKey}
				title={apiKey?.trim()
					? m.settings_page_apikey_click_to_copy()
					: m.settings_page_apikey_generate_hint()}
			/>
			<div class="api-key-action">
				<Button variant="secondary" size="md" onclick={ongenerateApiKey} disabled={saving}>
					{m.common_generate()}
				</Button>
			</div>
		</div>
	</div>
	{#if showFeedback}
	<div class="setting-row toggle-inline-row">
		<div class="flex flex-col gap-1">
			<span class="font-medium">{m.settings_page_feedback_label()}</span>
			<span class="setting-description">
				{m.settings_page_feedback_description()}
			</span>
		</div>
		<Toggle
			checked={$developFeedbackFabVisible}
			onchange={(v) => developFeedbackFabVisible.set(v)}
		/>
	</div>
	{/if}

	<div class="setting-row toggle-inline-row">
		<div class="flex flex-col gap-1">
			<span class="font-medium">{m.settings_page_happ_label()}</span>
			<span class="setting-description">
				{m.settings_page_happ_description()}
			</span>
		</div>
		<Button
			variant="secondary"
			size="md"
			onclick={() => (showHappKeysModal = true)}
			disabled={saving}
		>
			{m.settings_page_happ_manage()}
		</Button>
	</div>

	{#if showNdmsProxy}
		<div class="setting-row toggle-inline-row">
			<div class="flex flex-col gap-1">
				<span class="font-medium">{m.settings_page_ndms_proxy_label()}</span>
				<span class="setting-description">
					{#if ndmsProxyEnabled}
						{m.settings_page_ndms_proxy_on_1()}
						<br>
						{m.settings_page_ndms_proxy_on_2()}
					{:else}
						{m.settings_page_ndms_proxy_off()}
					{/if}
				</span>
			</div>
			<Toggle
				checked={ndmsProxyEnabled}
				controlled
				disabled={ndmsProxyBusy}
				onchange={handleNDMSProxyToggleClick}
			/>
		</div>
	{/if}
	</div>
</div>

<ConfirmModal
	open={ndmsProxyConfirmOpen}
	title={ndmsProxyConfirmEnable ? m.settings_page_ndms_confirm_enable_title() : m.settings_page_ndms_confirm_disable_title()}
	message={ndmsProxyConfirmEnable
		? m.settings_page_ndms_confirm_enable_message()
		: m.settings_page_ndms_confirm_disable_message()}
	secondary={ndmsProxyConfirmEnable
		? m.settings_page_ndms_confirm_enable_secondary()
		: m.settings_page_ndms_confirm_disable_secondary()}
	confirmLabel={ndmsProxyConfirmEnable ? m.common_enable() : m.common_disable()}
	variant={ndmsProxyConfirmEnable ? 'primary' : 'danger'}
	busy={ndmsProxyBusy}
	onConfirm={applyNDMSProxyToggle}
	onClose={() => (ndmsProxyConfirmOpen = false)}
/>

<HappKeysModal
	bind:open={showHappKeysModal}
	onclose={() => (showHappKeysModal = false)}
/>

<style>
	.api-key-controls {
		display: grid;
		grid-template-columns: minmax(0, 1fr) auto;
		align-items: stretch;
		gap: 0.5rem;
		width: 100%;
		min-width: 0;
	}

	.api-key-input {
		cursor: pointer;
	}

	.api-key-action {
		display: flex;
		align-items: stretch;
		white-space: nowrap;
	}

	.api-key-action :global(.btn) {
		height: 32px;
		min-height: 32px;
		max-height: 32px;
		box-sizing: border-box;
		padding-block: 0;
	}

	.api-key-setting {
		display: grid;
		grid-template-columns: minmax(0, 1fr) minmax(0, min(50%, 34rem));
		gap: 1rem;
		align-items: center;
	}
	.api-key-setting > *:first-child {
		min-width: 0;
	}

	@media (min-width: 641px) {
		.api-key-setting {
			grid-template-columns: minmax(0, 1fr) minmax(0, min(50%, 34rem));
			align-items: center;
		}

		.api-key-setting > *:first-child {
			display: flex;
			flex-direction: column;
			align-items: flex-start;
			gap: 0.25rem;
		}

		.api-key-setting .setting-description {
			white-space: normal;
			overflow: visible;
			text-overflow: clip;
		}

		.api-key-controls {
			width: 100%;
			grid-template-columns: minmax(0, 1fr) auto;
			align-items: stretch;
		}

		.api-key-action {
			display: flex;
		}

		.api-key-action :global(.btn) {
			width: auto;
			min-width: 7.5rem;
			height: 32px;
			min-height: 32px;
			max-height: 32px;
		}
	}

	@media (max-width: 640px) {
		.api-key-controls {
			grid-template-columns: minmax(0, 1fr) auto;
		}

		.api-key-setting {
			grid-template-columns: 1fr;
		}
	}
</style>
