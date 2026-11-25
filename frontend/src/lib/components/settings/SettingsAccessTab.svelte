<!-- Вкладка «Доступ»: вход в панель, её адрес, ключи API и MCP. -->
<script lang="ts">
	import { api } from '$lib/api/client';
	import { m } from '$lib/i18n';
	import { usageLevel } from '$lib/stores/settings';
	import AccessCard from './AccessCard.svelte';
	import ApiKeysCard from './ApiKeysCard.svelte';
	import McpSection from './McpSection.svelte';
	import { backendErrorOr, type SettingsEditor } from './settingsEditor.svelte';

	interface Props {
		editor: SettingsEditor;
	}

	let { editor }: Props = $props();

	const origin = window.location.origin;

	function toggleAuth(enabled: boolean) {
		return editor.save(
			() => api.updateSettings({ authEnabled: enabled }),
			enabled ? m.settings_page_auth_enabled() : m.settings_page_auth_disabled(),
			m.settings_page_save_error(),
		);
	}

	function saveSessionTtl(hours: number) {
		// 400 с русским сообщением от бэкенда (валидация 1..720) — показываем как есть.
		return editor.save(
			() => api.updateSettings({ sessionTtlHours: hours }),
			m.settings_page_session_ttl_saved(),
			backendErrorOr(m.settings_page_save_error()),
		);
	}

	function toggleMcp(enabled: boolean) {
		return editor.save(
			() => api.updateSettings({ mcpEnabled: enabled }),
			enabled ? m.settings_page_mcp_enabled() : m.settings_page_mcp_disabled(),
			backendErrorOr(m.settings_page_save_error()),
		);
	}

	function generateApiKey() {
		// Server-side generation: WebCrypto's randomUUID is unavailable
		// over plain HTTP (router LAN context), so the backend produces
		// the UUID via crypto/rand and persists it in one round-trip.
		return editor.save(
			() => api.regenerateApiKey(),
			m.settings_page_apikey_generated(),
			m.settings_page_apikey_generate_failed(),
		);
	}
</script>

<div class="settings-columns">
	<div class="settings-column">
		<AccessCard
			authEnabled={editor.settings.authEnabled}
			sessionTtlHours={editor.settings.sessionTtlHours}
			saving={editor.saving}
			ontoggleAuth={toggleAuth}
			onsaveSessionTtl={saveSessionTtl}
		/>
	</div>
	{#if $usageLevel === 'expert'}
		<div class="settings-column">
			<ApiKeysCard
				apiKey={editor.settings.apiKey}
				{origin}
				saving={editor.saving}
				ongenerateApiKey={generateApiKey}
			>
				<McpSection
					enabled={editor.settings.mcpEnabled ?? false}
					saving={editor.saving}
					{origin}
					ontoggle={toggleMcp}
				/>
			</ApiKeysCard>
		</div>
	{/if}
</div>
