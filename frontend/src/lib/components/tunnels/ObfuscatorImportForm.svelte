<script lang="ts" module>
	export interface ManualObfuscator {
		target: string;
		key: string;
		masking: 'STUN' | 'AUTO' | 'NONE';
		maxDummy: number;
		idleTimeout: number;
	}
</script>

<script lang="ts">
	import { m } from '$lib/i18n';

	interface Props {
		flavor: 'phobos' | 'clusterm';
		content?: string;
		installUrl?: string;
		obfuscator?: ManualObfuscator;
	}
	let {
		flavor,
		content = $bindable(''),
		installUrl = $bindable(''),
		obfuscator = $bindable({ target: '', key: '', masking: 'STUN', maxDummy: 4, idleTimeout: 0 })
	}: Props = $props();
</script>

{#if flavor === 'phobos'}
	<div class="flex flex-col gap-1.5">
		<label class="field-label" for="obf-install-url">{m.tunnel_edit_obf_install_url()}</label>
		<input
			id="obf-install-url"
			class="field-input"
			type="url"
			placeholder="https://panel.example/api/install/<token>"
			bind:value={installUrl}
		>
		<p class="form-hint">
			{m.tunnel_edit_obf_install_hint()}
		</p>
	</div>
	<div class="flex flex-col gap-1.5">
		<label class="field-label" for="obf-content">
			{m.tunnel_edit_obf_or_conf()}
		</label>
		<textarea id="obf-content" class="field-textarea" rows="10" bind:value={content}></textarea>
	</div>
{:else}
	<div class="flex flex-col gap-1.5">
		<label class="field-label" for="obf-content">{m.tunnel_edit_obf_wg_conf()}</label>
		<textarea id="obf-content" class="field-textarea" rows="8" bind:value={content}></textarea>
		<p class="form-hint">
			{m.tunnel_edit_obf_endpoint_ignored()}
		</p>
	</div>
	<div class="flex flex-col gap-1.5">
		<label class="field-label" for="obf-target">{m.tunnel_edit_obf_target_host_first()}</label>
		<input
			id="obf-target"
			class="field-input"
			placeholder="vpn.example.com:51824"
			bind:value={obfuscator.target}
		>
	</div>
	<div class="flex flex-col gap-1.5">
		<label class="field-label" for="obf-key">{m.tunnel_edit_obf_key()}</label>
		<input id="obf-key" class="field-input" bind:value={obfuscator.key}>
	</div>
	<div class="flex flex-col gap-1.5">
		<label class="field-label" for="obf-masking">{m.tunnel_edit_obf_masking()}</label>
		<select id="obf-masking" class="field-select" bind:value={obfuscator.masking}>
			<option value="STUN">STUN</option>
			<option value="AUTO">AUTO</option>
			<option value="NONE">NONE</option>
		</select>
	</div>
	<div class="flex gap-3">
		<div class="flex flex-1 flex-col gap-1.5">
			<label class="field-label" for="obf-dummy">max-dummy</label>
			<input
				id="obf-dummy"
				class="field-input"
				type="number"
				min="0"
				max="1024"
				bind:value={obfuscator.maxDummy}
			>
		</div>
		<div class="flex flex-1 flex-col gap-1.5">
			<label class="field-label" for="obf-idle">{m.tunnel_edit_obf_idle_default()}</label>
			<input id="obf-idle" class="field-input" type="number" min="0" bind:value={obfuscator.idleTimeout}>
		</div>
	</div>
{/if}
