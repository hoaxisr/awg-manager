<script lang="ts">
	// «Диагностика» — разовые проверки. Оперативные поверхности (Журнал,
	// Мониторинг, Соединения) вынесены отдельными пунктами сайдбара, здесь
	// остаётся то, что открывают по случаю.
	import { onMount } from 'svelte';
	import { page } from '$app/stores';
	import { goto } from '$app/navigation';
	import { api } from '$lib/api/client';
	import type { TunnelListItem, SingboxTunnel, Subscription } from '$lib/types';
	import type { DiagnosticsTargetSeed } from '$lib/stores/diagnostics';
	import { PageContainer, PageHeader } from '$lib/components/layout';
	import { Tabs } from '$lib/components/ui';
	import ChecksTab from './ChecksTab.svelte';
	import AboutDeviceTab from './AboutDeviceTab.svelte';
	import AwgConfigAnalyzerTab from './AwgConfigAnalyzerTab.svelte';
	import DnsInfoTab from './DnsInfoTab.svelte';

	type ActiveTab = 'checks' | 'about' | 'awgConfig' | 'dns';

	function initialTab(): ActiveTab {
		const tab = $page.url.searchParams.get('tab');
		if (tab === 'about') return 'about';
		if (tab === 'awgConfig') return 'awgConfig';
		if (tab === 'dns') return 'dns';
		return 'checks';
	}

	function singboxKind(protocol: string, security?: string): string {
		if (protocol === 'vless' && security === 'reality') return 'xray';
		if (protocol === 'vless') return 'vless';
		if (protocol === 'hysteria2') return 'hy2';
		if (protocol === 'naive') return 'ss';
		return protocol;
	}

	let activeTab = $state<ActiveTab>(initialTab());
	let tunnels = $state<DiagnosticsTargetSeed[]>([]);

	const tabs: { id: ActiveTab; label: string }[] = [
		{ id: 'checks', label: 'Проверки' },
		{ id: 'about', label: 'Окружение' },
		{ id: 'awgConfig', label: 'Конфиг AWG' },
		{ id: 'dns', label: 'Сведения о DNS' },
	];

	// Санитайзер старых значений: ?tab=tests / ?tab=dnscheck переписываем в
	// checks ДО того, как примитив Tabs прочитает URL.
	{
		const sp = new URLSearchParams($page.url.search);
		const t = sp.get('tab');
		if (t === 'tests' || t === 'dnscheck') {
			sp.set('tab', 'checks');
			const url = $page.url.pathname + (sp.toString() ? `?${sp}` : '') + $page.url.hash;
			void goto(url, { replaceState: true, keepFocus: true, noScroll: true });
		}
	}

	onMount(async () => {
		// Три источника целей для рейла проверок:
		//   1. AWG/managed туннели (системные NativeWG и внешние исключены —
		//      диагностику по ним гонять нельзя);
		//   2. sing-box туннели (строка на outbound);
		//   3. активные члены включённых подписок (с префиксом singbox).
		// Отказ необязательных источников молча вырождается в пустой список.
		try {
			const [snap, singboxTunnels, subscriptions] = await Promise.all([
				api.getTunnelsAll(),
				api.singboxListTunnels().catch(() => [] as SingboxTunnel[]),
				api.listSubscriptions().catch(() => [] as Subscription[]),
			]);

			const awg: DiagnosticsTargetSeed[] = (snap.tunnels ?? []).map((t: TunnelListItem) => ({
				id: t.id,
				name: t.name,
				status: t.status,
				kind: t.awgVersion ?? 'awg',
			}));

			const singbox: DiagnosticsTargetSeed[] = singboxTunnels.map((t) => ({
				id: `singbox:${t.tag}`,
				name: t.tag,
				status: t.running ? 'running' : 'stopped',
				kind: singboxKind(t.protocol, t.security),
			}));

			const subscriptionMembers: DiagnosticsTargetSeed[] = [];
			for (const sub of subscriptions) {
				if (!sub.enabled) continue;
				const activeTag =
					(sub.activeMember && sub.memberTags.includes(sub.activeMember)
						? sub.activeMember
						: sub.memberTags[0]) ?? '';
				if (!activeTag) continue;
				const m = (sub.members ?? []).find((member) => member.tag === activeTag);
				subscriptionMembers.push({
					id: `singbox:${activeTag}`,
					// Имя подписки понятнее сырого тега outbound'а.
					name: sub.label || m?.label || activeTag,
					kind: m?.protocol ? singboxKind(m.protocol) : undefined,
					// Члены проверяются через процесс sing-box — для видимости
					// в рейле считаем их запущенными.
					status: 'running',
				});
			}

			// Члены подписок идут перед сырыми туннелями sing-box, чтобы при
			// совпадении id в дедуп-карте побеждало понятное имя.
			const uniq = new Map<string, DiagnosticsTargetSeed>();
			for (const t of [...awg, ...subscriptionMembers, ...singbox]) {
				if (!uniq.has(t.id)) uniq.set(t.id, t);
			}
			tunnels = Array.from(uniq.values());
		} catch {
			tunnels = [];
		}
	});

	const pageTitle = $derived(
		activeTab === 'about'
			? 'Окружение · Диагностика'
			: activeTab === 'awgConfig'
				? 'Конфиг AWG · Диагностика'
				: activeTab === 'dns'
					? 'Сведения о DNS · Диагностика'
					: 'Проверки · Диагностика',
	);
</script>

<svelte:head>
	<title>{pageTitle} - AWGM</title>
</svelte:head>

<PageContainer>
	<PageHeader title="Диагностика" />

	<Tabs
		{tabs}
		active={activeTab}
		onchange={(id) => (activeTab = id as ActiveTab)}
		urlParam="tab"
		defaultTab="checks"
	/>

	{#if activeTab === 'checks'}
		<ChecksTab {tunnels} />
	{:else if activeTab === 'about'}
		<AboutDeviceTab />
	{:else if activeTab === 'awgConfig'}
		<AwgConfigAnalyzerTab />
	{:else if activeTab === 'dns'}
		<DnsInfoTab />
	{/if}
</PageContainer>
