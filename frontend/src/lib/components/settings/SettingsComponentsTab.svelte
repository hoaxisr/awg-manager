<!-- Вкладка «Компоненты»: то, что панель ставит и запускает на роутере. -->
<script lang="ts">
	import { api } from '$lib/api/client';
	import { m } from '$lib/i18n';
	import { usageLevel } from '$lib/stores/settings';
	import { singboxStatus } from '$lib/stores/singbox';
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

	// Статус ещё не пришёл — считаем установленным: карточка не перепрыгнет.
	const singboxTall = $derived(showSingbox && ($singboxStatus.data?.installed ?? true));

	function toggleObfuscatorRelay(process: boolean) {
		return editor.save(
			() => api.setObfuscatorRelay(process),
			process ? m.settings_page_phobos_userspace() : m.settings_page_phobos_kernel(),
			backendErrorOr(m.settings_page_save_error()),
		);
	}
</script>

<!-- Карточка sing-box высока, пока он установлен: FIRC — напротив неё. Иначе
     FIRC добирает короткую левую колонку. -->
<div class="settings-columns">
	<div class="settings-column">
		{#if showSingbox}
			<SingboxSection {editor}>
				{#if isExpert}
					<NdmsProxyRow />
				{/if}
			</SingboxSection>
		{/if}
		<ZapretCard />
		{#if !singboxTall}
			<FircIntegrationSection />
		{/if}
	</div>
	<div class="settings-column">
		<ProxyBinariesSection phobosKernel={isExpert ? phobosKernel : undefined} />
		{#if singboxTall}
			<FircIntegrationSection />
		{/if}
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
