<script lang="ts">
	import { page } from '$app/stores';
	import { goto } from '$app/navigation';
	import { onMount } from 'svelte';
	import { api } from '$lib/api/client';
	import type { Subscription } from '$lib/types';
	import { PageContainer, PageHeader } from '$lib/components/layout';
	import { Tabs, Button } from '$lib/components/ui';
	import SubscriptionMembersTab from '$lib/components/subscriptions/SubscriptionMembersTab.svelte';
	import SubscriptionSettingsTab from '$lib/components/subscriptions/SubscriptionSettingsTab.svelte';
	const SOFT_CARD_LATENCY_EVENT = 'awgm:soft-card-latency-refresh';
	let warmupTimer: ReturnType<typeof setTimeout> | null = null;
	let lastWarmupTab = $state<'members' | 'settings' | ''>('');

	const id = $derived($page.params.id ?? '');
	let subscription = $state<Subscription | null>(null);
	let loading = $state(true);
	let error = $state('');

	let active = $state<'members' | 'settings'>('members');

	function goBack(): void {
		if (typeof window !== 'undefined' && window.history.length > 1) {
			window.history.back();
			return;
		}
		goto('/');
	}

	async function reload(): Promise<void> {
		try {
			subscription = await api.getSubscription(id);
		} catch (e) {
			error = e instanceof Error ? e.message : 'Не удалось загрузить';
		} finally {
			loading = false;
		}
	}

	function scheduleMembersWarmup(): void {
		if (warmupTimer) clearTimeout(warmupTimer);
		warmupTimer = setTimeout(() => {
			window.dispatchEvent(
				new CustomEvent(SOFT_CARD_LATENCY_EVENT, { detail: { target: 'subscription-members' } }),
			);
		}, 900);
	}

	onMount(() => {
		void reload();
		return () => {
			if (warmupTimer) clearTimeout(warmupTimer);
		};
	});

	$effect(() => {
		if (active !== 'members') {
			lastWarmupTab = '';
			return;
		}
		if (loading || !subscription) return;
		if (lastWarmupTab === active) return;
		lastWarmupTab = active;
		scheduleMembersWarmup();
	});
</script>

<svelte:head>
	<title>{subscription?.label ?? 'Подписка'} - AWG Manager</title>
</svelte:head>

<PageContainer>
	{#if loading}
		<div>Загрузка...</div>
	{:else if error || !subscription}
		<div class="err">{error}</div>
	{:else}
		<PageHeader title={subscription.label || subscription.url} backTo="/?tab=subscriptions" />
		<Tabs
			tabs={[
				{ id: 'members', label: `Серверы (${subscription.memberTags.length})` },
				{ id: 'settings', label: 'Настройки' },
			]}
			active={active}
			onchange={(tabId) => (active = tabId as 'members' | 'settings')}
		/>
		<section class="content">
			{#if active === 'members'}
				<SubscriptionMembersTab {subscription} onUpdated={reload} />
			{:else}
				<SubscriptionSettingsTab {subscription} onUpdated={reload} />
			{/if}
		</section>
		<hr class="divider">
		<div class="actions">
			<Button variant="primary" size="sm" onclick={goBack}>Назад к подпискам</Button>
		</div>
	{/if}
</PageContainer>

<style>
	.err { color: #f85149; }
	.actions { margin-top: 0.5rem; }
	.content { margin-top: 1rem; }
	.divider { margin: 1rem 0; }
</style>
