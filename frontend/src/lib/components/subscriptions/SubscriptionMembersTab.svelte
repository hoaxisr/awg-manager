<script lang="ts">
	import { untrack } from 'svelte';
	import { goto } from '$app/navigation';
	import type { Subscription, SubscriptionMember } from '$lib/types';
	import { Ban, CheckLine, PanelBottomClose, RefreshCcw } from 'lucide-svelte';
	import { api } from '$lib/api/client';
	import { MAX_SUBSCRIPTION_INFO_ITEMS } from '$lib/constants/subscription';
	import { Button, Modal, Stat, StatStrip } from '$lib/components/ui';
	import { runWithConcurrency } from '$lib/utils/runWithConcurrency';
	import { singboxDelayHistory, triggerDelayCheck } from '$lib/stores/singbox';
	import { notifications } from '$lib/stores/notifications';
	import SubscriptionMemberList from './SubscriptionMemberList.svelte';
	import SubscriptionExcludedSection from './SubscriptionExcludedSection.svelte';
	import type { SingboxLayoutMode } from '$lib/constants/singboxLayout';
	import CreateIcon from '$lib/components/ui/icons/CreateIcon.svelte';

	interface Props {
		subscription: Subscription;
		onUpdated: () => void;
		autoDelayCheckNonce?: number;
		liveActiveMember?: string | null;
		layout?: SingboxLayoutMode;
	}
	let { subscription, onUpdated, autoDelayCheckNonce = 0, liveActiveMember = null, layout = 'compact' }: Props = $props();

	let refreshing = $state(false);
	let switching = $state<string | null>(null);
	let lastError = $state('');
	let batchTesting = $state(false);
	let batchProgress = $state({ done: 0, total: 0 });
	let lastAutoDelayCheckNonce = 0;
	let confirmClearOrphans = $state(false);
	let clearingOrphans = $state(false);
	let addOpen = $state(false);
	let addLink = $state('');
	let adding = $state(false);
	let addError = $state('');
	let removingTag = $state<string | null>(null);
	let pendingRemove = $state<SubscriptionMember | null>(null);
	let pendingExclude = $state<SubscriptionMember | null>(null);
	let movingToInfo = $state<string | null>(null);
	let removingInfoId = $state<string | null>(null);
	let selectMode = $state(false);
	let selected = $state<Set<string>>(new Set());
	let excluding = $state(false);
	let confirmExcludeSelected = $state(false);
	let restoring = $state(false);

	const infoItems = $derived(subscription.infoItems ?? []);
	const rejectedMembers = $derived(subscription.rejectedMembers ?? []);
	const isUrlSub = $derived(!subscription.isInline);

	async function removeInfoItem(itemId: string): Promise<void> {
		if (!itemId || removingInfoId) return;
		removingInfoId = itemId;
		lastError = '';
		try {
			await api.removeSubscriptionInfoItem(subscription.id, itemId);
			onUpdated();
		} catch (e) {
			lastError = e instanceof Error ? e.message : 'Не удалось убрать строку из info';
		} finally {
			removingInfoId = null;
		}
	}

	async function moveRejectedToInfo(memberTag: string): Promise<void> {
		if (!memberTag || movingToInfo) return;
		movingToInfo = memberTag;
		lastError = '';
		try {
			await api.moveSubscriptionRejectedToInfo(subscription.id, memberTag);
			onUpdated();
		} catch (e) {
			lastError = e instanceof Error ? e.message : 'Не удалось перенести в info';
		} finally {
			movingToInfo = null;
		}
	}

	async function addMember(): Promise<void> {
		const link = addLink.trim();
		if (!link || adding) return;
		adding = true;
		addError = '';
		try {
			await api.addSubscriptionMember(subscription.id, link);
			addLink = '';
			addOpen = false;
			onUpdated();
		} catch (e) {
			addError = e instanceof Error ? e.message : 'Не удалось добавить сервер';
		} finally {
			adding = false;
		}
	}

	function requestRemove(member: SubscriptionMember): void {
		pendingRemove = member;
	}

	async function confirmRemove(): Promise<void> {
		if (!pendingRemove || removingTag) return;
		const tag = pendingRemove.tag;
		removingTag = tag;
		lastError = '';
		try {
			const updated = await api.removeSubscriptionMember(subscription.id, tag);
			pendingRemove = null;
			if (updated === null) {
				goto('/?tab=subscriptions');
				return;
			}
			onUpdated();
		} catch (e) {
			lastError = e instanceof Error ? e.message : 'Не удалось удалить сервер';
		} finally {
			removingTag = null;
		}
	}

	// Derive member list from members[] when available; fall back to stubs
	// built from memberTags[] for subscriptions persisted before this change.
	const memberList = $derived<SubscriptionMember[]>(
		subscription.members && subscription.members.length > 0
			? subscription.members
			: subscription.memberTags.map((tag) => ({
					tag,
					protocol: '?',
					server: tag,
					port: 0,
			  })),
	);

	const membersListStats = $derived.by(() => {
		let delaySum = 0;
		let delayN = 0;
		let minLatest = Infinity;
		let bestDelayServer = '—';
		let bestDelayProtocol = '';
		const histMap = $singboxDelayHistory;
		for (const m of memberList) {
			const h = histMap.get(m.tag) ?? [];
			const last = h.length > 0 ? h[h.length - 1] : 0;
			if (typeof last === 'number' && last > 0) {
				delaySum += last;
				delayN++;
				if (last < minLatest) {
					minLatest = last;
					bestDelayServer = m.label || m.server || m.tag;
					bestDelayProtocol = m.protocol || '';
				}
			}
		}
		return {
			count: memberList.length,
			avgDelayMs: delayN > 0 ? Math.round(delaySum / delayN) : null,
			minDelayMs: minLatest === Infinity ? null : Math.round(minLatest),
			bestDelayServer,
			bestDelayProtocol,
		};
	});

	const modeLabel = $derived(subscription.mode === 'urltest' ? 'URLTest' : 'Selector');
	const modeHint = $derived(
		subscription.mode === 'urltest'
			? 'Sing-box автоматически выбирает быстрейший сервер по latency-тесту.'
			: 'Выберите активный сервер. Selector направит трафик в выбранный outbound.',
	);

	// For urltest mode, liveActiveMember reflects the auto-selected member as reported
	// by the running Clash API (polled every 5s by the parent page). For selector mode
	// this is always null, so we fall back to the persisted activeMember.
	const effectiveActiveMember = $derived(liveActiveMember || subscription.activeMember);

	async function refresh(): Promise<void> {
		refreshing = true;
		lastError = '';
		const beforeInfo = infoItems.length;
		const beforeRejected = rejectedMembers.length;
		try {
			const result = await api.refreshSubscription(subscription.id);
			const skipped: string[] = [];
			if (result.skippedDuplicate > 0) skipped.push(`дубликатов: ${result.skippedDuplicate}`);
			if (result.skippedVmess > 0) skipped.push(`vmess: ${result.skippedVmess}`);
			if (result.skippedOther > 0) skipped.push(`не поддерживаемых: ${result.skippedOther}`);
			if (skipped.length > 0) {
				notifications.warning(`Пропущено — ${skipped.join(', ')}`);
			}
			const updated = await api.getSubscription(subscription.id);
			const infoN = updated.infoItems?.length ?? 0;
			const rejN = updated.rejectedMembers?.length ?? 0;
			const extra: string[] = [];
			if (infoN > beforeInfo) extra.push(`+${infoN - beforeInfo} info`);
			if (rejN > beforeRejected) extra.push(`+${rejN - beforeRejected} отклонённых`);
			if (extra.length > 0) {
				notifications.info(`После обновления: ${extra.join(', ')}`);
			}
			onUpdated();
		} catch (e) {
			lastError = e instanceof Error ? e.message : 'Не удалось обновить';
		} finally {
			refreshing = false;
		}
	}

	async function pickActive(memberTag: string): Promise<void> {
		// Urltest auto-selects fastest member; manual pick is rejected by backend
		// with 409. Tell the user how to switch to selector mode.
		if (subscription.mode === 'urltest') {
			notifications.info(
				'Включён автовыбор (URLTest). Чтобы переключать сервер вручную, откройте вкладку «Настройки» этой подписки и выберите режим «Вручную».',
				{ duration: 9000 },
			);
			return;
		}
		if (memberTag === subscription.activeMember) return;
		switching = memberTag;
		lastError = '';
		try {
			await api.setSubscriptionActiveMember(subscription.id, memberTag);
			onUpdated();
		} catch (e) {
			lastError = e instanceof Error ? e.message : 'Не удалось переключить';
		} finally {
			switching = null;
		}
	}

	async function testAll(): Promise<void> {
		if (batchTesting) return;
		const tags = memberList.map((m) => m.tag);
		if (tags.length === 0) return;
		batchTesting = true;
		batchProgress = { done: 0, total: tags.length };
		try {
			let done = 0;
			await runWithConcurrency(tags, 4, async (tag) => {
				await triggerDelayCheck(tag);
				done += 1;
				batchProgress = { done, total: tags.length };
			});
		} finally {
			batchTesting = false;
		}
	}

	async function clearOrphans(): Promise<void> {
		if (clearingOrphans || subscription.orphanTags.length === 0) return;
		clearingOrphans = true;
		lastError = '';
		try {
			await api.deleteSubscriptionOrphans(subscription.id);
			confirmClearOrphans = false;
			onUpdated();
		} catch (e) {
			lastError = e instanceof Error ? e.message : 'Не удалось очистить сироты';
		} finally {
			clearingOrphans = false;
		}
	}

	function toggleSelectMode(): void {
		selectMode = !selectMode;
		if (!selectMode) {
			selected = new Set();
			confirmExcludeSelected = false;
		}
	}

	function toggleSel(tag: string): void {
		const next = new Set(selected);
		if (next.has(tag)) next.delete(tag);
		else next.add(tag);
		selected = next;
	}

	function selectAll(): void {
		selected = new Set(memberList.map((m) => m.tag));
	}

	function excludeOne(tag: string): void {
		const member = memberList.find((m) => m.tag === tag);
		if (member) pendingExclude = member;
	}

	async function confirmExcludeOne(): Promise<void> {
		if (!pendingExclude || excluding) return;
		excluding = true;
		lastError = '';
		try {
			await api.excludeSubscriptionMembers(subscription.id, [pendingExclude.tag]);
			pendingExclude = null;
			onUpdated();
		} catch (e) {
			lastError = e instanceof Error ? e.message : 'Не удалось исключить';
		} finally {
			excluding = false;
		}
	}

	async function excludeSelected(): Promise<void> {
		if (excluding || selected.size === 0) return;
		excluding = true;
		lastError = '';
		try {
			await api.excludeSubscriptionMembers(subscription.id, [...selected]);
			selected = new Set();
			selectMode = false;
			confirmExcludeSelected = false;
			onUpdated();
		} catch (e) {
			lastError = e instanceof Error ? e.message : 'Не удалось исключить';
		} finally {
			excluding = false;
		}
	}

	async function restore(tags: string[]): Promise<void> {
		if (restoring || tags.length === 0) return;
		restoring = true;
		lastError = '';
		try {
			await api.restoreSubscriptionMembers(subscription.id, tags);
			onUpdated();
		} catch (e) {
			lastError = e instanceof Error ? e.message : 'Не удалось вернуть';
		} finally {
			restoring = false;
		}
	}

	$effect(() => {
		const nonce = autoDelayCheckNonce;
		const hasMembers = memberList.length > 0;

		if (nonce <= 0 || nonce === lastAutoDelayCheckNonce) return;
		lastAutoDelayCheckNonce = nonce;
		if (!hasMembers || batchTesting) return;

		untrack(() => {
			void testAll();
		});
	});
</script>

{#snippet createIcon()}
	<CreateIcon />
{/snippet}

{#snippet refreshIcon()}
	<RefreshCcw size={14} strokeWidth={2} aria-hidden="true" />
{/snippet}

{#snippet testAllIcon()}
	<CheckLine size={14} strokeWidth={2} aria-hidden="true" />
{/snippet}

{#snippet banIcon()}
	<Ban size={14} strokeWidth={2} aria-hidden="true" />
{/snippet}

{#if selectMode}
	<header class="head select-bar">
		<div class="select-info">Выбрано {selected.size} из {memberList.length}</div>
		<div class="actions">
			<Button
				variant="ghost"
				size="sm"
				disabled={excluding || memberList.length === 0}
				onclick={selectAll}
			>
				Выбрать все
			</Button>
			{#if confirmExcludeSelected}
				<Button
					variant="danger"
					size="sm"
					disabled={excluding || selected.size === 0}
					loading={excluding}
					iconBefore={banIcon}
					onclick={excludeSelected}
				>
					{excluding ? 'Исключаем...' : `Подтвердить (${selected.size})`}
				</Button>
				<Button variant="ghost" size="sm" disabled={excluding} onclick={() => (confirmExcludeSelected = false)}>
					Назад
				</Button>
			{:else}
				<Button
					variant="danger"
					size="sm"
					disabled={excluding || selected.size === 0}
					iconBefore={banIcon}
					onclick={() => (confirmExcludeSelected = true)}
				>
					Исключить выбранные ({selected.size})
				</Button>
				<Button variant="ghost" size="sm" disabled={excluding} onclick={toggleSelectMode}>
					Отмена
				</Button>
			{/if}
		</div>
	</header>
{:else}
	<header class="head">
		<div class="head-info">
			<div class="lbl">{modeLabel}</div>
			<div class="val mono">{subscription.selectorTag}</div>
		</div>
		<div class="actions">
			{#if subscription.isInline}
				<Button variant="primary" size="sm" onclick={() => (addOpen = true)} iconBefore={createIcon}>
					Добавить сервер
				</Button>
			{:else}
				<Button
					variant="primary"
					size="sm"
					disabled={refreshing}
					loading={refreshing}
					iconBefore={refreshIcon}
					onclick={refresh}
				>
					{refreshing ? 'Обновляем...' : 'Обновить сейчас'}
				</Button>
			{/if}
			<Button
				variant="ghost"
				size="sm"
				disabled={batchTesting || memberList.length === 0}
				loading={batchTesting}
				iconBefore={testAllIcon}
				onclick={testAll}
			>
				{#if batchTesting}
					Тестируем {batchProgress.done}/{batchProgress.total}
				{:else}
					Проверить всё
				{/if}
			</Button>
			{#if isUrlSub && memberList.length > 0}
				<Button variant="ghost" size="sm" disabled={excluding} onclick={toggleSelectMode}>
					Выбрать
				</Button>
			{/if}
		</div>
	</header>
{/if}

{#if lastError}
	<div class="err">{lastError}</div>
{/if}

{#if infoItems.length > 0}
	<section class="info-block">
		<div class="lbl">Информация от провайдера ({infoItems.length}/{MAX_SUBSCRIPTION_INFO_ITEMS})</div>
		<ul class="info-list">
			{#each infoItems as item (item.id)}
				<li class="info-card">
					<span class="info-text">{item.label}</span>
					<div class="info-card-actions">
						<button
							type="button"
							class="info-remove-btn"
							title="Убрать в отклонённые"
							aria-label="Убрать в отклонённые: {item.label}"
							disabled={removingInfoId !== null}
							onclick={() => removeInfoItem(item.id)}
						>
							<PanelBottomClose size={14} aria-hidden="true" />
						</button>
					</div>
				</li>
			{/each}
		</ul>
	</section>
{/if}

{#if memberList.length === 0}
	<div class="empty">Подписка ещё не загружена. Нажмите «Обновить сейчас».</div>
{:else}
	<div class="hint">{modeHint}</div>
	{#if layout === 'list'}
		<div class="awg-summary-row">
			<StatStrip>
				<Stat value={`${membersListStats.count}`} label="Серверов" sub="в подписке" />
				<Stat
					value={membersListStats.avgDelayMs !== null ? `${membersListStats.avgDelayMs} ms` : '—'}
					label="Средний delay"
					sub="по последним проверкам"
				/>
				<Stat
					value={membersListStats.minDelayMs !== null ? `${membersListStats.minDelayMs} ms` : '—'}
					label="Мин. delay"
					sub="лучший из последних по серверам"
				/>
				<Stat
					value={membersListStats.minDelayMs !== null ? membersListStats.bestDelayServer : '—'}
					label="Лидер по delay"
					sub={membersListStats.minDelayMs !== null
						? `${membersListStats.minDelayMs} ms${membersListStats.bestDelayProtocol ? ` · ${membersListStats.bestDelayProtocol}` : ''}`
						: 'нет замеров'}
				/>
			</StatStrip>
		</div>
		{/if}
		<SubscriptionMemberList
			members={memberList}
			{effectiveActiveMember}
			{switching}
			{layout}
			isInline={subscription.isInline}
			{removingTag}
			minDelayMs={membersListStats.minDelayMs}
			{isUrlSub}
			{selectMode}
			{selected}
			{excluding}
			onpick={pickActive}
			onremove={requestRemove}
			ontoggle={toggleSel}
			onexclude={excludeOne}
		/>
	{/if}

<Modal
	open={addOpen}
	title="Добавить сервер"
	size="md"
	onclose={() => {
		if (adding) return;
		addOpen = false;
		addLink = '';
		addError = '';
	}}
>
	<form
		class="add-form"
		onsubmit={(e) => {
			e.preventDefault();
			void addMember();
		}}
	>
		<label class="add-row">
			<span class="add-lbl">Share-link сервера</span>
			<input
				class="add-inp"
				type="text"
				bind:value={addLink}
				placeholder="vless://... or trojan://... or hysteria2://... or mieru://..."
				autocomplete="off"
				required
			/>
		</label>
		{#if addError}<div class="err">{addError}</div>{/if}
	</form>
	{#snippet actions()}
		<Button
			variant="ghost"
			disabled={adding}
			onclick={() => {
				addOpen = false;
				addLink = '';
				addError = '';
			}}
		>
			Отмена
		</Button>
		<Button variant="primary" disabled={adding || !addLink.trim()} loading={adding} onclick={addMember}>
			{adding ? 'Добавляем...' : 'Добавить'}
		</Button>
	{/snippet}
</Modal>

<Modal
	open={pendingRemove !== null}
	title="Удалить сервер?"
	size="md"
	onclose={() => {
		if (removingTag) return;
		pendingRemove = null;
	}}
>
	{#if pendingRemove}
		<p>
			Сервер
			<strong>{pendingRemove.label || `${pendingRemove.server}:${pendingRemove.port}`}</strong>
			будет удалён из подписки.
		</p>
		{#if memberList.length === 1}
			<p class="warn">
				Это последний сервер в подписке. После удаления подписка
				целиком будет удалена вместе с её Proxy NDMS и
				selector / urltest outbound'ом.
			</p>
		{/if}
	{/if}
	{#snippet actions()}
		<Button
			variant="ghost"
			disabled={removingTag !== null}
			onclick={() => (pendingRemove = null)}
		>
			Отмена
		</Button>
		<Button
			variant="danger"
			disabled={removingTag !== null}
			loading={removingTag !== null}
			onclick={confirmRemove}
		>
			{removingTag !== null ? 'Удаляем...' : 'Удалить'}
		</Button>
	{/snippet}
</Modal>

<Modal
	open={pendingExclude !== null}
	title="Исключить сервер?"
	size="md"
	onclose={() => {
		if (excluding) return;
		pendingExclude = null;
	}}
>
	{#if pendingExclude}
		<p>
			Сервер
			<strong>{pendingExclude.label || `${pendingExclude.server}:${pendingExclude.port}`}</strong>
			будет исключён из подписки и перестанет участвовать в выборе. Сервер
			останется исключённым при обновлении подписки; вернуть его можно в
			разделе «Исключённые».
		</p>
	{/if}
	{#snippet actions()}
		<Button variant="ghost" disabled={excluding} onclick={() => (pendingExclude = null)}>
			Отмена
		</Button>
		<Button
			variant="danger"
			disabled={excluding}
			loading={excluding}
			iconBefore={banIcon}
			onclick={confirmExcludeOne}
		>
			{excluding ? 'Исключаем...' : 'Исключить'}
		</Button>
	{/snippet}
</Modal>

{#if rejectedMembers.length > 0}
	<section class="rejected">
		<div class="rejected-head">
			<div>
				<div class="lbl warn">Отклонённые ({rejectedMembers.length})</div>
				<div class="hint">
					Не попали в sing-box (некорректный UUID, info-строки сверх лимита и т.д.). Не участвуют в выборе сервера.
				</div>
			</div>
		</div>
		<div class="rejected-list">
			{#each rejectedMembers as row, idx (`rej:${idx}:${row.tag ?? ''}:${row.reason}:${row.label ?? ''}`)}
				<div class="rejected-card">
					<div class="rejected-main">
						<div class="rejected-title">{row.label || row.tag || '—'}</div>
						<div class="rejected-meta mono">
							{#if row.protocol}{row.protocol}{/if}
							{#if row.protocol && row.server}
								{' '}
							{/if}
							{#if row.server}
								{row.server}{#if row.port}:{row.port}{/if}
							{/if}
							{#if row.protocol || row.server}
								·
							{/if}
							{row.reason}
						</div>
					</div>
					{#if row.tag}
						<Button
							variant="ghost"
							size="sm"
							disabled={movingToInfo !== null || infoItems.length >= MAX_SUBSCRIPTION_INFO_ITEMS}
							loading={movingToInfo === row.tag}
							onclick={() => moveRejectedToInfo(row.tag!)}
						>
							Перенести в info
						</Button>
					{/if}
				</div>
			{/each}
		</div>
	</section>
{/if}

{#if subscription.orphanTags.length > 0}
	<section class="orphans">
		<div class="orphans-head">
			<div>
				<div class="lbl warn">Сироты ({subscription.orphanTags.length})</div>
				<div class="hint">
					Эти серверы были в прошлой версии подписки, но не вернулись при последнем обновлении.
					Они не участвуют в выборе, но остаются в конфиге sing-box до очистки.
				</div>
			</div>
			<div class="orphan-actions">
				{#if confirmClearOrphans}
					<Button
						variant="danger"
						size="sm"
						disabled={clearingOrphans}
						loading={clearingOrphans}
						onclick={clearOrphans}
					>
						{clearingOrphans ? 'Очищаем...' : 'Удалить'}
					</Button>
					<Button
						variant="ghost"
						size="sm"
						disabled={clearingOrphans}
						onclick={() => (confirmClearOrphans = false)}
					>
						Отмена
					</Button>
				{:else}
					<Button variant="ghost" size="sm" onclick={() => (confirmClearOrphans = true)}>
						Очистить сироты
					</Button>
				{/if}
			</div>
		</div>
		<div class="grid">
			{#each subscription.orphanTags as tag (tag)}
				<div class="orphan-card mono">{tag}</div>
			{/each}
		</div>
	</section>
{/if}

<SubscriptionExcludedSection
	members={subscription.excludedMembers ?? []}
	{restoring}
	onrestore={restore}
/>

<style>
	.head {
		display: flex;
		justify-content: space-between;
		align-items: center;
		gap: 1rem;
		margin-bottom: 1rem;
	}
	.head-info { display: flex; flex-direction: column; gap: 0.2rem; }
	.actions { display: flex; gap: 0.5rem; align-items: center; }
	.select-bar {
		padding: 0.5rem 0.75rem;
		border: 1px solid var(--color-accent-border);
		border-radius: 10px;
		background: var(--color-accent-tint);
	}
	.select-info {
		font-size: 0.85rem;
		font-weight: 600;
		color: var(--color-accent);
	}
	@media (max-width: 640px) {
		.head {
			display: grid;
			grid-template-columns: minmax(0, 1fr);
			align-items: stretch;
			gap: 0.55rem;
		}

		.head-info {
			flex-direction: row;
			align-items: baseline;
			gap: 0.35rem;
			width: 100%;
			min-width: 0;
		}

		.head-info .lbl {
			flex: 0 0 auto;
			white-space: nowrap;
		}

		.head-info .lbl::after {
			content: ':';
		}

		.head-info .val {
			flex: 1 1 auto;
			min-width: 0;
			overflow: hidden;
			text-overflow: ellipsis;
			white-space: nowrap;
		}

		.actions {
			display: grid;
			grid-template-columns: repeat(2, minmax(0, 1fr));
			align-items: stretch;
			gap: 0.5rem;
			width: 100%;
		}

		/* Select-mode bar: stack buttons in one column so the long
		   "Исключить выбранные (N)" label never overflows on narrow screens. */
		.select-bar .actions {
			grid-template-columns: minmax(0, 1fr);
		}

		.actions :global(.btn) {
			width: 100%;
			min-width: 0;
			justify-content: center;
			border: 1px solid var(--color-border);
		}
	}
	.lbl {
		font-size: 0.7rem;
		color: var(--color-text-muted);
		text-transform: uppercase;
		letter-spacing: 0.5px;
	}
	.lbl.warn { color: #d29922; }
	.val { color: var(--color-text-primary); font-size: 0.85rem; }
	.err { color: #f85149; font-size: 0.85rem; margin-bottom: 0.6rem; }
	.hint { color: var(--color-text-muted); font-size: 0.82rem; margin-bottom: 0.8rem; }
	.empty {
		padding: 2rem;
		text-align: center;
		color: var(--color-text-muted);
		border: 1px dashed var(--color-border);
		border-radius: 6px;
	}
	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(min(100%, 280px), 1fr));
		gap: 0.8rem;
		justify-items: stretch;
		align-items: stretch;
	}
	.info-block {
		margin-bottom: 1rem;
		padding: 0.75rem 1rem;
		border: 1px solid var(--color-border);
		border-radius: 10px;
		background: var(--color-bg-secondary);
	}
	.info-list {
		list-style: none;
		margin: 0.5rem 0 0;
		padding: 0;
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
	}
	.info-card {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.5rem;
		padding: 0.5rem 0.65rem;
		border-radius: 8px;
		background: var(--color-bg-primary);
	}
	.info-text {
		font-size: 0.9rem;
		color: var(--color-text-primary);
	}
	.info-card-actions {
		display: flex;
		align-items: center;
		gap: 0.35rem;
		flex-shrink: 0;
	}
	.info-remove-btn {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: 28px;
		height: 28px;
		padding: 0;
		border: none;
		border-radius: var(--radius-sm);
		background: transparent;
		color: var(--color-text-muted);
		cursor: pointer;
	}
	.info-remove-btn:hover:not(:disabled) {
		color: var(--color-danger, #f85149);
		background: color-mix(in srgb, var(--color-danger, #f85149) 12%, transparent);
	}
	.info-remove-btn:disabled {
		opacity: 0.45;
		cursor: not-allowed;
	}
	.info-remove-btn:focus-visible {
		outline: 2px solid var(--color-accent);
		outline-offset: 2px;
	}
	.rejected {
		margin-top: 1.5rem;
		padding-top: 1rem;
		border-top: 1px solid var(--color-border);
	}
	.rejected-head {
		margin-bottom: 0.8rem;
	}
	.rejected-list {
		display: flex;
		flex-direction: column;
		gap: 0.6rem;
	}
	.rejected-card {
		display: flex;
		align-items: flex-start;
		justify-content: space-between;
		gap: 0.75rem;
		padding: 12px 14px;
		border: 1px dashed var(--color-border);
		border-radius: 10px;
	}
	.rejected-title {
		font-size: 0.88rem;
		color: var(--color-text-primary);
	}
	.rejected-meta {
		font-size: 0.75rem;
		color: var(--color-text-muted);
		margin-top: 0.25rem;
	}
	.orphans {
		margin-top: 1.5rem;
		padding-top: 1rem;
		border-top: 1px solid var(--color-border);
	}
	.orphans-head {
		display: flex;
		justify-content: space-between;
		align-items: flex-start;
		gap: 1rem;
		margin-bottom: 0.8rem;
	}
	.orphan-actions {
		display: flex;
		gap: 0.5rem;
		flex-shrink: 0;
	}
	.orphan-card {
		padding: 14px 16px;
		border: 1px dashed var(--color-border);
		border-radius: 10px;
		font-size: 0.8rem;
		color: var(--color-text-muted);
	}
	.mono { font-family: var(--font-mono, ui-monospace, monospace); }
	@media (max-width: 720px) {
		.orphans-head {
			flex-direction: column;
		}
		.orphan-actions {
			width: 100%;
			flex-wrap: wrap;
		}
	}

	.add-form { display: flex; flex-direction: column; gap: 0.5rem; }
	.add-row { display: flex; flex-direction: column; gap: 0.3rem; }
	.add-lbl { font-size: 0.85rem; color: var(--color-text-muted); }
	.add-inp {
		padding: 0.5rem 0.7rem;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: 4px;
		color: var(--color-text-primary);
		font-family: var(--font-mono, ui-monospace, monospace);
		font-size: 0.82rem;
	}
	.warn { color: #d29922; font-size: 0.85rem; }

	@media (max-width: 900px) {
		.grid {
			grid-template-columns: repeat(auto-fit, minmax(min(100%, 250px), 1fr));
		}
	}

	@media (max-width: 640px) {
		.grid {
			grid-template-columns: 1fr;
		}
	}

	.awg-summary-row {
		margin-bottom: 0.75rem;
	}
</style>
