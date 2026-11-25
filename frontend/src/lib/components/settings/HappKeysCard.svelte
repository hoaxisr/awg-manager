<!-- Карточка «Ключи подписок»: RSA-ключи Happ. -->
<script lang="ts">
	import { m } from '$lib/i18n';
	import { Button } from '$lib/components/ui';
	import HappKeysModal from '$lib/components/subscriptions/HappKeysModal.svelte';
	import { KeySquare } from 'lucide-svelte';
	import SettingsSectionLabel from './SettingsSectionLabel.svelte';

	interface Props {
		saving?: boolean;
	}

	let { saving = false }: Props = $props();

	let showHappKeysModal = $state(false);
</script>

<div class="settings-block">
	<div class="card">
		<SettingsSectionLabel label={m.settings_page_sub_keys_title()} icon={KeySquare} tone="pink" header />
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
