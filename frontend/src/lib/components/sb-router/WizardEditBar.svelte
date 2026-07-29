<script lang="ts">
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
			label="Изменить настройки"
			hint="Разблокирует шаги конфигурации для правок (импорт ссылки, пароль, NAT и т.д.)"
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
