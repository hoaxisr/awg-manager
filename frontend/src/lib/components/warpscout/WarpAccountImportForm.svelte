<script lang="ts">
	import { m } from '$lib/i18n';
	import { Button } from '$lib/components/ui';

	interface Props {
		raw: string;
		busy?: boolean;
	}

	let { raw = $bindable(), busy = false }: Props = $props();
	let fileInputEl = $state<HTMLInputElement | null>(null);

	async function onFileSelected(e: Event): Promise<void> {
		const input = e.currentTarget as HTMLInputElement;
		const file = input.files?.[0];
		if (!file) return;
		try {
			raw = await file.text();
		} finally {
			input.value = '';
		}
	}
</script>

<p class="alt-hint">{m.warpscout_import_hint()}</p>
<input
	bind:this={fileInputEl}
	type="file"
	accept=".json,.toml,.conf,.txt"
	class="file-input-hidden"
	onchange={onFileSelected}
/>
<div class="alt-head-row">
	<label class="field-label" for="warp-import-raw">{m.warpscout_import_paste_label()}</label>
	<Button size="sm" variant="ghost" disabled={busy} onclick={() => fileInputEl?.click()}>
		{m.warpscout_import_file_btn()}
	</Button>
</div>
<textarea
	id="warp-import-raw"
	class="field-textarea"
	rows={7}
	placeholder={'[Interface]\nPrivateKey = ...\nAddress = 172.16.0.2/32'}
	bind:value={raw}
></textarea>

<style>
	.alt-hint {
		margin: 0;
		font-size: 0.75rem;
		line-height: 1.4;
		color: var(--color-text-muted);
	}
	.alt-head-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.5rem;
	}
	.field-label {
		font-size: 0.72rem;
		font-weight: 500;
		color: var(--color-text-secondary);
	}
	.file-input-hidden {
		display: none;
	}
</style>
