<script lang="ts">
	import { Modal, Button } from '$lib/components/ui';
	import type { TunnelReferencedError } from '$lib/types';
	import { describeRouterReference } from '$lib/utils/tunnelRefs';

	interface Props {
		open: boolean;
		details: TunnelReferencedError | null;
		tunnelName?: string;
		onclose: () => void;
	}

	let { open, details, tunnelName, onclose }: Props = $props();
</script>

<Modal {open} title="Удаление невозможно" size="sm" {onclose}>
	{#if details}
		<p class="lead">
			Туннель {#if tunnelName}<strong>{tunnelName}</strong>{/if} используется в других местах конфигурации:
		</p>
		<ul class="ref-list">
			{#if details.deviceProxy}
				<li>Активен в селекторе device-proxy (выбран как маршрут по умолчанию)</li>
			{/if}
			{#if details.routerRules && details.routerRules.length > 0}
				<li>
					Используется в правилах sing-box router:
					<span class="rule-indices">
						{details.routerRules.map((i) => `#${i + 1}`).join(', ')}
					</span>
				</li>
			{/if}
			{#if details.routerOther}
				{#each details.routerOther as loc}
					{@const ref = describeRouterReference(loc)}
					<li>{#if ref.known}{ref.text}{:else}<code>{ref.text}</code>{/if}</li>
				{/each}
			{/if}
		</ul>
		<p class="hint">Удалите ссылки и попробуйте снова.</p>
	{/if}
	{#snippet actions()}
		<Button variant="primary" size="md" onclick={onclose}>Понятно</Button>
	{/snippet}
</Modal>

<style>
	.lead {
		margin: 0 0 0.5rem;
	}
	.ref-list {
		margin: 0.5rem 0 0.75rem;
		padding-left: 1.25rem;
		list-style: disc;
	}
	.ref-list li {
		margin: 0.25rem 0;
	}
	.rule-indices {
		font-family: var(--font-mono);
		color: var(--color-text-muted);
	}
	.ref-list code {
		font-family: var(--font-mono);
		font-size: 0.8125rem;
		color: var(--color-text-muted);
	}
	.hint {
		color: var(--color-text-muted);
		font-size: 0.875rem;
		margin: 0;
	}
</style>
