<script lang="ts">
	import { onMount } from 'svelte';
	import { AlertTriangle, ChevronDown, ChevronUp, Pencil, Plus, RefreshCw, Save, Trash2, Users, Waypoints } from 'lucide-svelte';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { subscriptionsStore } from '$lib/stores/subscriptions';
	import type { MihomoNativeGroup, MihomoNativeProxy, MihomoNativeRule, MihomoNativeRuleProvider, MihomoNativeSubscription, MihomoRuntimeProvider, MihomoRuntimeProxy } from '$lib/types';
	import { MIHOMO_SUPPORTED_RULE_TYPES } from '$lib/types/mihomoRuleTypes.generated';
	import { Badge, Button, Card, Modal, SegmentedControl } from '$lib/components/ui';

	let section = $state<'rules' | 'groups'>('groups');
	let groups = $state<MihomoNativeGroup[]>([]);
	let rules = $state<MihomoNativeRule[]>([]);
	let rulesRevision = $state<number>(0);
	let confirmedRules = $state<MihomoNativeRule[]>([]);
	let ruleMutationInFlight = $state(false);
	let ruleStatusMessage = $state('');

	let reorderInFlight = false;
	let pendingReorder: string[] | null = null;
	let reorderDebounceTimer: ReturnType<typeof setTimeout> | null = null;
	let ruleProviders = $state<MihomoNativeRuleProvider[]>([]);
	let proxies = $state<MihomoNativeProxy[]>([]);
	let subscriptions = $state<MihomoNativeSubscription[]>([]);
	let runtime = $state<MihomoRuntimeProxy[]>([]);
	let runtimeProviders = $state<MihomoRuntimeProvider[]>([]);
	let unsupportedRules = $state<MihomoNativeRule[]>([]);
	let unsupportedRevision = $state('');
	let unsupportedModalOpen = $state(false);
	let deletingUnsupported = $state(false);
	let loading = $state(true);
	let saving = $state(false);

	let groupForm = $state(false);
	let groupAdvanced = $state(false);
	let editGroupId = $state('');
	let groupName = $state('');
	let groupType = $state<MihomoNativeGroup['type']>('select');
	let groupMembers = $state<string[]>([]);
	let groupProviders = $state<string[]>([]);
	let groupURL = $state('https://www.gstatic.com/generate_204');
	let groupInterval = $state(300);
	let groupStrategy = $state<MihomoNativeGroup['strategy']>('consistent-hashing');
	let groupLazy = $state(true);
	let groupTolerance = $state(50);
	let groupTimeout = $state(5000);
	let groupMaxFailedTimes = $state(5);
	let groupDisableUDP = $state(false);
	let groupIncludeAllProxies = $state(false);
	let groupIncludeAllProviders = $state(false);
	let groupFilter = $state('');
	let groupExcludeFilter = $state('');
	let groupExcludeType = $state('');
	let groupExpectedStatus = $state('');

	let ruleForm = $state(false);
	let editRuleId = $state('');
	let ruleType = $state('DOMAIN-SUFFIX');
	let rulePayload = $state('');
	let ruleOutbound = $state('DIRECT');
	let ruleNoResolve = $state(false);

	let providerForm = $state(false);
	let editProviderId = $state('');
	let providerName = $state('');
	let providerType = $state<MihomoNativeRuleProvider['type']>('http');
	let providerURL = $state('');
	let providerPath = $state('');
	let providerBehavior = $state<MihomoNativeRuleProvider['behavior']>('classical');
	let providerFormat = $state<MihomoNativeRuleProvider['format']>('yaml');
	let providerInterval = $state(86400);

	const ruleTypes = MIHOMO_SUPPORTED_RULE_TYPES;

	function displayName(name: string): string {
		const native = subscriptions.find((sub) => sub.groupName === name);
		if (native) return native.name;
		for (const sub of $subscriptionsStore.data ?? []) {
			if (sub.selectorTag === name) return sub.label || name;
			const member = sub.members?.find((item) => item.tag === name);
			if (member?.label) return member.label;
		}
		return name;
	}

	const memberOptions = $derived.by(() => {
		const values = new Set<string>(['DIRECT', 'REJECT']);
		const groupedNodes = new Set(runtime.filter((item) => item.all?.length).flatMap((item) => item.all ?? []));
		for (const proxy of proxies) if (proxy.enabled && proxy.selectedEngine === 'mihomo') values.add(proxy.name);
		for (const item of runtime) {
			if (item.name === 'GLOBAL' || item.name === groupName) continue;
			if (item.all?.length || !groupedNodes.has(item.name)) values.add(item.name);
		}
		for (const group of groups) if (group.name !== groupName) values.add(group.name);
		return [...values].map((value) => ({ value, label: displayName(value) })).sort((a, b) => a.label.localeCompare(b.label));
	});

	const outputOptions = $derived.by(() => {
		const values = new Set<string>(['DIRECT', 'REJECT']);
		for (const group of groups) values.add(group.name);
		for (const sub of subscriptions) if (sub.enabled && sub.groupName) values.add(sub.groupName);
		for (const item of runtime) if (item.all?.length && item.name !== 'GLOBAL') values.add(item.name);
		for (const proxy of proxies) if (proxy.enabled && proxy.selectedEngine === 'mihomo') values.add(proxy.name);
		return [...values].map((value) => ({ value, label: displayName(value) }));
	});

	async function load() {
		loading = true;
		try {
			const [g, rData, rp, p, s, rt, providers, unsupp] = await Promise.all([
				api.mihomoNativeGroups(),
				api.mihomoNativeRulesWithRevision().catch(async () => ({ items: await api.mihomoNativeRules(), revision: 0 })),
				api.mihomoNativeRuleProviders(),
				api.mihomoNativeProxies(),
				api.mihomoNativeSubscriptions(),
				api.mihomoRuntimeProxies().catch(() => ({ proxies: {} })),
				api.mihomoRuntimeProviders().catch(() => ({ providers: {} })),
				api.mihomoNativeUnsupportedRules().catch(() => ({ items: [], revision: '' })),
			]);
			groups = g;
			rules = rData.items ?? [];
			rulesRevision = rData.revision ?? 0;
			confirmedRules = [...rules];
			ruleProviders = rp; proxies = p; subscriptions = s;
			runtime = Object.values(rt.proxies ?? {});
			runtimeProviders = Object.entries(providers.providers ?? {}).map(([name, provider]) => ({ ...provider, name: provider.name || name }));
			unsupportedRules = unsupp.items ?? [];
			unsupportedRevision = unsupp.revision ?? '';
		} catch (error) { notifications.error(error instanceof Error ? error.message : String(error)); }
		finally { loading = false; }
	}

	function resetGroup() {
		editGroupId = ''; groupName = ''; groupType = 'select'; groupMembers = []; groupProviders = [];
		groupURL = 'https://www.gstatic.com/generate_204'; groupInterval = 300; groupStrategy = 'consistent-hashing';
		groupLazy = true; groupTolerance = 50; groupTimeout = 5000; groupMaxFailedTimes = 5; groupDisableUDP = false;
		groupIncludeAllProxies = false; groupIncludeAllProviders = false; groupFilter = ''; groupExcludeFilter = ''; groupExcludeType = ''; groupExpectedStatus = '';
		groupAdvanced = false;
	}

	function editGroup(group: MihomoNativeGroup) {
		editGroupId = group.id; groupName = group.name; groupType = group.type; groupMembers = [...(group.proxies ?? [])]; groupProviders = [...(group.use ?? [])];
		groupURL = group.url || 'https://www.gstatic.com/generate_204'; groupInterval = group.interval || 300; groupStrategy = group.strategy || 'consistent-hashing';
		groupLazy = group.lazy ?? true; groupTolerance = group.tolerance ?? 50; groupTimeout = group.timeout ?? 5000; groupMaxFailedTimes = group.maxFailedTimes ?? 5;
		groupDisableUDP = group.disableUdp ?? false; groupIncludeAllProxies = group.includeAllProxies ?? false; groupIncludeAllProviders = group.includeAllProviders ?? false;
		groupFilter = group.filter ?? ''; groupExcludeFilter = group.excludeFilter ?? ''; groupExcludeType = group.excludeType ?? ''; groupExpectedStatus = group.expectedStatus ?? '';
		groupAdvanced = Boolean(group.filter || group.excludeFilter || group.excludeType || group.includeAllProxies || group.includeAllProviders || group.disableUdp);
		groupForm = true;
	}

	function toggleMember(name: string) { groupMembers = groupMembers.includes(name) ? groupMembers.filter((item) => item !== name) : [...groupMembers, name]; }
	function toggleProvider(name: string) { groupProviders = groupProviders.includes(name) ? groupProviders.filter((item) => item !== name) : [...groupProviders, name]; }

	async function saveGroup() {
		if (saving) return;
		saving = true;
		try {
			await api.mihomoNativeSaveGroup({
				id: editGroupId || undefined, name: groupName, type: groupType, proxies: groupMembers, use: groupProviders,
				url: groupType === 'select' || groupType === 'relay' ? '' : groupURL, interval: groupType === 'select' || groupType === 'relay' ? 0 : groupInterval,
				lazy: groupLazy, strategy: groupType === 'load-balance' ? groupStrategy : undefined, tolerance: groupType === 'url-test' ? groupTolerance : 0,
				timeout: groupTimeout, maxFailedTimes: groupMaxFailedTimes, disableUdp: groupDisableUDP,
				includeAllProxies: groupIncludeAllProxies, includeAllProviders: groupIncludeAllProviders,
				filter: groupFilter, excludeFilter: groupExcludeFilter, excludeType: groupExcludeType, expectedStatus: groupExpectedStatus, enabled: true,
			});
			groupForm = false; resetGroup(); await load(); notifications.success('Группа Mihomo применена');
		} catch (error) {
			notifications.error(error instanceof Error ? error.message : String(error));
		} finally { saving = false; }
	}

	async function removeGroup(group: MihomoNativeGroup) {
		try { await api.mihomoNativeDeleteGroup(group.id); await load(); }
		catch (error) { notifications.error(error instanceof Error ? error.message : String(error)); }
	}

	function resetRule() { editRuleId = ''; ruleType = 'DOMAIN-SUFFIX'; rulePayload = ''; ruleOutbound = groups[0]?.name || subscriptions[0]?.groupName || 'DIRECT'; ruleNoResolve = false; }
	function editRule(rule: MihomoNativeRule) { editRuleId = rule.id; ruleType = rule.type; rulePayload = rule.payload || ''; ruleOutbound = rule.outbound; ruleNoResolve = rule.noResolve ?? false; ruleForm = true; }
	function cancelPendingReorder() {
		if (reorderDebounceTimer) clearTimeout(reorderDebounceTimer);
		reorderDebounceTimer = null;
		pendingReorder = null;
	}

	async function saveRule() {
		if (saving || ruleMutationInFlight) return;
		saving = true;
		ruleMutationInFlight = true;
		ruleStatusMessage = editRuleId ? 'Обновление правила…' : 'Создание правила…';
		try {
			cancelPendingReorder();
			const mutation = await api.mihomoNativeSaveRuleDetailed({ id: editRuleId || undefined, type: ruleType, payload: ruleType === 'MATCH' ? '' : rulePayload, outbound: ruleOutbound, noResolve: ruleNoResolve, enabled: true });
			rules = [...(mutation.items ?? [])];
			confirmedRules = [...rules];
			rulesRevision = mutation.revision ?? rulesRevision;
			ruleForm = false;
			resetRule();
			notifications.success('Правило Mihomo применено');
		} catch (error) {
			notifications.error(error instanceof Error ? error.message : String(error));
		} finally {
			saving = false;
			ruleMutationInFlight = false;
			ruleStatusMessage = '';
		}
	}

	async function removeRule(rule: MihomoNativeRule) {
		if (ruleMutationInFlight) return;
		const prev = [...rules];
		rules = rules.filter((r) => r.id !== rule.id);
		ruleMutationInFlight = true;
		ruleStatusMessage = 'Удаление правила…';
		try {
			cancelPendingReorder();
			const mutation = await api.mihomoNativeDeleteRuleDetailed(rule.id);
			rules = [...(mutation.items ?? [])];
			confirmedRules = [...rules];
			rulesRevision = mutation.revision ?? rulesRevision;
			notifications.success('Правило удалено');
		} catch (error) {
			rules = prev;
			notifications.error(error instanceof Error ? error.message : String(error));
		} finally {
			ruleMutationInFlight = false;
			ruleStatusMessage = '';
		}
	}

	async function deleteUnsupportedRules() {
		if (deletingUnsupported) return;
		deletingUnsupported = true;
		try {
			const ids = unsupportedRules.map((r) => r.id);
			await api.mihomoNativeDeleteUnsupportedRules(ids, unsupportedRevision, true);
			notifications.success('Неподдерживаемые правила удалены');
			unsupportedModalOpen = false;
			await load();
		} catch (error: any) {
			const is409 = error?.status === 409 || error?.body?.code === 'MIHOMO_RULES_STALE' || String(error?.message).includes('MIHOMO_RULES_STALE') || String(error?.message).includes('rules have been modified');
			if (is409) {
				notifications.warning('Список правил изменился, данные обновлены');
				try {
					const refreshed = await api.mihomoNativeUnsupportedRules();
					unsupportedRules = refreshed.items ?? [];
					unsupportedRevision = refreshed.revision ?? '';
					if (unsupportedRules.length === 0) {
						unsupportedModalOpen = false;
					}
				} catch {
					await load();
				}
			} else {
				notifications.error(error instanceof Error ? error.message : String(error));
			}
		} finally {
			deletingUnsupported = false;
		}
	}

	function resetProvider() { editProviderId = ''; providerName = ''; providerType = 'http'; providerURL = ''; providerPath = ''; providerBehavior = 'classical'; providerFormat = 'yaml'; providerInterval = 86400; }
	function editProvider(provider: MihomoNativeRuleProvider) { editProviderId = provider.id; providerName = provider.name; providerType = provider.type; providerURL = provider.url ?? ''; providerPath = provider.path ?? ''; providerBehavior = provider.behavior; providerFormat = provider.format; providerInterval = provider.interval || 86400; providerForm = true; }
	async function saveProvider() {
		saving = true;
		try { await api.mihomoNativeSaveRuleProvider({ id: editProviderId || undefined, name: providerName, type: providerType, url: providerType === 'http' ? providerURL : '', path: providerPath, behavior: providerBehavior, format: providerFormat, interval: providerInterval, enabled: true }); resetProvider(); providerForm = false; await load(); notifications.success('Rule provider применён'); }
		catch (error) { notifications.error(error instanceof Error ? error.message : String(error)); }
		finally { saving = false; }
	}
	async function removeProvider(provider: MihomoNativeRuleProvider) { try { await api.mihomoNativeDeleteRuleProvider(provider.id); await load(); } catch (error) { notifications.error(error instanceof Error ? error.message : String(error)); } }

	function queueReorder(ids: string[]) {
		if (reorderDebounceTimer) {
			clearTimeout(reorderDebounceTimer);
		}
		reorderDebounceTimer = setTimeout(() => {
			reorderDebounceTimer = null;
			void flushReorder(ids);
		}, 300);
	}

	async function flushReorder(ids: string[]) {
		if (reorderInFlight) {
			pendingReorder = ids;
			return;
		}
		reorderInFlight = true;
		ruleMutationInFlight = true;
		ruleStatusMessage = 'Применение порядка правил…';
		try {
			const res = await api.mihomoNativeReorderRules(ids, rulesRevision);
			if (res.revision !== undefined) {
				rulesRevision = res.revision;
			}
			if (res.items && res.items.length > 0) {
				confirmedRules = [...res.items];
				if (!pendingReorder) {
					rules = [...res.items];
				}
			} else {
				confirmedRules = [...rules];
			}
		} catch (error: any) {
			const is409 = error?.status === 409 || error?.code === 'MIHOMO_RULES_STALE' || error?.body?.code === 'MIHOMO_RULES_STALE' || String(error?.message).includes('MIHOMO_RULES_STALE') || String(error?.message).includes('rules have been modified');
			if (is409) {
				notifications.warning('Список правил изменился, данные обновлены с сервера');
				const serverData = error?.data || error?.body?.data;
				if (serverData?.items) {
					rules = serverData.items;
					confirmedRules = [...serverData.items];
					if (serverData.revision !== undefined) {
						rulesRevision = serverData.revision;
					}
				} else {
					try {
						const refreshed = await api.mihomoNativeRulesWithRevision();
						rules = refreshed.items;
						confirmedRules = [...refreshed.items];
						rulesRevision = refreshed.revision;
					} catch {
						await load();
					}
				}
			} else {
				rules = [...confirmedRules];
				notifications.error(error instanceof Error ? error.message : String(error));
			}
		} finally {
			reorderInFlight = false;
			ruleMutationInFlight = false;
			ruleStatusMessage = '';
			if (pendingReorder) {
				const nextIds = pendingReorder;
				pendingReorder = null;
				void flushReorder(nextIds);
			}
		}
	}

	function moveRule(index: number, delta: number) {
		if (ruleMutationInFlight) return;
		const target = index + delta;
		if (target < 0 || target >= rules.length) return;
		const next = [...rules];
		[next[index], next[target]] = [next[target], next[index]];
		rules = next;
		queueReorder(next.map((rule) => rule.id));
	}

	onMount(() => { void subscriptionsStore.refetch(); void load(); });
</script>

<section class="policy-panel">
	<div class="panel-head">
		<div><h3>Политики Mihomo</h3><p>Собственные правила, rule-providers и proxy-группы. Изменения проверяются ядром перед переключением трафика.</p></div>
		<div class="head-actions">
			{#if ruleMutationInFlight}
				<span class="mutation-badge"><span class="spin"><RefreshCw size={12} /></span> {ruleStatusMessage}</span>
			{/if}
			<SegmentedControl value={section} options={[{ value: 'groups', label: `Группы (${groups.length + subscriptions.filter((sub) => sub.enabled && sub.groupName).length})` }, { value: 'rules', label: `Правила (${rules.length})` }]} onchange={(value) => section = value as typeof section} ariaLabel="Настройки маршрутизации Mihomo" />
			<button class="icon" onclick={load} disabled={ruleMutationInFlight} aria-label="Обновить"><span class:spin={loading}><RefreshCw size={15} /></span></button>
		</div>
	</div>

	{#if section === 'groups'}
		<div class="section-tools"><div><strong>Proxy-группы</strong><span>Ручной выбор, автотест, резервирование, балансировка и цепочки Relay.</span></div><Button variant="primary" size="sm" onclick={() => { resetGroup(); groupForm = !groupForm; }}><Plus size={15} />Группа</Button></div>
		{#if groupForm}
			<Card padding="lg"><div class="editor">
				<div class="form-grid"><label><span>Название</span><input bind:value={groupName} placeholder="Заблокированные сервисы" /></label><label><span>Тип</span><select bind:value={groupType}><option value="select">Select · ручной выбор</option><option value="url-test">URLTest · минимальная задержка</option><option value="fallback">Fallback · первый доступный</option><option value="load-balance">Load Balance</option><option value="relay">Relay · цепочка</option></select></label>{#if groupType !== 'select' && groupType !== 'relay'}<label><span>Интервал, сек.</span><input type="number" min="0" bind:value={groupInterval} /></label>{/if}</div>
				{#if groupType !== 'select' && groupType !== 'relay'}<label><span>URL проверки</span><input bind:value={groupURL} /></label>{/if}
				{#if groupType === 'load-balance'}<label><span>Стратегия</span><select bind:value={groupStrategy}><option value="consistent-hashing">Consistent hashing</option><option value="round-robin">Round robin</option><option value="sticky-sessions">Sticky sessions</option></select></label>{/if}
				<div><span class="field-title">Прокси и вложенные группы</span><div class="checks">{#each memberOptions as option}<button type="button" title={option.value} class:selected={groupMembers.includes(option.value)} onclick={() => toggleMember(option.value)}>{option.label}</button>{/each}</div></div>
				{#if subscriptions.some((sub) => sub.providerName)}<div><span class="field-title">Подписки / proxy-providers</span><div class="checks">{#each subscriptions.filter((sub) => sub.providerName) as sub}<button type="button" class:selected={groupProviders.includes(sub.providerName!)} onclick={() => toggleProvider(sub.providerName!)}>{sub.name}</button>{/each}</div></div>{/if}
				<button class="advanced-toggle" onclick={() => groupAdvanced = !groupAdvanced}>{groupAdvanced ? 'Скрыть' : 'Показать'} дополнительные параметры</button>
				{#if groupAdvanced}<div class="advanced-grid"><label class="inline-check"><input type="checkbox" bind:checked={groupIncludeAllProxies} /> Все локальные прокси</label><label class="inline-check"><input type="checkbox" bind:checked={groupIncludeAllProviders} /> Все providers</label><label class="inline-check"><input type="checkbox" bind:checked={groupLazy} /> Lazy health-check</label><label class="inline-check"><input type="checkbox" bind:checked={groupDisableUDP} /> Отключить UDP</label><label><span>Таймаут, мс</span><input type="number" min="0" bind:value={groupTimeout} /></label><label><span>Ошибок до перепроверки</span><input type="number" min="0" bind:value={groupMaxFailedTimes} /></label>{#if groupType === 'url-test'}<label><span>Tolerance, мс</span><input type="number" min="0" bind:value={groupTolerance} /></label>{/if}<label><span>Ожидаемый HTTP-статус</span><input bind:value={groupExpectedStatus} placeholder="204 или 200/302" /></label><label><span>Фильтр имён (regex)</span><input bind:value={groupFilter} /></label><label><span>Исключить имена (regex)</span><input bind:value={groupExcludeFilter} /></label><label><span>Исключить типы</span><input bind:value={groupExcludeType} placeholder="Shadowsocks|Http" /></label></div>{/if}
				<div class="editor-actions"><Button variant="ghost" size="sm" onclick={() => groupForm = false}>Отмена</Button><Button variant="primary" size="sm" onclick={saveGroup} loading={saving}><Save size={14} />Сохранить и применить</Button></div>
			</div></Card>
		{/if}

		<div class="cards">
			{#each groups as group (group.id)}<article class="group-row"><div class="kind-icon"><Users size={17} /></div><div><strong>{group.name}</strong><span>{group.type} · {(group.proxies?.length ?? 0)} прокси · {(group.use?.length ?? 0)} providers</span></div><Badge variant="accent">Mihomo</Badge><button class="icon" onclick={() => editGroup(group)} aria-label="Изменить"><Pencil size={14} /></button><button class="icon danger" onclick={() => removeGroup(group)} aria-label="Удалить"><Trash2 size={14} /></button></article>{/each}
			{#each subscriptions.filter((sub) => sub.enabled && sub.groupName) as sub (sub.id)}
				{@const provider = runtimeProviders.find((item) => item.name === sub.providerName)}
				<article class="group-row provider-row"><div class="kind-icon"><Users size={17} /></div><div><strong>{sub.name}</strong><span>{sub.groupName} · provider · {provider?.proxies?.length ?? 0} узлов{provider?.updatedAt ? ` · ${new Date(provider.updatedAt).toLocaleString()}` : ''}</span>{#if sub.lastError}<em>{sub.lastError}</em>{/if}</div><Badge variant={provider ? 'success' : 'warning'}>{provider ? 'загружена' : 'ожидание'}</Badge></article>
			{/each}
			{#if !loading && groups.length === 0 && subscriptions.filter((sub) => sub.enabled && sub.groupName).length === 0}<div class="empty">Создайте группу или подписку Mihomo. Существующая маршрутизация пока остаётся безопасным fallback.</div>{/if}
		</div>
	{:else}
		<div class="section-tools"><div><strong>Правила</strong><span>Первое совпадение сверху побеждает. Если ничего не подошло, используется DIRECT.</span></div><div class="tool-buttons"><Button variant="secondary" size="sm" onclick={() => { resetProvider(); providerForm = !providerForm; }}><Plus size={15} />Rule provider</Button><Button variant="primary" size="sm" onclick={() => { resetRule(); ruleForm = !ruleForm; }}><Plus size={15} />Правило</Button></div></div>
		{#if unsupportedRules.length > 0}
			<div class="unsupported-banner">
				<div class="unsupported-banner-content">
					<AlertTriangle size={18} class="unsupported-icon" />
					<div>
						<strong>Обнаружены неподдерживаемые правила ({unsupportedRules.length})</strong>
						<p>В конфигурации сохранены правила типов, не поддерживаемых текущей версией ядра Mihomo.</p>
					</div>
				</div>
				<Button variant="danger" size="sm" onclick={() => unsupportedModalOpen = true}>
					Просмотреть и удалить
				</Button>
			</div>
		{/if}
		{#if providerForm}<Card padding="lg"><div class="editor"><div class="form-grid"><label><span>Название набора</span><input bind:value={providerName} placeholder="youtube" /></label><label><span>Источник</span><select bind:value={providerType}><option value="http">HTTP</option><option value="file">Локальный файл</option></select></label><label><span>Формат</span><select bind:value={providerFormat}><option value="yaml">YAML</option><option value="text">Text</option><option value="mrs">MRS</option></select></label></div>{#if providerType === 'http'}<label><span>URL</span><input bind:value={providerURL} placeholder="https://…/rules.mrs" /></label>{:else}<label><span>Путь к файлу</span><input bind:value={providerPath} placeholder="./rules/local.yaml" /></label>{/if}<div class="form-grid"><label><span>Поведение</span><select bind:value={providerBehavior}><option value="classical">Classical</option><option value="domain">Domains</option><option value="ipcidr">IP CIDR</option></select></label><label><span>Интервал, сек.</span><input type="number" min="0" bind:value={providerInterval} /></label></div><div class="editor-actions"><Button variant="ghost" size="sm" onclick={() => providerForm = false}>Отмена</Button><Button variant="primary" size="sm" onclick={saveProvider} loading={saving}>Сохранить provider</Button></div></div></Card>{/if}
		{#if ruleProviders.length > 0}<div class="provider-chips">{#each ruleProviders as provider (provider.id)}<span><strong>{provider.name}</strong> · {provider.behavior}/{provider.format}<button onclick={() => editProvider(provider)} aria-label="Изменить provider"><Pencil size={12} /></button><button onclick={() => removeProvider(provider)} aria-label="Удалить provider"><Trash2 size={12} /></button></span>{/each}</div>{/if}
		{#if ruleForm}<Card padding="lg"><div class="editor"><div class="form-grid rule-grid"><label><span>Условие</span><select bind:value={ruleType}>{#each ruleTypes as category}<optgroup label={category.label}>{#each category.items as item}<option value={item}>{item}</option>{/each}</optgroup>{/each}</select></label>{#if ruleType !== 'MATCH'}<label><span>Значение</span>{#if ruleType === 'RULE-SET' && ruleProviders.length > 0}<select bind:value={rulePayload}><option value="">Выберите provider</option>{#each ruleProviders as provider}<option value={provider.name}>{provider.name}</option>{/each}</select>{:else}<input bind:value={rulePayload} placeholder={ruleType === 'AND' || ruleType === 'OR' || ruleType === 'NOT' ? '((DOMAIN,example.com),(NETWORK,UDP))' : 'значение'} />{/if}</label>{/if}<label><span>Выход</span><select bind:value={ruleOutbound}>{#each outputOptions as output}<option value={output.value}>{output.label}</option>{/each}</select></label></div>{#if ['IP-CIDR','IP-CIDR6','IP-SUFFIX','IP-ASN','GEOIP'].includes(ruleType)}<label class="inline-check"><input type="checkbox" bind:checked={ruleNoResolve} /> Не выполнять DNS-resolve</label>{/if}<div class="editor-actions"><Button variant="ghost" size="sm" onclick={() => ruleForm = false}>Отмена</Button><Button variant="primary" size="sm" onclick={saveRule} loading={saving} disabled={ruleMutationInFlight}><Save size={14} />Сохранить и применить</Button></div></div></Card>{/if}
		<div class="rule-list">{#each rules as rule, index (rule.id)}<article class="rule-row"><span class="order">{index + 1}</span><div class="kind-icon"><Waypoints size={17} /></div><div><strong>{rule.type}{rule.payload ? `, ${rule.payload}` : ''}</strong><span>→ {displayName(rule.outbound)}{rule.noResolve ? ' · no-resolve' : ''}</span></div><div class="reorder"><button onclick={() => moveRule(index, -1)} disabled={ruleMutationInFlight || index === 0}><ChevronUp size={14} /></button><button onclick={() => moveRule(index, 1)} disabled={ruleMutationInFlight || index === rules.length - 1}><ChevronDown size={14} /></button></div><button class="icon" onclick={() => editRule(rule)} disabled={ruleMutationInFlight} aria-label="Изменить"><Pencil size={14} /></button><button class="icon danger" onclick={() => removeRule(rule)} disabled={ruleMutationInFlight} aria-label="Удалить"><Trash2 size={14} /></button></article>{/each}{#if !loading && rules.length === 0}<div class="empty">Нативных правил ещё нет. Текущий набор маршрутизации продолжает работать как fallback; новые правила Mihomo будут иметь более высокий приоритет.</div>{/if}</div>
	{/if}
</section>

<Modal open={unsupportedModalOpen} title="Неподдерживаемые правила Mihomo" size="md" onclose={() => { if (!deletingUnsupported) unsupportedModalOpen = false; }}>
	<div class="unsupported-modal">
		<p class="modal-desc">
			Следующие правила имеют типы, не поддерживаемые ядром Mihomo. Вы можете удалить весь набор неподдерживаемых правил разом.
		</p>
		{#if unsupportedRevision}
			<div class="revision-token">
				<span>Ревизия снимка:</span>
				<code>{unsupportedRevision}</code>
			</div>
		{/if}
		<div class="unsupported-list">
			{#each unsupportedRules as rule (rule.id)}
				<div class="unsupported-item">
					<div class="unsupported-item-header">
						<Badge variant="warning">{rule.type}</Badge>
						<code class="rule-id">{rule.id}</code>
					</div>
					<div class="unsupported-item-details">
						<span class="payload">{rule.payload || '—'}</span>
						<span class="arrow">→</span>
						<span class="outbound">{displayName(rule.outbound)}</span>
					</div>
				</div>
			{/each}
		</div>
		<div class="modal-actions">
			<Button variant="ghost" size="sm" onclick={() => unsupportedModalOpen = false} disabled={deletingUnsupported}>
				Отмена
			</Button>
			<Button variant="danger" size="sm" onclick={deleteUnsupportedRules} loading={deletingUnsupported} disabled={deletingUnsupported || unsupportedRules.length === 0}>
				<Trash2 size={14} />
				Удалить неподдерживаемые правила ({unsupportedRules.length})
			</Button>
		</div>
	</div>
</Modal>

<style>
	.policy-panel{display:grid;gap:12px;padding-top:4px}.panel-head,.section-tools{display:flex;align-items:flex-start;justify-content:space-between;gap:12px}.panel-head h3,.panel-head p{margin:0}.panel-head p,.section-tools span{display:block;margin-top:3px;color:var(--text-muted);font-size:12px}.head-actions,.tool-buttons{display:flex;align-items:center;gap:7px;flex-wrap:wrap}.mutation-badge{display:flex;align-items:center;gap:5px;font-size:11px;color:var(--accent);font-weight:500;padding:4px 8px;border-radius:6px;background:var(--accent-soft)}.icon{display:grid;place-items:center;width:30px;height:30px;border:0;border-radius:7px;background:transparent;color:var(--text-muted);cursor:pointer}.icon:hover{background:var(--bg-tertiary);color:var(--text-primary)}.icon.danger:hover{color:var(--danger)}.editor{display:grid;gap:12px}.form-grid,.advanced-grid{display:grid;grid-template-columns:2fr 1fr 1fr;gap:10px}.rule-grid{grid-template-columns:1fr 2fr 1fr}.advanced-grid{grid-template-columns:repeat(4,minmax(0,1fr));padding:12px;border:1px solid var(--border);border-radius:8px;background:var(--bg-primary)}.editor label{display:grid;gap:5px;color:var(--text-secondary);font-size:12px}input,select{width:100%;box-sizing:border-box;padding:8px 10px;border:1px solid var(--border);border-radius:7px;background:var(--bg-primary);color:var(--text-primary);font:inherit}.field-title{display:block;margin-bottom:6px;color:var(--text-secondary);font-size:12px}.checks{display:flex;flex-wrap:wrap;gap:6px;max-height:190px;overflow:auto}.checks button{max-width:240px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;padding:7px 10px;border:1px solid var(--border);border-radius:7px;background:var(--bg-primary);color:var(--text-secondary);cursor:pointer}.checks button.selected{border-color:var(--accent);background:var(--accent-soft);color:var(--accent)}.inline-check{display:flex!important;grid-template-columns:auto 1fr!important;align-items:center}.inline-check input{width:auto}.advanced-toggle{justify-self:start;border:0;background:transparent;color:var(--accent);font-size:12px;cursor:pointer}.editor-actions{display:flex;justify-content:flex-end;gap:7px}.cards,.rule-list{display:grid;gap:8px}.group-row,.rule-row{display:grid;grid-template-columns:auto minmax(0,1fr) auto auto auto;align-items:center;gap:10px;padding:11px;border:1px solid var(--border);border-radius:9px;background:var(--bg-secondary)}.provider-row{grid-template-columns:auto minmax(0,1fr) auto}.rule-row{grid-template-columns:auto auto minmax(0,1fr) auto auto auto}.group-row>div:nth-child(2),.rule-row>div:nth-child(3){display:grid;min-width:0}.group-row strong,.rule-row strong{overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-size:13px}.group-row span,.rule-row span{color:var(--text-muted);font-size:11px}.provider-row em{margin-top:3px;color:var(--danger);font-size:11px}.kind-icon{display:grid;place-items:center;width:34px;height:34px;border-radius:8px;background:var(--accent-soft);color:var(--accent)}.order{width:22px;text-align:center;color:var(--text-muted);font:11px var(--font-mono)}.reorder{display:grid}.reorder button{display:grid;place-items:center;width:24px;height:18px;border:0;background:transparent;color:var(--text-muted);cursor:pointer}.provider-chips{display:flex;flex-wrap:wrap;gap:7px}.provider-chips>span{display:flex;align-items:center;gap:5px;padding:7px 9px;border:1px solid var(--border);border-radius:7px;background:var(--bg-secondary);color:var(--text-muted);font-size:11px}.provider-chips button{display:grid;place-items:center;border:0;background:transparent;color:var(--text-muted);cursor:pointer}.empty{padding:24px;text-align:center;border:1px dashed var(--border);border-radius:9px;color:var(--text-muted);font-size:12px}.spin{animation:spin .8s linear infinite}@keyframes spin{to{transform:rotate(360deg)}}@media(max-width:900px){.advanced-grid{grid-template-columns:1fr 1fr}}@media(max-width:760px){.panel-head,.section-tools{flex-direction:column}.head-actions,.head-actions :global(.segmented-control){width:100%}.form-grid,.rule-grid,.advanced-grid{grid-template-columns:1fr}.group-row{grid-template-columns:auto minmax(0,1fr) auto auto}.group-row :global(.badge){display:none}.provider-row{grid-template-columns:auto 1fr}.rule-row{grid-template-columns:minmax(0,1fr) auto auto}.rule-row>.kind-icon,.rule-row>.order,.rule-row>.reorder{display:none}}
	.unsupported-banner{display:flex;align-items:center;justify-content:space-between;gap:12px;padding:12px 14px;border:1px solid rgba(245,158,11,0.3);border-radius:8px;background:rgba(245,158,11,0.08)}
	.unsupported-banner-content{display:flex;align-items:center;gap:10px}
	.unsupported-banner-content strong{display:block;font-size:13px;color:var(--text-primary)}
	.unsupported-banner-content p{margin:2px 0 0;font-size:11px;color:var(--text-muted)}
	:global(.unsupported-icon){color:#f59e0b;flex-shrink:0}
	.unsupported-modal{display:grid;gap:14px}
	.modal-desc{margin:0;font-size:13px;color:var(--text-secondary);line-height:1.4}
	.revision-token{display:flex;align-items:center;gap:8px;padding:6px 10px;border-radius:6px;background:var(--bg-tertiary);font-size:11px;color:var(--text-muted)}
	.revision-token code{font-family:var(--font-mono);color:var(--text-primary)}
	.unsupported-list{display:grid;gap:8px;max-height:240px;overflow-y:auto}
	.unsupported-item{display:flex;align-items:center;justify-content:space-between;padding:8px 10px;border:1px solid var(--border);border-radius:7px;background:var(--bg-secondary);font-size:12px}
	.unsupported-item-header{display:flex;align-items:center;gap:8px}
	.rule-id{font-family:var(--font-mono);font-size:11px;color:var(--text-muted)}
	.unsupported-item-details{display:flex;align-items:center;gap:6px;color:var(--text-secondary)}
	.unsupported-item-details .outbound{font-weight:500;color:var(--text-primary)}
	.modal-actions{display:flex;justify-content:flex-end;gap:8px;padding-top:6px}
</style>
