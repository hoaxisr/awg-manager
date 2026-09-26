<!--
  Карточка «Релей обфускатора»: выключатель kernel-релея awgm_relay для
  Phobos-туннелей и причина, если его выключил сторож. Презентационная:
  API вызывает страница (как McpCard).
-->
<script lang="ts">
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
		<SettingsSectionLabel label="Релей обфускатора" icon={Cpu} header />
		<div class="setting-row">
			<div class="flex flex-col gap-1">
				<span class="font-medium">Phobos в ядре</span>
				<span class="setting-description">
					Туннели через обфускатор Phobos работают модулем ядра awgm_relay — меньше
					нагрузка на процессор. Выключите, если модуль ведёт себя нестабильно: туннели
					перезапустятся на userspace-процессе.
				</span>
				{#if tripped}
					<span class="setting-description text-warning">Выключено автоматически: {tripped}</span>
				{/if}
			</div>
			<Toggle checked={!process} disabled={saving} onchange={(v: boolean) => ontoggle(!v)} />
		</div>
	</div>
</div>
