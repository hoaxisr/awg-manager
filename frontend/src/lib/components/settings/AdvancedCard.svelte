<!--
  Карточка «Расширенные»: API-ключ, кнопка обратной связи (канал develop)
  и переключатель NDMS Proxy для sing-box туннелей.
-->
<script lang="ts">
	import { Wrench } from 'lucide-svelte';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { singboxStatus } from '$lib/stores/singbox';
	import { Toggle, Button, ConfirmModal } from '$lib/components/ui';
	import { setSettings as setGlobalSettings } from '$lib/stores/settings';
	import { developFeedbackFabVisible } from '$lib/stores/developFeedbackFab';
	import { copyToClipboard } from '$lib/utils/clipboard';
	import SettingsSectionLabel from './SettingsSectionLabel.svelte';
	import type { Settings } from '$lib/types';

	interface Props {
		settings: Settings;
		saving: boolean;
		highlightFeedbackFab: boolean;
	}

	let { settings = $bindable(), saving = $bindable(), highlightFeedbackFab }: Props = $props();

	const origin = $derived(typeof window !== "undefined" ? window.location.origin : "");

	const singboxStatusValue = $derived($singboxStatus.data ?? null);
	const singboxInstalled = $derived(singboxStatusValue?.installed ?? false);
	const ndmsProxyEnabled = $derived(singboxStatusValue?.ndmsProxyEnabled ?? true);

	let ndmsProxyBusy = $state(false);
	let ndmsProxyConfirmOpen = $state(false);
	let ndmsProxyConfirmEnable = $state(false); // true = подтверждение включения; false = выключения

	async function generateApiKey() {
		saving = true;
		try {
			// Server-side generation: WebCrypto's randomUUID is unavailable
			// over plain HTTP (router LAN context), so the backend produces
			// the UUID via crypto/rand and persists it in one round-trip.
			settings = await api.regenerateApiKey();
			setGlobalSettings(settings);
			notifications.success("API ключ сгенерирован");
		} catch {
			notifications.error("Ошибка генерации ключа");
		} finally {
			saving = false;
		}
	}

	async function copyApiKey() {
		const key = (settings.apiKey ?? "").trim();
		if (!key) {
			notifications.info("Сначала сгенерируйте API ключ");
			return;
		}
		if (await copyToClipboard(key)) {
			notifications.success("API ключ скопирован в буфер обмена");
		} else {
			notifications.error("Не удалось скопировать API ключ");
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
					? (enabled ? 'NDMS Proxy включены' : 'NDMS Proxy выключены')
					: 'Состояние не изменилось',
			);
		} catch (e) {
			const msg = e instanceof Error ? e.message : 'Не удалось переключить NDMS Proxy';
			if (msg.includes('PROXY_COMPONENT_MISSING') || msg.includes("'proxy'")) {
				notifications.error('NDMS-компонент "proxy" не установлен. Установите его в System → Components.');
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
		class:highlighted={highlightFeedbackFab}
	>
	<SettingsSectionLabel label="Расширенные" icon={Wrench} tone="indigo" header />
	<div class="setting-row api-key-setting">
		<div class="flex flex-col gap-1">
			<span class="font-medium">API Key</span>
			<span class="setting-description">
				API ключ для доступа к&nbsp;<code>{origin}/api/</code>, если включена авторизация. Передавайте в заголовке <code>Authorization: Bearer &lt;ключ&gt;</code>.
			</span>
		</div>
		<div class="api-key-controls">
			<input
				type="text"
				class="api-key-input"
				value={settings.apiKey ?? ""}
				readonly
				placeholder="не сгенерирован"
				onclick={copyApiKey}
				title={settings.apiKey?.trim()
					? "Нажмите, чтобы скопировать в буфер обмена"
					: "Сначала нажмите «Сгенерировать»"}
			/>
			<div class="api-key-action">
				<Button variant="secondary" size="md" onclick={generateApiKey} disabled={saving}>
					Сгенерировать
				</Button>
			</div>
		</div>
	</div>
	{#if settings.updates.channel === 'develop'}
	<div class="setting-row toggle-inline-row">
		<div class="flex flex-col gap-1">
			<span class="font-medium">Кнопка обратной связи</span>
			<span class="setting-description">
				Плавающая кнопка «!» в правом нижнем углу на канале разработки.
				Помогает быстро сообщить об ошибке или предложить улучшение.
			</span>
		</div>
		<Toggle
			checked={$developFeedbackFabVisible}
			onchange={(v) => developFeedbackFabVisible.set(v)}
		/>
	</div>
	{/if}
	{#if singboxInstalled}
		<div class="setting-row toggle-inline-row">
			<div class="flex flex-col gap-1">
				<span class="font-medium">NDMS Proxy для sing-box туннелей</span>
				<span class="setting-description">
					{#if ndmsProxyEnabled}
						Если включено — для каждого туннеля sing-box создаётся интерфейс ProxyX в роутере.
						<br>
						Необходимо, если используете NDMS-маршрутизацию (Access Policy, политики роутера) для sing-box.
					{:else}
						Выключено — sing-box работает только через свою маршрутизацию. ProxyX-интерфейсы не создаются
						(решает проблему зависания роутера при потере WAN).
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
	title={ndmsProxyConfirmEnable ? 'Включить NDMS Proxy?' : 'Выключить NDMS Proxy?'}
	message={ndmsProxyConfirmEnable
		? 'Будут созданы интерфейсы ProxyX в NDMS для текущих туннелей sing-box.'
		: 'Интерфейсы ProxyX будут удалены из NDMS. Sing-box продолжит работать через свою маршрутизацию.'}
	secondary={ndmsProxyConfirmEnable
		? 'Требуется NDMS-компонент "proxy".'
		: 'Проверьте, что никакие правила маршрутизации NDMS (Access Policy, политики роутера) не ссылаются на эти ProxyX — иначе они перестанут работать.'}
	confirmLabel={ndmsProxyConfirmEnable ? 'Включить' : 'Выключить'}
	variant={ndmsProxyConfirmEnable ? 'primary' : 'danger'}
	busy={ndmsProxyBusy}
	onConfirm={applyNDMSProxyToggle}
	onClose={() => (ndmsProxyConfirmOpen = false)}
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
		width: 100%;
		max-width: none;
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
