<script lang="ts">
	import type { AmneziaPremiumCatalog } from '$lib/types';
	import {
		formatPremiumDate,
		premiumSubscriptionDaysLeft,
		premiumSubscriptionState,
		type PremiumSubscriptionState
	} from '$lib/utils/amneziaPremiumCatalog';

	interface Props {
		catalog: AmneziaPremiumCatalog;
		/** Отметка времени, от которой считается срок; приходит снаружи, чтобы не зависеть от часов рендера. */
		nowMs: number;
	}

	let { catalog, nowMs }: Props = $props();

	const state = $derived<PremiumSubscriptionState>(
		premiumSubscriptionState(catalog.subscriptionEndDate, nowMs)
	);
	const daysLeft = $derived(premiumSubscriptionDaysLeft(catalog.subscriptionEndDate, nowMs));
	const endDate = $derived(formatPremiumDate(catalog.subscriptionEndDate));

	// Элемент, для которого поле не пришло, НЕ рисуется: «Устройства: 0 из 0»
	// и «Действует до —» читаются как факты о подписке, хотя означают только
	// то, что портал промолчал.
	const hasDevices = $derived(
		typeof catalog.activeDeviceCount === 'number' && typeof catalog.maxDeviceCount === 'number'
	);
</script>

<div
	class="premium-sub"
	class:premium-sub--expiring={state === 'expiring'}
	class:premium-sub--expired={state === 'expired'}
>
	{#if catalog.planName}
		<p class="premium-sub-plan">{catalog.planName}</p>
	{/if}
	<div class="premium-sub-facts">
		{#if endDate}
			<span class="premium-sub-fact">
				{state === 'expired' ? 'Истекла' : 'Действует до'}
				{endDate}
				{#if state === 'expiring' && daysLeft !== null}
					<span class="premium-sub-days">(осталось {daysLeft} дн.)</span>
				{/if}
			</span>
		{/if}
		{#if hasDevices}
			<span class="premium-sub-fact">
				Устройства: {catalog.activeDeviceCount} из {catalog.maxDeviceCount}
			</span>
		{/if}
	</div>
	{#if state === 'expired'}
		<p class="premium-sub-warning">Подписка истекла — конфигурации не выдаются.</p>
	{/if}
</div>

<style>
	.premium-sub {
		padding: 10px 12px;
		border: 1px solid var(--border, var(--color-border));
		border-radius: 8px;
		background: var(--bg-secondary, var(--color-bg-secondary));
	}

	.premium-sub--expiring {
		border-color: var(--warning, var(--color-warning));
	}

	.premium-sub--expired {
		border-color: var(--error, var(--color-error));
	}

	.premium-sub-plan {
		margin: 0 0 4px;
		font-size: 0.9375rem;
		font-weight: 600;
		color: var(--text-primary, var(--color-text-primary));
	}

	.premium-sub-facts {
		display: flex;
		flex-wrap: wrap;
		gap: 4px 16px;
	}

	.premium-sub-fact {
		font-size: 0.8125rem;
		color: var(--text-secondary, var(--color-text-secondary));
	}

	.premium-sub-days {
		color: var(--warning, var(--color-warning));
	}

	.premium-sub-warning {
		margin: 6px 0 0;
		font-size: 0.8125rem;
		color: var(--error, var(--color-error));
	}
</style>
