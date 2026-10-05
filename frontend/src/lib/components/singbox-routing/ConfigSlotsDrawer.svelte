<!--
  Обзор слотов config.d (эксперт-режим): список всех слотов с бейджами
  владельца, клик по системному слоту — read-only просмотр с подсветкой,
  user-слот (90-user.json) — полноценный редактор с draft-пайплайном.
  «Итоговый конфиг» открывает существующий JsonConfigDrawer (merged preview).
-->
<script lang="ts">
	import { SideDrawer, Button, Badge } from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import ConfigSlotEditor from './ConfigSlotEditor.svelte';
	import { m } from '$lib/i18n';
	import type { ConfigSlotInfo, ConfigSlotContentResponse } from '$lib/types';

	interface Props {
		open: boolean;
		onClose: () => void;
		/** Открыть merged-preview (существующий JsonConfigDrawer). */
		onOpenMerged?: () => void;
	}

	let { open, onClose, onOpenMerged }: Props = $props();

	let slots = $state<ConfigSlotInfo[]>([]);
	let listLoading = $state(false);
	let listError = $state<string | null>(null);

	let selected = $state<ConfigSlotContentResponse | null>(null);
	let selectedInfo = $state<ConfigSlotInfo | null>(null);
	let slotLoading = $state(false);
	/** Ключ пересоздания редактора после discard/refresh. */
	let editorEpoch = $state(0);
	/** В редакторе есть несохранённые правки (проброс из ConfigSlotEditor). */
	let editorDirty = $state(false);

	let wasOpen = $state(false);

	/**
	 * Guard для in-app навигации: несохранённые правки редактора не должны
	 * молча пропасть при возврате к списку или закрытии панели (beforeunload
	 * ловит только уход со страницы). window.confirm — принятый в репо
	 * паттерн для лёгких подтверждений (см. HeadersTextarea).
	 */
	function confirmDiscardDirty(): boolean {
		if (!editorDirty) return true;
		return confirm(m.singbox_routing_slots_confirm_discard());
	}

	async function loadSlots(): Promise<void> {
		listLoading = true;
		listError = null;
		try {
			const res = await api.singboxConfigSlots();
			slots = res.slots;
		} catch (e) {
			listError = e instanceof Error ? e.message : String(e);
			slots = [];
		} finally {
			listLoading = false;
		}
	}

	async function openSlot(info: ConfigSlotInfo): Promise<void> {
		if (slotLoading) return;
		slotLoading = true;
		try {
			const res = await api.singboxConfigSlot(info.slot);
			selected = res;
			selectedInfo = info;
			editorEpoch++;
		} catch (e) {
			listError = e instanceof Error ? e.message : String(e);
		} finally {
			slotLoading = false;
		}
	}

	function backToList(): void {
		if (!confirmDiscardDirty()) return;
		editorDirty = false;
		selected = null;
		selectedInfo = null;
		void loadSlots();
	}

	/** Закрытие панели (крестик/backdrop/Esc/свайп) — тоже через dirty-guard. */
	function handleClose(): void {
		if (!confirmDiscardDirty()) return;
		editorDirty = false;
		onClose();
	}

	function onEditorStateChanged(): void {
		// Обновляем бейджи не покидая редактор: и в списке, и в шапке
		// открытого слота («черновик»/«выключен» должны жить сразу после
		// apply/discard/enable, а не после повторного открытия слота).
		void loadSlots().then(() => {
			const info = selectedInfo;
			if (!info) return;
			const fresh = slots.find((s) => s.slot === info.slot);
			if (!fresh) return;
			selectedInfo = fresh;
			if (selected) {
				// Редактор пересоздаётся только по editorEpoch — обновление
				// selected не трогает текст в textarea.
				selected = {
					...selected,
					hasDraft: fresh.hasDraft,
					state: fresh.size === 0 ? 'absent' : fresh.enabled ? 'active' : 'disabled',
				};
			}
		});
	}

	function formatSize(bytes: number): string {
		if (bytes <= 0) return '—';
		if (bytes < 1024) return m.singbox_routing_slots_size_bytes({ size: bytes });
		return m.singbox_routing_slots_size_kb({ size: (bytes / 1024).toFixed(1) });
	}

	$effect(() => {
		if (open && !wasOpen) {
			wasOpen = true;
			selected = null;
			selectedInfo = null;
			editorDirty = false;
			void loadSlots();
		} else if (!open && wasOpen) {
			// Закрытие: пользовательские пути (крестик/backdrop/Esc) уже
			// прошли dirty-guard в handleClose; при внешнем сбросе open
			// редактор размонтирован SideDrawer'ом — dirty-флаг обнуляем,
			// чтобы устаревшее значение не блокировало следующую сессию.
			wasOpen = false;
			editorDirty = false;
		}
	});

	let title = $derived(
		selected ? m.singbox_routing_slots_title_slot({ filename: selected.filename }) : m.singbox_routing_slots_title(),
	);
</script>

<SideDrawer {open} onClose={handleClose} {title} width={840}>
	<div class="content">
		{#if selected}
			<div class="slot-toolbar">
				<Button variant="ghost" size="sm" onclick={backToList}>{m.singbox_routing_slots_back()}</Button>
				{#if selectedInfo?.ownership === 'system'}
					<Badge variant="muted" size="sm">{m.singbox_routing_slots_badge_generated()}</Badge>
				{:else}
					<Badge variant="accent" size="sm">{m.singbox_routing_slots_badge_user()}</Badge>
				{/if}
				{#if selected.hasDraft}
					<Badge variant="warning" size="sm">{m.singbox_routing_slots_badge_draft()}</Badge>
				{/if}
				{#if selected.state === 'disabled'}
					<Badge variant="muted" size="sm">{m.singbox_routing_slots_badge_disabled()}</Badge>
				{/if}
			</div>
			{#key editorEpoch}
				<ConfigSlotEditor
					filename={selected.filename}
					content={selected.content}
					readonly={selectedInfo?.ownership !== 'user'}
					enabled={selectedInfo?.enabled ?? false}
					hasDraft={selected.hasDraft}
					onStateChanged={onEditorStateChanged}
					onDirtyChange={(d: boolean) => (editorDirty = d)}
				/>
			{/key}
		{:else}
			<div class="list-toolbar">
				<Button variant="secondary" size="sm" onclick={loadSlots} disabled={listLoading}>
					{listLoading ? m.common_loading() : m.common_refresh()}
				</Button>
				<div class="spacer"></div>
				{#if onOpenMerged}
					<Button variant="secondary" size="sm" onclick={onOpenMerged}>{m.singbox_routing_slots_merged()}</Button>
				{/if}
			</div>

			{#if listError}
				<div class="error">
					<div class="error-title">{m.singbox_routing_slots_load_failed()}</div>
					<div class="error-message">{listError}</div>
				</div>
			{:else if listLoading && slots.length === 0}
				<div class="placeholder">{m.common_loading()}</div>
			{:else}
				<div class="slot-list">
					{#each slots as s (s.slot)}
						<button type="button" class="slot-row" onclick={() => openSlot(s)} disabled={slotLoading}>
							<span class="slot-file">{s.filename}</span>
							<span class="slot-name">{s.slot}</span>
							<span class="badges">
								{#if s.ownership === 'user'}
									<Badge variant="accent" size="sm">{m.singbox_routing_slots_badge_user()}</Badge>
								{:else}
									<Badge variant="muted" size="sm">{m.singbox_routing_slots_badge_generated()}</Badge>
								{/if}
								{#if s.hasDraft}
									<Badge variant="warning" size="sm">{m.singbox_routing_slots_badge_draft()}</Badge>
								{/if}
								{#if !s.enabled}
									<Badge variant="muted" size="sm">{m.singbox_routing_slots_badge_disabled()}</Badge>
								{/if}
							</span>
							<span class="slot-size">{formatSize(s.size)}</span>
						</button>
					{/each}
				</div>
				<div class="list-hint">
					{m.singbox_routing_slots_list_hint()}
				</div>
			{/if}
		{/if}
	</div>
</SideDrawer>

<style>
	.content {
		display: flex;
		flex-direction: column;
		gap: 0.75rem;
		height: 100%;
		min-height: 0;
	}

	.list-toolbar,
	.slot-toolbar {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		flex-wrap: wrap;
	}
	.spacer {
		flex: 1;
	}

	.slot-list {
		display: flex;
		flex-direction: column;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		overflow: hidden;
	}

	.slot-row {
		display: grid;
		grid-template-columns: minmax(9rem, auto) minmax(0, auto) 1fr auto;
		align-items: center;
		gap: 0.75rem;
		padding: 0.55rem 0.75rem;
		background: transparent;
		border: none;
		border-bottom: 1px solid var(--color-border);
		font-family: inherit;
		font-size: 0.85rem;
		color: var(--color-text-primary);
		text-align: left;
		cursor: pointer;
	}
	.slot-row:last-child {
		border-bottom: none;
	}
	.slot-row:hover {
		background: var(--color-bg-hover, var(--color-bg-tertiary));
	}

	.slot-file {
		font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
		font-size: 0.8rem;
	}
	.slot-name {
		color: var(--color-text-muted);
		font-size: 0.78rem;
	}
	.badges {
		display: inline-flex;
		gap: 0.375rem;
		flex-wrap: wrap;
	}
	.slot-size {
		color: var(--color-text-muted);
		font-size: 0.78rem;
		white-space: nowrap;
	}

	.list-hint {
		font-size: 0.78rem;
		color: var(--color-text-secondary);
		line-height: 1.4;
	}

	.placeholder {
		padding: 1.5rem;
		text-align: center;
		color: var(--color-text-secondary);
		font-size: 13px;
		border: 1px dashed var(--color-border);
		border-radius: var(--radius-sm);
	}

	.error {
		padding: 0.875rem 1rem;
		border: 1px solid var(--color-error);
		background: color-mix(in srgb, var(--color-error) 12%, transparent);
		border-radius: var(--radius-sm);
	}
	.error-title {
		font-weight: 600;
		font-size: 13px;
		margin-bottom: 0.25rem;
	}
	.error-message {
		font-size: 12px;
		color: var(--color-text-secondary);
		word-break: break-word;
	}

	@media (max-width: 640px) {
		.slot-row {
			grid-template-columns: 1fr auto;
			grid-auto-rows: auto;
		}
		.slot-name {
			display: none;
		}
	}
</style>
