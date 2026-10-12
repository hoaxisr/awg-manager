<!--
  Карточка «Релей обфускатора»: выключатель kernel-релея awgm_relay для
  Phobos-туннелей и причина, если его выключил сторож. Презентационная:
  API вызывает страница (как McpCard).
-->
<script lang="ts">
	import { m } from '$lib/i18n';
	import { Toggle } from '$lib/components/ui';
	import SettingsSectionLabel from './SettingsSectionLabel.svelte';
	import { Cpu } from 'lucide-svelte';

	interface Props {
		process: boolean;
		tripped?: string;
		saving?: boolean;
		ontoggle: (process: boolean) => void;
	}

	let { process, tripped = '', saving = false, ontoggle }: Props = $props();
</script>

<div class="settings-block" id="obfuscator-relay">
	<div class="card">
		<SettingsSectionLabel label={m.settings_relay_title()} icon={Cpu} header />
		<div class="setting-row">
			<div class="flex flex-col gap-1">
				<span class="font-medium">{m.settings_relay_phobos_label()}</span>
				<span class="setting-description">
					{m.settings_relay_phobos_description()}
				</span>
				{#if tripped}
					<span class="setting-description text-warning">{m.settings_relay_tripped({ reason: tripped })}</span>
				{/if}
			</div>
			<Toggle checked={!process} disabled={saving} onchange={(v: boolean) => ontoggle(!v)} />
		</div>
	</div>
</div>
