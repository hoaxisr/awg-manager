<!--
  Строка «Phobos в ядре» карточки «Прокси и обфускаторы»: выключатель
  kernel-релея awgm_relay и причина, если его выключил сторож.
  Презентационная: API вызывает вкладка.
-->
<script lang="ts">
	import { m } from '$lib/i18n';
	import { Toggle } from '$lib/components/ui';

	interface Props {
		process: boolean;
		tripped?: string;
		saving?: boolean;
		ontoggle: (process: boolean) => void;
	}

	let { process, tripped = '', saving = false, ontoggle }: Props = $props();
</script>

<div class="setting-row toggle-inline-row component-indent">
	<div class="flex flex-col gap-1">
		<span class="font-medium">{m.settings_relay_phobos_label()}</span>
		<span class="setting-description">
			{m.settings_relay_phobos_description()}
		</span>
		{#if tripped}
			<span class="setting-description warning">{m.settings_relay_tripped({ reason: tripped })}</span>
		{/if}
	</div>
	<Toggle checked={!process} controlled disabled={saving} onchange={(v: boolean) => ontoggle(!v)} />
</div>
