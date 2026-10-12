<script lang="ts">
	import { m } from '$lib/i18n';
	import { Toggle } from '$lib/components/ui';

	interface Props {
		visible: boolean;
		editUnlocked?: boolean;
	}

	let { visible, editUnlocked = $bindable(false) }: Props = $props();
</script>

{#if visible}
	<div class="wizard-edit-bar">
		<Toggle
			label={m.sb_router_wizard_edit_bar_label()}
			hint={m.sb_router_wizard_edit_bar_hint()}
			checked={editUnlocked}
			onchange={(v) => (editUnlocked = v)}
		/>
	</div>
{/if}

<style>
	.wizard-edit-bar {
		margin: 0 0 0.75rem;
		padding: 0.5rem 0.75rem;
		border-radius: var(--radius-sm);
		background: var(--color-surface-secondary, rgba(0, 0, 0, 0.04));
		border: 1px solid var(--color-border);
	}
</style>
