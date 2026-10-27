<!--
  Карточка «Ping check»: цели проверки по умолчанию (ICMP и HTTP). Поля —
  черновик карточки; сохраняет общие настройки страница (onsave).
-->
<script lang="ts">
	import { m } from '$lib/i18n';
	import { Button } from '$lib/components/ui';
	import { Activity } from 'lucide-svelte';
	import SettingsSectionLabel from './SettingsSectionLabel.svelte';

	interface Props {
		target: string;
		url: string;
		saving?: boolean;
		onsave: (target: string, url: string) => void;
	}

	let { target, url, saving = false, onsave }: Props = $props();

	const defaultPingTarget = '8.8.8.8';
	const defaultConnectivityCheckUrl = 'http://connectivitycheck.gstatic.com/generate_204';

	let targetDraft = $state('');
	let urlDraft = $state('');
	$effect(() => {
		targetDraft = target;
		urlDraft = url;
	});
</script>

<div class="settings-block">
	<div class="card">
	<SettingsSectionLabel label={m.settings_page_ping_title()} icon={Activity} tone="teal" header />
	<div class="setting-row ping-target-setting">
		<div class="flex flex-col gap-1">
			<span class="font-medium">{m.settings_page_ping_targets_label()}</span>
			<span class="setting-description">
				{m.settings_page_ping_targets_description()}
			</span>
		</div>
		<div class="ping-target-controls">
			<label class="ping-target-field">
				<span>ICMP target</span>
				<input
					type="text"
					class="settings-text-input"
					bind:value={targetDraft}
					placeholder={defaultPingTarget}
					disabled={saving}
				/>
			</label>
			<label class="ping-target-field">
				<span>{m.settings_page_ping_http_url()}</span>
				<input
					type="url"
					class="settings-text-input"
					bind:value={urlDraft}
					placeholder={defaultConnectivityCheckUrl}
					disabled={saving}
				/>
			</label>
			<div class="ping-target-action">
				<Button variant="secondary" size="md" onclick={() => onsave(targetDraft, urlDraft)} disabled={saving}>
					{m.common_save()}
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
