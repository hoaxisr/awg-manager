<script lang="ts">
	import { untrack } from 'svelte';
	import { goto } from '$app/navigation';
	import type { Subscription, SubscriptionMember } from '$lib/types';
	import { api } from '$lib/api/client';
	import { Button, Modal } from '$lib/components/ui';
	import { triggerDelayCheck } from '$lib/stores/singbox';
	import { notifications } from '$lib/stores/notifications';
	import SubscriptionMemberCard from './SubscriptionMemberCard.svelte';

	interface Props {
		subscription: Subscription;
		onUpdated: () => void;
		autoDelayCheckNonce?: number;
		liveActiveMember?: string | null;
	}
	let { subscription, onUpdated, autoDelayCheckNonce = 0, liveActiveMember = null }: Props = $props();

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
		try {
			const result = await api.refreshSubscription(subscription.id);
			const skipped: string[] = [];
			if (result.skippedDuplicate > 0) skipped.push(`дубликатов: ${result.skippedDuplicate}`);
			if (result.skippedVmess > 0) skipped.push(`vmess: ${result.skippedVmess}`);
			if (result.skippedOther > 0) skipped.push(`не поддерживаемых: ${result.skippedOther}`);
			if (skipped.length > 0) {
				notifications.warning(`Пропущено — ${skipped.join(', ')}`);
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
		// with 409. Surface no error, just no-op the click.
		if (subscription.mode === 'urltest') return;
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
			await Promise.allSettled(
				tags.map(async (tag) => {
					await triggerDelayCheck(tag);
					batchProgress = { done: batchProgress.done + 1, total: batchProgress.total };
				}),
			);
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

<header class="head">
	<div class="head-info">
		<div class="lbl">{modeLabel}</div>
		<div class="val mono">{subscription.selectorTag}</div>
	</div>
	<div class="actions">
		{#if subscription.isInline}
			<Button variant="primary" size="sm" onclick={() => (addOpen = true)}>
				+ Добавить сервер
			</Button>
		{:else}
			<Button variant="primary" size="sm" disabled={refreshing} loading={refreshing} onclick={refresh}>
				{refreshing ? 'Обновляем...' : 'Обновить сейчас'}
			</Button>
		{/if}
		<Button
			variant="ghost"
			size="sm"
			disabled={batchTesting || memberList.length === 0}
			loading={batchTesting}
			onclick={testAll}
		>
			{#if batchTesting}
				Тестируем {batchProgress.done}/{batchProgress.total}
			{:else}
				Проверить всё
			{/if}
		</Button>
	</div>
</header>

{#if lastError}
	<div class="err">{lastError}</div>
{/if}

{#if memberList.length === 0}
	<div class="empty">Подписка ещё не загружена. Нажмите «Обновить сейчас».</div>
{:else}
	<div class="hint">{modeHint}</div>
	<div class="grid">
		{#each memberList as member (member.tag)}
			<div class="member-slot">
				<SubscriptionMemberCard
					{member}
					active={member.tag === effectiveActiveMember}
					switching={switching === member.tag}
					disabled={switching !== null}
					onclick={() => pickActive(member.tag)}
				/>
				{#if subscription.isInline}
					<button
						type="button"
						class="member-remove"
						title="Удалить сервер"
						aria-label="Удалить сервер {member.label || member.tag}"
						disabled={removingTag !== null}
						onclick={(e) => {
							e.stopPropagation();
							requestRemove(member);
						}}
					>
						<svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
							<line x1="18" y1="6" x2="6" y2="18" />
							<line x1="6" y1="6" x2="18" y2="18" />
						</svg>
					</button>
				{/if}
			</div>
		{/each}
	</div>
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
				placeholder="vless://... or trojan://... or hysteria2://..."
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

	.member-slot {
		position: relative;
		min-width: 0;
	}
	.member-remove {
		position: absolute;
		top: 6px;
		right: 6px;
		width: 22px;
		height: 22px;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: 50%;
		color: var(--color-text-muted);
		cursor: pointer;
		transition: color 120ms, border-color 120ms, background 120ms;
		z-index: 1;
	}
	.member-remove:hover {
		color: var(--color-error, #ef4444);
		border-color: var(--color-error, #ef4444);
		background: rgba(239, 68, 68, 0.08);
	}
	.member-remove:disabled { cursor: not-allowed; opacity: 0.5; }

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
</style>
