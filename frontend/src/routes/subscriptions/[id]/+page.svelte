<script lang="ts">
	import { page } from '$app/stores';
	import { onMount } from 'svelte';
	import { api } from '$lib/api/client';
	import type { Subscription } from '$lib/types';
	import { PageContainer, PageHeader } from '$lib/components/layout';
	import { Tabs } from '$lib/components/ui';
	import SubscriptionMembersTab from '$lib/components/subscriptions/SubscriptionMembersTab.svelte';
	import SubscriptionSettingsTab from '$lib/components/subscriptions/SubscriptionSettingsTab.svelte';

	const id = $derived($page.params.id ?? '');
	let subscription = $state<Subscription | null>(null);
	let loading = $state(true);
	let error = $state('');

	let active = $state<'members' | 'settings'>('members');
	let membersAutoDelayCheckNonce = $state(0);
	let currentSubscriptionSurface = '';
	let subscriptionSurfaceEntryNonce = $state(0);
	let lastAutoDelayCheckKey = '';

	async function reload(): Promise<void> {
		try {
			subscription = await api.getSubscription(id);
		} catch (e) {
			error = e instanceof Error ? e.message : 'Не удалось загрузить';
		} finally {
			loading = false;
		}
	}

	onMount(reload);

	$effect(() => {
		const surface = `${id}:${active}`;
		if (surface === currentSubscriptionSurface) return;
		currentSubscriptionSurface = surface;
		subscriptionSurfaceEntryNonce += 1;
	});

	$effect(() => {
		const sub = subscription;
		const entryNonce = subscriptionSurfaceEntryNonce;

		if (loading || error || !sub || active !== 'members') return;

		const tags = (sub.members && sub.members.length > 0 ? sub.members.map((m) => m.tag) : sub.memberTags)
			.filter(Boolean)
			.sort()
			.join(',');

		if (!tags) return;

		const key = `${entryNonce}:${sub.id}:${tags}`;
		if (key === lastAutoDelayCheckKey) return;

		lastAutoDelayCheckKey = key;
		membersAutoDelayCheckNonce += 1;
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
				<SubscriptionMembersTab
					{subscription}
					onUpdated={reload}
					autoDelayCheckNonce={membersAutoDelayCheckNonce}
				/>
			{:else}
				<SubscriptionSettingsTab {subscription} onUpdated={reload} />
			{/if}
		</section>
	{/if}
</PageContainer>

<style>
	.err { color: #f85149; }
	.content { margin-top: 1rem; }
</style>
