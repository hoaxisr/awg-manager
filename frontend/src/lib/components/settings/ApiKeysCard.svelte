<!--
  Карточка «Ключи и API»: API-ключ, MCP-сервер и ключи Happ. Ключ выпускает
  вкладка (ongenerateApiKey) — он меняет общие настройки.
-->
<script lang="ts">
	import type { Snippet } from 'svelte';
	import { m } from '$lib/i18n';
	import { Button } from '$lib/components/ui';
	import RichText from '$lib/components/ui/RichText.svelte';
	import HappKeysModal from '$lib/components/subscriptions/HappKeysModal.svelte';
	import { notifications } from '$lib/stores/notifications';
	import { copyToClipboard } from '$lib/utils/clipboard';
	import { KeyRound } from 'lucide-svelte';
	import SettingsSectionLabel from './SettingsSectionLabel.svelte';

	interface Props {
		apiKey: string | undefined;
		/** Origin веб-интерфейса — для адреса API в описании ключа. */
		origin: string;
		saving?: boolean;
		ongenerateApiKey: () => void;
		/** Строки MCP-сервера: у него свой стор ключей. */
		children: Snippet;
	}

	let { apiKey, origin, saving = false, ongenerateApiKey, children }: Props = $props();

	let showHappKeysModal = $state(false);

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
</script>

<div class="settings-block">
	<div class="card">
	<SettingsSectionLabel label={m.settings_page_keys_title()} icon={KeyRound} tone="indigo" header />
	<div class="setting-row settings-stacked-row">
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
			<Button variant="secondary" size="md" onclick={ongenerateApiKey} disabled={saving}>
				{m.common_generate()}
			</Button>
		</div>
	</div>
	{@render children()}

	<div class="setting-row">
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

	</div>
</div>

<HappKeysModal
	bind:open={showHappKeysModal}
	onclose={() => (showHappKeysModal = false)}
/>

<style>
	.api-key-controls {
		display: flex;
		align-items: stretch;
		gap: 0.5rem;
		min-width: 0;
	}

	.api-key-input {
		flex: 1 1 auto;
		min-width: 0;
		cursor: pointer;
	}
</style>
