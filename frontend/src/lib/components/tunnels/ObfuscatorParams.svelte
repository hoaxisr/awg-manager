<script lang="ts">
	import type { ObfuscatorMasking, TunnelObfuscator } from '$lib/types';
	import { m } from '$lib/i18n';
	interface Props {
		obfuscator: TunnelObfuscator;
		error?: string;
	}
	let { obfuscator = $bindable(), error }: Props = $props();
	const maskings: ObfuscatorMasking[] = $derived(
		obfuscator.flavor === 'phobos' ? ['STUN', 'MEDIA', 'AUTO', 'NONE'] : ['STUN', 'AUTO', 'NONE']
	);
</script>

<p class="form-hint">
	{m.tunnel_edit_obf_flavor_prefix()} <strong>{obfuscator.flavor === 'phobos' ? 'Phobos' : 'ClusterM'}</strong>
	{m.tunnel_edit_obf_flavor_suffix({ port: obfuscator.localPort })}
</p>
<div class="flex flex-col gap-1.5">
	<label class="field-label" for="obf-target">{m.tunnel_edit_obf_target_obf_first()}</label>
	<input id="obf-target" class="field-input" bind:value={obfuscator.target}>
	<!-- .field-hint.is-error, а не text-error-500: последний в этой сборке
	     резолвится в серый (oklch(0.556 0 0)), т.е. ошибка не читается красным. -->
	{#if error}<p class="field-hint is-error">{error}</p>{/if}
</div>
<div class="flex flex-col gap-1.5">
	<label class="field-label" for="obf-key">{m.tunnel_edit_obf_key()}</label>
	<input id="obf-key" class="field-input" bind:value={obfuscator.key}>
</div>
<div class="flex flex-col gap-1.5">
	<label class="field-label" for="obf-masking">{m.tunnel_edit_obf_masking()}</label>
	<select id="obf-masking" class="field-select" bind:value={obfuscator.masking}>
		{#each maskings as masking (masking)}<option value={masking}>{masking}</option>{/each}
	</select>
</div>
<div class="flex items-end gap-3">
	<div class="flex flex-1 flex-col gap-1.5">
		<label class="field-label" for="obf-dummy">max-dummy</label>
		<input id="obf-dummy" class="field-input" type="number" min="0" max="1024" bind:value={obfuscator.maxDummy}>
	</div>
	<div class="flex flex-1 flex-col gap-1.5">
		<label class="field-label" for="obf-idle">{m.tunnel_edit_obf_idle()}</label>
		<input id="obf-idle" class="field-input" type="number" min="0" bind:value={obfuscator.idleTimeout}>
	</div>
	{#if obfuscator.flavor === 'phobos'}
		<div class="flex flex-1 flex-col gap-1.5">
			<label class="field-label" for="obf-bytes">{m.tunnel_edit_obf_bytes()}</label>
			<input id="obf-bytes" class="field-input" type="number" min="0" bind:value={obfuscator.obfuscateBytes}>
		</div>
	{/if}
</div>
