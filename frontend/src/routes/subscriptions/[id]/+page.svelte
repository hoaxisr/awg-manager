<script lang="ts">
	import { page } from '$app/stores';
	import { onMount, onDestroy } from 'svelte';
	import { api } from '$lib/api/client';
	import type { Subscription } from '$lib/types';
	import { PageContainer, PageHeader, LoadingSpinner } from '$lib/components/layout';
	import { Tabs, GridListToggle } from '$lib/components/ui';
	import SubscriptionMembersTab from '$lib/components/subscriptions/SubscriptionMembersTab.svelte';
	import SubscriptionSettingsTab from '$lib/components/subscriptions/SubscriptionSettingsTab.svelte';
	import { usageLevel } from '$lib/stores/settings';
	import {
		SINGBOX_LAYOUT_STORAGE_KEY,
		TUNNEL_MOBILE_LAYOUT_MAX_WIDTH_PX,
		type SingboxLayoutMode,
	} from '$lib/constants/singboxLayout';

	// Poll Clash for the live "now" pointer this often when on members tab in urltest
	// mode. 5s balances responsiveness with Clash API load.
	const URLTEST_POLL_MS = 5000;

	// Show explicit progress bar only for subscriptions where the per-member
	// stream is long enough to be worth the visual. Below this threshold the
	// generic spinner suffices because total render is sub-second.
	const PROGRESS_BAR_THRESHOLD = 5;

	const id = $derived($page.params.id ?? '');
	let subscription = $state<Subscription | null>(null);
	let loading = $state(true);
	let error = $state('');
	let progressTotal = $state(0);
	let progressLoaded = $state(0);

	let active = $state<'members' | 'settings'>('members');
	let membersAutoDelayCheckNonce = $state(0);
	let liveActiveMember = $state<string | null>(null);
	let currentSubscriptionSurface = '';
	let subscriptionSurfaceEntryNonce = $state(0);
	let lastAutoDelayCheckKey = '';

	let singboxLayoutMode = $state<SingboxLayoutMode>('grid');
	let singboxLayoutReady = false;
	let isSingboxMembersMobile = $state(false);
	const showSingboxListOption = $derived($usageLevel !== 'basic');
	const singboxEffectiveLayout = $derived<SingboxLayoutMode>(
		isSingboxMembersMobile || (!showSingboxListOption && singboxLayoutMode === 'list')
			? 'grid'
			: singboxLayoutMode,
	);
	const showSingboxGridListToggle = $derived(showSingboxListOption && !isSingboxMembersMobile);

	function isSingboxLayoutMode(value: string | null): value is SingboxLayoutMode {
		return value === 'grid' || value === 'list';
	}

	let evtSrc: EventSource | null = null;

	function loadStream(): void {
		if (!id) return;
		progressLoaded = 0;
		progressTotal = 0;
		loading = true;
		error = '';
		subscription = null;
		evtSrc?.close();
		evtSrc = new EventSource(
			`/api/singbox/subscriptions/get-stream?id=${encodeURIComponent(id)}`,
		);
		// Guard against onerror firing right after a clean done — browser emits
		// onerror on the closed connection, but we treat that as success.
		let streamDone = false;

		evtSrc.addEventListener('meta', (e) => {
			const meta = JSON.parse((e as MessageEvent).data);
			subscription = {
				...meta,
				members: [],
				memberTags: [],
				orphanTags: [],
				activeMember: '',
			} as Subscription;
			progressTotal = meta.total ?? 0;
		});

		evtSrc.addEventListener('member', (e) => {
			if (!subscription) return;
			const { member } = JSON.parse((e as MessageEvent).data);
			subscription.members = [...(subscription.members ?? []), member];
			subscription.memberTags = [...subscription.memberTags, member.tag];
			progressLoaded += 1;
		});

		evtSrc.addEventListener('done', (e) => {
			if (subscription) {
				const data = JSON.parse((e as MessageEvent).data);
				subscription.orphanTags = data.orphanTags ?? [];
				subscription.activeMember = data.activeMember ?? '';
			}
			streamDone = true;
			loading = false;
			evtSrc?.close();
			evtSrc = null;
		});

		evtSrc.onerror = () => {
			if (streamDone) return; // already completed cleanly — ignore connection-close error
			// Browser fires onerror on connection drop. Surface partial state
			// if we got members, generic error otherwise.
			if (progressLoaded > 0 && progressTotal > 0) {
				error = `Загружено ${progressLoaded} из ${progressTotal} серверов. Соединение прервалось.`;
			} else {
				error = 'Не удалось загрузить подписку';
			}
			loading = false;
			evtSrc?.close();
			evtSrc = null;
		};
	}

	onMount(() => {
		const sb = localStorage.getItem(SINGBOX_LAYOUT_STORAGE_KEY);
		if (isSingboxLayoutMode(sb)) singboxLayoutMode = sb;
		singboxLayoutReady = true;
		loadStream();
	});

	onMount(() => {
		const media = window.matchMedia(`(max-width: ${TUNNEL_MOBILE_LAYOUT_MAX_WIDTH_PX}px)`);
		const sync = (event?: MediaQueryList | MediaQueryListEvent) => {
			isSingboxMembersMobile = event ? event.matches : media.matches;
		};
		sync(media);
		media.addEventListener('change', sync);
		return () => media.removeEventListener('change', sync);
	});
	onDestroy(() => {
		evtSrc?.close();
		evtSrc = null;
	});

	$effect(() => {
		const surface = `${id}:${active}`;
		if (surface === currentSubscriptionSurface) return;
		currentSubscriptionSurface = surface;
		subscriptionSurfaceEntryNonce += 1;
	});

	// Poll Clash live "now" every 5s for urltest mode. Selector mode doesn't need
	// this — its activeMember is whatever the user picked and Clash echoes it.
	$effect(() => {
		const sub = subscription;
		if (loading || error || !sub || sub.mode !== 'urltest' || active !== 'members') {
			liveActiveMember = null;
			return;
		}
		let cancelled = false;
		const tick = async (): Promise<void> => {
			try {
				const res = await api.getSubscriptionActiveNow(sub.id);
				if (!cancelled) liveActiveMember = res.now || null;
			} catch {
				if (!cancelled) liveActiveMember = null;
			}
		};
		void tick();
		const handle = setInterval(() => void tick(), URLTEST_POLL_MS);
		return () => {
			cancelled = true;
			clearInterval(handle);
		};
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

	$effect(() => {
		if (!singboxLayoutReady) return;
		localStorage.setItem(SINGBOX_LAYOUT_STORAGE_KEY, singboxLayoutMode);
	});
</script>

<svelte:head>
	<title>{subscription?.label ?? 'Подписка'} - AWG Manager</title>
</svelte:head>

<PageContainer width="full">
	{#if !subscription && loading}
		<!-- Initial spinner before meta arrives (any subscription size) -->
		<div class="loading-centered">
			<LoadingSpinner size="md" message="Загружаем подписку..." />
		</div>
	{:else if !subscription && error}
		<div class="err">{error}</div>
	{:else if subscription}
		<PageHeader title={subscription.label || subscription.url} backTo="/?tab=subscriptions" />
		<Tabs
			tabs={[
				{ id: 'members', label: `Серверы (${subscription.memberTags.length})` },
				{ id: 'settings', label: 'Настройки' },
			]}
			active={active}
			onchange={(tabId) => (active = tabId as 'members' | 'settings')}
		/>
		{#if loading && progressTotal > PROGRESS_BAR_THRESHOLD}
			<div class="loading-progress">
				<div class="progress-text">
					Загружено {progressLoaded} из {progressTotal} серверов
				</div>
				<div class="progress-bar">
					<div class="progress-fill" style="width: {(progressLoaded / progressTotal) * 100}%"></div>
				</div>
			</div>
		{/if}
		{#if error}
			<div class="err">{error}</div>
		{/if}
		<section class="content">
			{#if active === 'members'}
				{#if subscription.memberTags.length > 0}
					<div class="members-toolbar">
						<GridListToggle
							value={singboxEffectiveLayout}
							showListOption={showSingboxGridListToggle}
							onchange={(v) => (singboxLayoutMode = v)}
						/>
					</div>
				{/if}
				<SubscriptionMembersTab
					{subscription}
					{liveActiveMember}
					onUpdated={loadStream}
					autoDelayCheckNonce={membersAutoDelayCheckNonce}
					layout={singboxEffectiveLayout}
				/>
			{:else}
				<SubscriptionSettingsTab {subscription} onUpdated={loadStream} />
			{/if}
		</section>
	{/if}
</PageContainer>

<style>
	.err { color: #f85149; margin-top: 1rem; }
	.content { margin-top: 1rem; }
	.members-toolbar {
		display: flex;
		justify-content: flex-end;
		margin-bottom: 0.75rem;
	}
	.loading-progress {
		margin: 1rem 0;
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
	}
	.progress-text {
		font-size: 0.9rem;
		color: var(--color-text-muted);
		text-align: center;
	}
	.progress-bar {
		height: 6px;
		background: var(--color-bg-tertiary);
		border-radius: 3px;
		overflow: hidden;
	}
	.progress-fill {
		height: 100%;
		background: var(--color-accent);
		transition: width 200ms ease-out;
	}
</style>
