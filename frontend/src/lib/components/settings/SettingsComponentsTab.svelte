<!-- Вкладка «Компоненты»: то, что панель ставит и запускает на роутере. -->
<script lang="ts">
	import { api } from '$lib/api/client';
	import { m } from '$lib/i18n';
	import { usageLevel } from '$lib/stores/settings';
	import { isSectionVisible } from '$lib/types/usageLevel';
	import FircIntegrationSection from './FircIntegrationSection.svelte';
	import NdmsProxyRow from './NdmsProxyRow.svelte';
	import ObfuscatorRelayRow from './ObfuscatorRelayRow.svelte';
	import ProxyBinariesSection from './ProxyBinariesSection.svelte';
	import SingboxSection from './SingboxSection.svelte';
	import ZapretCard from './ZapretCard.svelte';
	import { backendErrorOr, type SettingsEditor } from './settingsEditor.svelte';

	interface Props {
		editor: SettingsEditor;
	}

	let { editor }: Props = $props();

	const showSingbox = $derived(isSectionVisible($usageLevel, 'singboxTunnels'));
	const isExpert = $derived($usageLevel === 'expert');

	function toggleObfuscatorRelay(process: boolean) {
		return editor.save(
			() => api.setObfuscatorRelay(process),
			process ? m.settings_page_phobos_userspace() : m.settings_page_phobos_kernel(),
			backendErrorOr(m.settings_page_save_error()),
		);
	}
</script>

<div class="settings-columns">
	<div class="settings-column">
		{#if showSingbox}
			<SingboxSection {editor}>
				{#if isExpert}
					<NdmsProxyRow />
				{/if}
			</SingboxSection>
		{/if}
		<FircIntegrationSection />
	</div>
	<div class="settings-column">
		<ProxyBinariesSection phobosKernel={isExpert ? phobosKernel : undefined} />
		<ZapretCard />
	</div>
</div>

{#snippet phobosKernel()}
	<ObfuscatorRelayRow
		process={editor.settings.obfuscatorRelayProcess ?? false}
		tripped={editor.settings.obfuscatorKmodTripped ?? ''}
		saving={editor.saving}
		ontoggle={toggleObfuscatorRelay}
	/>
{/snippet}
