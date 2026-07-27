<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/stores';
	import { api } from '$lib/api/client';
	import type { Subscription } from '$lib/types';
	import TunnelDiagnosticsPanel from '$lib/components/testing/TunnelDiagnosticsPanel.svelte';

	let subscriptionId = $derived($page.params.id as string);
	let subscription: Subscription | null = $state(null);
	let loaded = $state(false);

	let selectorTag = $derived.by(() => {
		return subscription ? subscription.selectorTag : '';
	});
	let kernelIface = $derived.by(() => {
		if (subscription && subscription.proxyIndex >= 0) {
			return `t2s${subscription.proxyIndex}`;
		}
		return '';
	});
	let displayName = $derived.by(() => {
		if (subscription?.label) return subscription.label;
		if (selectorTag) return selectorTag;
		return subscriptionId;
	});

	let unavailableReason = $derived.by(() => {
		if (!loaded) return undefined;
		if (!subscription) return 'Подписка не найдена.';
		if (!selectorTag || !kernelIface) return 'Для подписки не удалось определить интерфейс тестирования.';
		return undefined;
	});

	onMount(async () => {
		try {
			subscription = await api.getSubscription(subscriptionId);
		} catch {
			subscription = null;
		} finally {
			loaded = true;
		}
	});
</script>

<TunnelDiagnosticsPanel
	kind="subscription"
	targetId={selectorTag}
	{displayName}
	backHref="/sb/subscriptions"
	backLabel="К списку подписок"
	subjectLabel="подписку"
	iface={kernelIface}
	loading={!loaded}
	{unavailableReason}
/>
