<script lang="ts">
	// Поле хеша / базового секрета пользователя с «Сгенерировать» и «Сохранить».
	import { m } from '$lib/i18n';
	import { Button } from '$lib/components/ui';
	import { RefreshCw } from 'lucide-svelte';

	interface Props {
		value: string;
		saving: boolean;
		onregenerate: () => void;
		onsave: () => void;
	}

	let { value = $bindable(''), saving, onregenerate, onsave }: Props = $props();
</script>

<span class="m-label">{m.tg_user_secret_base_label()}</span>
<div class="input-with-actions">
	<input
		type="text"
		aria-label={m.tg_user_secret_base_label()}
		bind:value
		placeholder={m.tg_user_secret_placeholder()}
		class="link-input font-mono"
	/>
	<Button variant="secondary" size="sm" onclick={onregenerate} disabled={saving}>
		<RefreshCw size={13} class={saving ? 'spin mr-1' : 'mr-1'} />
		{m.tg_user_secret_generate()}
	</Button>
	<Button variant="primary" size="sm" onclick={onsave} disabled={saving}>
		{m.tg_user_secret_save()}
	</Button>
</div>
<span class="field-hint">{m.tg_user_secret_hint()}</span>

<style>
	.m-label {
		font-size: 11px;
		color: var(--color-text-muted);
		display: block;
		margin-bottom: 2px;
	}

	.input-with-actions {
		display: flex;
		gap: 6px;
		flex-wrap: wrap;
	}

	.link-input {
		flex: 1;
		min-width: 160px;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		padding: 5px 8px;
		font-size: 12px;
		color: var(--color-text-primary);
		outline: none;
	}

	.field-hint {
		font-size: 11.5px;
		color: var(--color-text-muted);
		margin: 0;
	}

	.font-mono {
		font-family: var(--font-mono);
	}
</style>
