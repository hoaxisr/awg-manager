<script lang="ts">
	import type { Awg3Tunnel } from '$lib/types';
	import type { SingboxLayoutMode, TunnelRenderMode } from '$lib/constants/singboxLayout';
	import { api } from '$lib/api/client';
	import { awg3Tunnels, triggerAwg3DelayCheck } from '$lib/stores/awg3';
	import { singboxDelayHistory } from '$lib/stores/singbox';
	import { singboxDelayFromHistory } from '$lib/utils/singboxDelay';
	import type { BadgeVariant } from '$lib/components/ui';
	import { Badge, IconButton, Input, Modal, Button } from '$lib/components/ui';
	import { TunnelDelaySparkBars } from '$lib/components/tunnels';
	import { notifications } from '$lib/stores/notifications';
	import { untrack } from 'svelte';
	import { Waypoints, ShieldCheck, Activity, Pencil, Trash2, Check, X } from 'lucide-svelte';

	interface Props {
		tunnel: Awg3Tunnel;
		renderMode?: TunnelRenderMode;
		layout?: SingboxLayoutMode;
		autoDelayCheckNonce?: number;
		autoDelayCheckDelayMs?: number;
	}

	let {
		tunnel,
		renderMode = 'compact',
		layout = 'compact',
		autoDelayCheckNonce = 0,
		autoDelayCheckDelayMs = 0,
	}: Props = $props();

	let checking = $state(false);
	let deleting = $state(false);
	let confirmDeleteOpen = $state(false);
	let renaming = $state(false);
	let renameValue = $state('');
	let savingRename = $state(false);

	const history = $derived($singboxDelayHistory.get(tunnel.tag) ?? []);
	const delayPresentation = $derived(singboxDelayFromHistory(history));
	const cardState = $derived(delayPresentation.state);
	const latText = $derived(delayPresentation.label);

	// AWG3 device timers — read-only, only the non-empty ones. Edited in RouteBox.
	const timers = $derived(
		[
			{ label: 'rekey', value: tunnel.rekeyTimeout, unit: 's' },
			{ label: 'rekey-after', value: tunnel.rekeyAfterTime, unit: 's' },
			{ label: 'reject', value: tunnel.rejectAfterTime, unit: 's' },
			{ label: 'keepalive', value: tunnel.keepaliveTimeout, unit: 's' },
			{ label: 'max-hs', value: tunnel.maxHandshakeAttempts, unit: '' },
		].filter((t): t is { label: string; value: string; unit: string } => !!t.value),
	);

	// Колонка «Таймеры» в таблице режется по ellipsis — полный список в title
	// самой ячейки (в карточке чипы видны целиком, дубль там не нужен).
	const timersText = $derived(timers.map((t) => `${t.label} ${t.value}${t.unit}`).join(' · '));

	const badgeVariant = $derived<BadgeVariant>(
		cardState === 'ok'
			? 'success'
			: cardState === 'slow'
				? 'warning'
				: cardState === 'fail'
					? 'error'
					: 'muted',
	);

	async function triggerCheck(): Promise<void> {
		if (checking) return;
		checking = true;
		try {
			await triggerAwg3DelayCheck(tunnel.tag);
		} finally {
			checking = false;
		}
	}

	let lastAutoDelayCheckNonce = 0;
	$effect(() => {
		const nonce = autoDelayCheckNonce;
		const delay = autoDelayCheckDelayMs;
		if (nonce <= 0 || nonce === lastAutoDelayCheckNonce) return;
		lastAutoDelayCheckNonce = nonce;

		const timer = setTimeout(() => {
			untrack(() => void triggerCheck());
		}, delay);
		return () => clearTimeout(timer);
	});

	function startRename(): void {
		renameValue = tunnel.tag;
		renaming = true;
	}

	function cancelRename(): void {
		renaming = false;
	}

	async function submitRename(): Promise<void> {
		if (savingRename) return;
		const next = renameValue.trim();
		if (next === '' || next === tunnel.tag) {
			renaming = false;
			return;
		}
		savingRename = true;
		try {
			const fresh = await api.awg3Rename(tunnel.id, next);
			awg3Tunnels.applyMutationResponse(fresh);
			renaming = false;
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Не удалось переименовать туннель');
		} finally {
			savingRename = false;
		}
	}

	async function remove(): Promise<void> {
		deleting = true;
		confirmDeleteOpen = false;
		try {
			const fresh = await api.awg3Delete(tunnel.id);
			awg3Tunnels.applyMutationResponse(fresh);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Не удалось удалить туннель');
		} finally {
			deleting = false;
		}
	}
</script>

{#snippet renameForm()}
	<form class="rename-form" onsubmit={(e) => { e.preventDefault(); void submitRename(); }}>
		<Input bind:value={renameValue} placeholder="Имя туннеля" disabled={savingRename} fullWidth />
		<IconButton ariaLabel="Сохранить имя" title="Сохранить" disabled={savingRename} onclick={() => void submitRename()}>
			<Check size={16} aria-hidden="true" />
		</IconButton>
		<IconButton ariaLabel="Отмена" title="Отмена" disabled={savingRename} onclick={cancelRename}>
			<X size={16} aria-hidden="true" />
		</IconButton>
	</form>
{/snippet}

{#snippet delayBadge()}
	<Badge variant={badgeVariant} size="sm" mono title="Delay">{latText}</Badge>
{/snippet}

{#snippet timersMeta()}
	{#if timers.length > 0}
		<span class="timers" title="Таймеры устройства (настраиваются в RouteBox)">
			{#each timers as t (t.label)}
				<span class="timer"><span class="timer-label">{t.label}</span> {t.value}{t.unit}</span>
			{/each}
		</span>
	{/if}
{/snippet}

{#snippet cardActions()}
	<IconButton ariaLabel="Проверить delay" title="Проверить" disabled={checking} onclick={() => void triggerCheck()}>
		<Activity size={16} aria-hidden="true" />
	</IconButton>
	<IconButton ariaLabel="Переименовать" title="Переименовать" disabled={renaming} onclick={startRename}>
		<Pencil size={16} aria-hidden="true" />
	</IconButton>
	<IconButton variant="danger" ariaLabel="Удалить туннель «{tunnel.tag}»" title="Удалить" disabled={deleting} onclick={() => (confirmDeleteOpen = true)}>
		<Trash2 size={16} aria-hidden="true" />
	</IconButton>
{/snippet}

{#if renderMode === 'table'}
	<tr
		class="awg3-row"
		class:ok={cardState === 'ok'}
		class:slow={cardState === 'slow'}
		class:fail={cardState === 'fail'}
		class:unknown={cardState === 'unknown'}
	>
		<td class="cell cell-name">
			{#if renaming}
				{@render renameForm()}
			{:else}
				<span class="tag" title={tunnel.tag}>{tunnel.tag}</span>
			{/if}
		</td>
		<td class="cell cell-host">
			<span class="host" title={tunnel.host}>{tunnel.host}</span>
		</td>
		<td class="cell cell-hp">
			<div class="hp-cell">
			{#if tunnel.headerProtection}
				<Badge variant="accent" size="xs">
					<ShieldCheck size={11} aria-hidden="true" /> HP
				</Badge>
			{:else}
				<span class="muted">—</span>
			{/if}
			</div>
		</td>
		<td class="cell cell-timers" title={timersText}>
			{#if timers.length === 0}<span class="muted">—</span>{/if}
			{@render timersMeta()}
		</td>
		<td class="cell cell-delay">
			<div class="delay-cell">
				{@render delayBadge()}
				<TunnelDelaySparkBars {history} state={cardState} layout="list" onclick={() => void triggerCheck()} />
			</div>
		</td>
		<td class="cell cell-actions">
			<div class="row-actions">
				{@render cardActions()}
			</div>
		</td>
	</tr>
{:else}
	<div
		class="card"
		class:view-dense={layout === 'dense'}
		class:ok={cardState === 'ok'}
		class:slow={cardState === 'slow'}
		class:fail={cardState === 'fail'}
		class:unknown={cardState === 'unknown'}
	>
		<div class="card-head">
			<div class="icon-tile" aria-hidden="true">
				<Waypoints size={18} />
			</div>
			<div class="head-body">
				{#if renaming}
					{@render renameForm()}
				{:else}
					<div class="title-row">
						<span class="tag" title={tunnel.tag}>{tunnel.tag}</span>
						{@render delayBadge()}
					</div>
					<div class="host-row">
						<span class="host" title={tunnel.host}>{tunnel.host}</span>
						{#if tunnel.headerProtection}
							<Badge variant="accent" size="xs">
								<ShieldCheck size={11} aria-hidden="true" /> HP
							</Badge>
						{/if}
					</div>
					{@render timersMeta()}
				{/if}
			</div>
		</div>

		<TunnelDelaySparkBars {history} state={cardState} layout="compact" onclick={() => void triggerCheck()} />

		<div class="card-actions">
			{@render cardActions()}
		</div>
	</div>
{/if}

<Modal open={confirmDeleteOpen} title="Удаление" size="sm" onclose={() => (confirmDeleteOpen = false)}>
	<p class="confirm-text">Удалить туннель <strong>{tunnel.tag}</strong>?</p>
	{#snippet actions()}
		<Button variant="ghost" size="md" onclick={() => (confirmDeleteOpen = false)}>Отмена</Button>
		<Button variant="danger" size="md" onclick={remove}>Удалить</Button>
	{/snippet}
</Modal>

<style>
	.card {
		display: flex;
		flex-direction: column;
		gap: 10px;
		padding: 12px 14px;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		color: var(--color-text-primary);
		transition: border-color var(--t-fast) ease;
	}
	.card.ok { border-color: var(--color-success-border); }
	.card.slow { border-color: var(--color-warning-border); }
	.card.fail { border-color: var(--color-error-border); }
	.card.unknown { border-color: var(--color-border); }
	.card.view-dense { gap: 8px; padding: 10px 12px; }

	.card-head {
		display: flex;
		align-items: flex-start;
		gap: 10px;
		min-width: 0;
	}

	.icon-tile {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: 34px;
		height: 34px;
		flex-shrink: 0;
		border-radius: var(--radius-sm);
		background: var(--color-accent-tint);
		color: var(--color-accent);
		border: 1px solid var(--color-accent-border);
	}

	.head-body {
		display: flex;
		flex-direction: column;
		gap: 2px;
		min-width: 0;
		flex: 1 1 auto;
	}

	.title-row {
		display: flex;
		align-items: center;
		gap: 6px;
		min-width: 0;
	}

	.tag {
		font-size: 14px;
		font-weight: 600;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		min-width: 0;
		flex: 1 1 auto;
	}
	.title-row :global(.badge) { flex-shrink: 0; }

	.host-row {
		display: flex;
		align-items: center;
		gap: 6px;
		min-width: 0;
	}

	.host {
		font-family: var(--font-mono, monospace);
		font-size: 12px;
		color: var(--color-text-muted);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		min-width: 0;
	}

	.card-actions {
		display: flex;
		align-items: center;
		gap: 6px;
		justify-content: flex-end;
	}

	.rename-form {
		display: flex;
		align-items: center;
		gap: 6px;
		width: 100%;
		min-width: 0;
		position: relative;
	}
	.rename-form :global(.field) { flex: 1 1 auto; min-width: 0; }

	/* Table row */
	.awg3-row .cell {
		vertical-align: middle;
		min-width: 0;
	}
	.awg3-row .tag {
		font-size: 13px;
		font-weight: 600;
	}
	.awg3-row .host {
		font-family: var(--font-mono, monospace);
		font-size: 12px;
		color: var(--color-text-muted);
	}
	.delay-cell {
		display: flex;
		align-items: center;
		gap: 8px;
	}
	.row-actions {
		display: flex;
		align-items: center;
		gap: 4px;
		justify-content: flex-end;
	}
	.muted { color: var(--color-text-muted); }

	/* Read-only AWG3 device timers (edited only in RouteBox) */
	.timers {
		display: inline-flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 4px 8px;
		min-width: 0;
	}
	.timer {
		font-family: var(--font-mono, monospace);
		font-size: 11px;
		color: var(--color-text-muted);
		white-space: nowrap;
	}
	.timer-label { opacity: 0.7; }
	.hp-cell {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 6px;
	}
	/* В таблице таймеры живут одной строкой: перенос раздувал высоту ряда,
	   а nowrap-чипы вылезали за колонку. Полный список — в title. */
	.awg3-row .cell-timers {
		overflow: hidden;
	}
	.awg3-row .timers {
		display: block;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.awg3-row .timer + .timer {
		margin-left: 8px;
	}

	.confirm-text { margin: 0; }
</style>
