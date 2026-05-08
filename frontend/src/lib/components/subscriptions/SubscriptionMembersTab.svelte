<script lang="ts">
	import { untrack } from 'svelte';
	import type { Subscription, SubscriptionMember } from '$lib/types';
	import { api } from '$lib/api/client';
	import { Button } from '$lib/components/ui';
	import { triggerDelayCheck } from '$lib/stores/singbox';
	import SubscriptionMemberCard from './SubscriptionMemberCard.svelte';

	interface Props {
		subscription: Subscription;
		onUpdated: () => void;
		autoDelayCheckNonce?: number;
	}
	let { subscription, onUpdated, autoDelayCheckNonce = 0 }: Props = $props();

	let refreshing = $state(false);
	let switching = $state<string | null>(null);
	let lastError = $state('');
	let batchTesting = $state(false);
	let batchProgress = $state({ done: 0, total: 0 });
	let lastAutoDelayCheckNonce = 0;
	let confirmClearOrphans = $state(false);
	let clearingOrphans = $state(false);

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

	async function refresh(): Promise<void> {
		refreshing = true;
		lastError = '';
		try {
			await api.refreshSubscription(subscription.id);
			onUpdated();
		} catch (e) {
			lastError = e instanceof Error ? e.message : 'Не удалось обновить';
		} finally {
			refreshing = false;
		}
	}

	async function pickActive(memberTag: string): Promise<void> {
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
		<div class="lbl">Selector</div>
		<div class="val mono">{subscription.selectorTag}</div>
	</div>
	<div class="actions">
		<Button variant="primary" size="sm" disabled={refreshing} loading={refreshing} onclick={refresh}>
			{refreshing ? 'Обновляем...' : 'Обновить сейчас'}
		</Button>
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
	<div class="hint">Выберите активный сервер. Selector направит трафик в выбранный outbound.</div>
	<div class="grid">
		{#each memberList as member (member.tag)}
			<SubscriptionMemberCard
				{member}
				active={member.tag === subscription.activeMember}
				switching={switching === member.tag}
				disabled={switching !== null}
				onclick={() => pickActive(member.tag)}
			/>
		{/each}
	</div>
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
		grid-template-columns: repeat(auto-fill, minmax(260px, 1fr));
		gap: 0.7rem;
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
</style>
