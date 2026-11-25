<!--
  Строка «NDMS Proxy» карточки sing-box: состояние живёт в его сторе, поэтому
  переключение — здесь, а не у страницы.
-->
<script lang="ts">
	import { api } from '$lib/api/client';
	import { m } from '$lib/i18n';
	import { ConfirmModal, Toggle } from '$lib/components/ui';
	import { notifications } from '$lib/stores/notifications';
	import { singboxStatus } from '$lib/stores/singbox';

	const singboxStatusValue = $derived($singboxStatus.data ?? null);
	const ndmsProxyEnabled = $derived(singboxStatusValue?.ndmsProxyEnabled ?? true);
	let ndmsProxyBusy = $state(false);
	let ndmsProxyConfirmOpen = $state(false);
	let ndmsProxyConfirmEnable = $state(false); // true = подтверждение включения; false = выключения

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
