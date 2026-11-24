<script lang="ts">
	import { m } from '$lib/i18n';
	import { Button, Modal } from '$lib/components/ui';

	interface Props {
		open: boolean;
		text: string;
		onclose: () => void;
		onsubmit: () => void;
	}

	let { open, text = $bindable(), onclose, onsubmit }: Props = $props();
</script>

<!-- Modal 3: Bulk Import Rules -->
<Modal
	{open}
	{onclose}
	title={m.firc_groups_import_rules_title()}
	size="lg"
>
	<div class="modal-form">
		<p class="hint">
			{m.firc_groups_import_rules_hint()}
		</p>
		<textarea
			class="inp font-mono"
			rows="10"
			placeholder={'example.com\n*.openai.com\n149.154.160.0/20'}
			bind:value={text}
		></textarea>
	</div>

	{#snippet actions()}
		<div class="modal-footer-btns">
			<Button variant="ghost" size="sm" onclick={onclose}>
				{m.common_cancel()}
			</Button>
			<Button variant="primary" size="sm" onclick={onsubmit}>{m.firc_groups_import_rules_btn()}</Button>
		</div>
	{/snippet}
</Modal>

<style>
	.inp {
		padding: 5px 9px;
		border-radius: var(--radius-sm);
		background: var(--bg-primary);
		border: 1px solid var(--border);
		color: var(--text-primary);
		font-size: 12.5px;
		font-family: inherit;
	}

	.inp:focus {
		outline: none;
		border-color: var(--accent);
	}

	.modal-form {
		display: flex;
		flex-direction: column;
		gap: 12px;
	}

	.hint {
		margin: 0;
		font-size: 11.5px;
		color: var(--text-muted);
		line-height: 1.4;
	}

	.modal-footer-btns {
		display: flex;
		justify-content: flex-end;
		gap: 8px;
		width: 100%;
	}
</style>
