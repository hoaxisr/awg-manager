<!--
  Карточка «Проверка пинга»: глобальные цели health-мониторинга —
  ICMP-адрес и HTTP URL проверки доступности.
-->
<script lang="ts">
	import { Activity } from 'lucide-svelte';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { Button } from '$lib/components/ui';
	import { setSettings as setGlobalSettings } from '$lib/stores/settings';
	import SettingsSectionLabel from './SettingsSectionLabel.svelte';
	import type { Settings } from '$lib/types';

	interface Props {
		settings: Settings;
		saving: boolean;
	}

	let { settings = $bindable(), saving = $bindable() }: Props = $props();

	const defaultPingTarget = "8.8.8.8";
	const defaultConnectivityCheckUrl = "http://connectivitycheck.gstatic.com/generate_204";

	async function savePingTargetsSettings() {
		saving = true;
		try {
			settings = await api.updateSettings({
				pingCheck: {
					...settings.pingCheck,
					defaults: {
						...settings.pingCheck.defaults,
						target: settings.pingCheck.defaults.target,
					},
				},
				connectivityCheckUrl: settings.connectivityCheckUrl,
			});
			setGlobalSettings(settings);
			notifications.success("Цели проверки пинга сохранены");
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : "Ошибка сохранения целей проверки");
		} finally {
			saving = false;
		}
	}
</script>

<div class="settings-block">
	<div class="card">
	<SettingsSectionLabel label="Проверка пинга" icon={Activity} tone="teal" header />
	<div class="setting-row ping-target-setting">
		<div class="flex flex-col gap-1">
			<span class="font-medium">Цели проверки</span>
			<span class="setting-description">
				ICMP target используется как глобальный адрес для ping-check. HTTP URL вызывается через туннель для проверки доступности и задержки.
			</span>
		</div>
		<div class="ping-target-controls">
			<label class="ping-target-field">
				<span>ICMP target</span>
				<input
					type="text"
					class="settings-text-input"
					bind:value={settings.pingCheck.defaults.target}
					placeholder={defaultPingTarget}
					disabled={saving}
				/>
			</label>
			<label class="ping-target-field">
				<span>HTTP URL проверки</span>
				<input
					type="url"
					class="settings-text-input"
					bind:value={settings.connectivityCheckUrl}
					placeholder={defaultConnectivityCheckUrl}
					disabled={saving}
				/>
			</label>
			<div class="ping-target-action">
				<Button variant="secondary" size="md" onclick={savePingTargetsSettings} disabled={saving}>
					Сохранить
				</Button>
			</div>
		</div>
	</div>
	</div>
</div>

<style>
	.ping-target-setting {
		display: grid;
		grid-template-columns: minmax(0, 1fr);
		gap: 0.65rem;
		align-items: start;
	}

	.ping-target-controls {
		display: grid;
		grid-template-columns: minmax(8rem, 0.78fr) minmax(16rem, 1.22fr) 7.5rem;
		gap: 0.5rem 0.625rem;
		width: 100%;
		min-width: 0;
		align-items: end;
	}

	.ping-target-field {
		display: grid;
		gap: 0.25rem;
		min-width: 0;
	}

	.ping-target-field > span {
		color: var(--color-text-secondary);
		font-size: 0.75rem;
		font-weight: 600;
	}

	.ping-target-field input {
		min-width: 0;
	}

	.settings-text-input {
		width: 100%;
		max-width: none;
	}

	.ping-target-action {
		display: flex;
		align-items: stretch;
		justify-content: stretch;
		align-self: end;
		min-width: 0;
	}

	.ping-target-action :global(.btn) {
		width: 100%;
		min-width: 7.5rem;
		height: 32px;
		min-height: 32px;
		max-height: 32px;
		box-sizing: border-box;
		padding-block: 0;
	}

	@media (min-width: 641px) {
		.ping-target-setting > *:first-child {
			display: flex;
			flex-direction: column;
			align-items: flex-start;
			gap: 0.25rem;
		}

		.ping-target-setting .setting-description {
			white-space: normal;
			overflow: visible;
			text-overflow: clip;
		}

		.ping-target-controls {
			grid-template-rows: auto 32px;
			align-items: stretch;
		}

		.ping-target-field {
			display: contents;
		}

		.ping-target-field > span {
			grid-row: 1;
		}

		.ping-target-field > input {
			grid-row: 2;
		}

		.ping-target-action {
			grid-row: 2;
			align-self: stretch;
		}
	}

	@media (max-width: 640px) {
		.ping-target-setting {
			grid-template-columns: 1fr;
			align-items: stretch;
		}

		.ping-target-controls {
			grid-template-columns: minmax(0, 1fr);
		}

		.ping-target-action {
			justify-content: stretch;
		}

		.ping-target-action :global(.btn) {
			width: 100%;
		}
	}
</style>
