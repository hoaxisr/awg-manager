<script lang="ts">
	import { usageLevel } from '$lib/stores/settings';
	import { TriangleAlert } from 'lucide-svelte';

	interface Props {
		/** NDMS dns-proxy tab is OS5-only; parent passes router capability. */
		isOS5?: boolean;
	}

	let { isOS5 = false }: Props = $props();

	const visible = $derived($usageLevel === 'basic' && isOS5);
</script>

{#if visible}
	<div class="ndms-policy-hint" role="note">
		<div class="hint-icon" aria-hidden="true">
			<TriangleAlert size={20} />
		</div>
		<p>
			Для работы DNS-маршрутизации клиент должен находиться в политике доступа «Политика по умолчанию» — в веб-интерфейсе
			роутера, раздел <strong>Приоритеты подключений</strong>. Если устройство привязано к другой
			политике, DNS-маршруты на него не распространяются.
		</p>
	</div>
{/if}

<style>
	.ndms-policy-hint {
		display: flex;
		align-items: flex-start;
		gap: 0.75rem;
		margin-bottom: 1rem;
		padding: 0.875rem 1rem;
		background: var(--color-warning-tint, rgba(224, 175, 104, 0.1));
		border: 1px solid var(--color-warning-border, rgba(224, 175, 104, 0.4));
		border-left: 3px solid var(--color-warning, var(--warning));
		border-radius: var(--radius-sm, 6px);
		color: var(--color-text-primary, var(--text-primary));
	}

	.hint-icon {
		flex-shrink: 0;
		color: var(--color-warning, var(--warning));
	}

.ndms-policy-hint p {
		flex: 1;
		margin: 0;
		font-size: 0.8125rem;
		line-height: 1.45;
		color: var(--color-text-secondary, var(--text-secondary));
	}

	.ndms-policy-hint strong {
		color: var(--color-text-primary, var(--text-primary));
		font-weight: 600;
	}
</style>
