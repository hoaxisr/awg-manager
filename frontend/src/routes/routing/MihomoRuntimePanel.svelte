<script lang="ts">
	import { onMount } from 'svelte';
	import { Activity, RefreshCw, Search, Zap } from 'lucide-svelte';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { subscriptionsStore } from '$lib/stores/subscriptions';
	import type { MihomoNativeSubscription, MihomoRuntimeProvider, MihomoRuntimeProxy } from '$lib/types';
	import { Badge, Button, SegmentedControl } from '$lib/components/ui';

	let view = $state<'groups' | 'providers'>('groups');
	let groups = $state<MihomoRuntimeProxy[]>([]);
	let providers = $state<MihomoRuntimeProvider[]>([]);
	let nativeSubscriptions = $state<MihomoNativeSubscription[]>([]);
	let query = $state('');
	let loading = $state(true);
	let testing = $state<string | null>(null);
	let refreshingProvider = $state<string | null>(null);
	let delays = $state<Record<string, number>>({});
	const legacySubscriptions = subscriptionsStore;

	function displayName(name: string): string {
		const native = nativeSubscriptions.find((sub) => sub.groupName === name);
		if (native) return native.name;
		for (const sub of $legacySubscriptions.data ?? []) {
			if (sub.selectorTag === name) return sub.label || name;
			const member = sub.members?.find((item) => item.tag === name);
			if (member?.label) return member.label;
		}
		return name;
	}

	function providerName(name: string): string {
		return nativeSubscriptions.find((sub) => sub.providerName === name)?.name ?? name;
	}

	const visibleGroups = $derived(groups.filter((group) => {
		const needle = query.trim().toLocaleLowerCase();
		return !needle || displayName(group.name).toLocaleLowerCase().includes(needle) || group.all?.some((name) => displayName(name).toLocaleLowerCase().includes(needle));
	}));

	async function load(silent = false) {
		if (!silent) loading = true;
		try {
			const [proxyData, providerData, nativeSubs] = await Promise.all([
				api.mihomoRuntimeProxies(),
				api.mihomoRuntimeProviders().catch(() => ({ providers: {} })),
				api.mihomoNativeSubscriptions().catch(() => []),
			]);
			nativeSubscriptions = nativeSubs;
			groups = Object.values(proxyData.proxies ?? {})
				.filter((item) => Array.isArray(item.all) && item.all.length > 0)
				.sort((a, b) => a.name.localeCompare(b.name));
			providers = Object.entries(providerData.providers ?? {})
				.map(([name, provider]) => ({ ...provider, name: provider.name || name }))
				.sort((a, b) => a.name.localeCompare(b.name));
			const next: Record<string, number> = {};
			for (const proxy of Object.values(proxyData.proxies ?? {})) {
				const latest = proxy.history?.at(-1)?.delay;
				if (latest) next[proxy.name] = latest;
			}
			delays = { ...delays, ...next };
		} catch (error) {
			notifications.error(error instanceof Error ? error.message : String(error));
		} finally { loading = false; }
	}

	async function select(group: MihomoRuntimeProxy, name: string) {
		try {
			await api.mihomoRuntimeSelect(group.name, name);
			group.now = name;
			groups = [...groups];
		} catch (error) { notifications.error(error instanceof Error ? error.message : String(error)); }
	}

	async function testGroup(group: MihomoRuntimeProxy) {
		if (testing) return;
		testing = group.name;
		const members = (group.all ?? []).filter((name) => !groups.some((candidate) => candidate.name === name));
		const results = await Promise.allSettled(members.map(async (name) => [name, await api.mihomoRuntimeDelay(name)] as const));
		const next = { ...delays };
		for (const result of results) if (result.status === 'fulfilled') next[result.value[0]] = result.value[1];
		delays = next;
		testing = null;
	}

	async function refreshProvider(provider: MihomoRuntimeProvider) {
		refreshingProvider = provider.name;
		try {
			await api.mihomoRuntimeRefreshProvider(provider.name);
			await load(true);
			notifications.success(`Провайдер «${provider.name}» обновлён`);
		} catch (error) { notifications.error(error instanceof Error ? error.message : String(error)); }
		finally { refreshingProvider = null; }
	}

	function delayTone(delay?: number): 'success' | 'warning' | 'error' | 'muted' {
		if (!delay) return 'muted';
		if (delay < 180) return 'success';
		if (delay < 700) return 'warning';
		return 'error';
	}

	onMount(() => { void subscriptionsStore.refetch(); void load(); });
</script>

<section class="runtime">
	<div class="toolbar">
		<SegmentedControl value={view} options={[{ value: 'groups', label: `Прокси (${groups.length})` }, { value: 'providers', label: `Провайдеры (${providers.length})` }]} onchange={(value) => view = value as typeof view} ariaLabel="Раздел Mihomo" />
		<label class="search"><Search size={15} /><input bind:value={query} placeholder="Поиск групп и прокси" /></label>
		<Button variant="secondary" size="sm" onclick={() => load()} disabled={loading}><span class:spin={loading}><RefreshCw size={14} /></span>Обновить</Button>
	</div>

	{#if view === 'groups'}
		<div class="groups">
			{#each visibleGroups as group (group.name)}
				<article class="group-card">
					<header>
						<div><h3>{displayName(group.name)}</h3><span>{group.type} · выбрано {group.now ? displayName(group.now) : '—'}</span></div>
						<button class="test" onclick={() => testGroup(group)} disabled={testing !== null} title="Проверить задержку"><span class:spin={testing === group.name}><Zap size={15} /></span></button>
					</header>
					<div class="nodes">
						{#each group.all ?? [] as name}
							<button class="node" class:selected={group.now === name} onclick={() => select(group, name)}>
								<strong title={name}>{displayName(name)}</strong>
								<span>{name === 'DIRECT' || name === 'REJECT' ? name.toLocaleLowerCase() : 'proxy'}</span>
								<Badge variant={delayTone(delays[name])}>{delays[name] ? `${delays[name]} ms` : '—'}</Badge>
							</button>
						{/each}
					</div>
				</article>
			{/each}
			{#if !loading && visibleGroups.length === 0}<div class="empty">Группы не найдены. Примените конфигурацию Mihomo или измените поиск.</div>{/if}
		</div>
	{:else}
		<div class="providers">
			{#each providers as provider (provider.name)}
				<article class="provider-card">
					<div class="provider-icon"><Activity size={18} /></div>
					<div><h3>{providerName(provider.name)}</h3><p><span title={provider.name}>{provider.vehicleType || provider.type}</span> · {provider.proxies?.length ?? 0} прокси{provider.updatedAt ? ` · ${new Date(provider.updatedAt).toLocaleString()}` : ''}</p></div>
					<Button variant="secondary" size="sm" onclick={() => refreshProvider(provider)} loading={refreshingProvider === provider.name}>Обновить</Button>
				</article>
			{/each}
			{#if !loading && providers.length === 0}<div class="empty">Провайдеров пока нет.</div>{/if}
		</div>
	{/if}
</section>

<style>
	.runtime{display:grid;gap:14px}.toolbar{display:flex;align-items:center;gap:10px;flex-wrap:wrap}.search{height:32px;min-width:240px;flex:1;display:flex;align-items:center;gap:7px;padding:0 10px;border:1px solid var(--border);border-radius:var(--radius-sm);background:var(--bg-secondary);color:var(--text-muted)}.search input{min-width:0;width:100%;border:0;outline:0;background:transparent;color:var(--text-primary);font:inherit}.groups{display:grid;grid-template-columns:repeat(auto-fit,minmax(420px,1fr));gap:14px}.group-card,.provider-card{border:1px solid var(--border);border-radius:var(--radius-md);background:var(--bg-secondary);box-shadow:var(--shadow-sm)}.group-card{padding:14px}.group-card header{display:flex;justify-content:space-between;align-items:center;gap:10px;margin-bottom:12px}.group-card h3,.provider-card h3{margin:0;font-size:14px}.group-card header span,.provider-card p{margin:3px 0 0;color:var(--text-muted);font-size:12px}.test{display:grid;place-items:center;width:30px;height:30px;border:0;border-radius:7px;background:transparent;color:var(--text-muted);cursor:pointer}.test:hover{background:var(--bg-tertiary);color:var(--accent)}.nodes{display:grid;grid-template-columns:repeat(auto-fill,minmax(145px,1fr));gap:8px}.node{position:relative;display:grid;grid-template-columns:minmax(0,1fr) auto;gap:3px 7px;padding:10px;text-align:left;border:1px solid var(--border);border-radius:8px;background:var(--bg-primary);color:var(--text-primary);cursor:pointer}.node:hover{border-color:var(--accent-line)}.node.selected{border-color:var(--accent);background:var(--accent-soft)}.node strong{overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-size:12px}.node span{grid-row:2;color:var(--text-muted);font-size:10px}.node :global(.badge){grid-column:2;grid-row:1 / span 2;align-self:center}.providers{display:grid;gap:10px}.provider-card{display:grid;grid-template-columns:auto minmax(0,1fr) auto;align-items:center;gap:12px;padding:13px}.provider-icon{display:grid;place-items:center;width:36px;height:36px;border-radius:9px;background:var(--accent-soft);color:var(--accent)}.empty{padding:30px;text-align:center;border:1px dashed var(--border);border-radius:var(--radius-md);color:var(--text-muted)}.spin{animation:spin .8s linear infinite}@keyframes spin{to{transform:rotate(360deg)}}@media(max-width:650px){.groups{grid-template-columns:1fr}.toolbar>*{width:100%}.search{min-width:0}.nodes{grid-template-columns:1fr 1fr}.provider-card{grid-template-columns:auto 1fr}.provider-card :global(button){grid-column:1/-1;width:100%}}
</style>
