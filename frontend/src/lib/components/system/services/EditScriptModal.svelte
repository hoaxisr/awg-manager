<script lang="ts">
	import { api, type SystemServiceItem } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { Button, Modal } from '$lib/components/ui';
	import { errorMessage } from '$lib/utils/errorMessage';
	import { RefreshCw, RotateCw, Check } from 'lucide-svelte';

	interface Props {
		/** Открытая на редактирование служба; null — модалка закрыта. */
		item: SystemServiceItem | null;
		onclose: () => void;
		/** Скрипт сохранён: перезапустить службу при `restartAfter` и перечитать список. */
		onSaved: (restartAfter: boolean) => Promise<void>;
	}

	let { item, onclose, onSaved }: Props = $props();

	let content = $state('');
	let loading = $state(false);
	let saving = $state(false);

	$effect(() => {
		if (item) void loadContent(item);
	});

	async function loadContent(target: SystemServiceItem) {
		content = '';
		loading = true;
		try {
			const res = await api.systemServicesGet(target.script);
			content = res.content;
		} catch (e) {
			notifications.error(errorMessage(e, 'Не удалось загрузить содержимое скрипта'));
			onclose();
		} finally {
			loading = false;
		}
	}

	async function handleSave(restartAfter = false) {
		if (!item) return;
		saving = true;
		const scriptName = item.script.split('/').pop() || item.name;
		try {
			await api.systemServicesSave({ scriptName, content });
			notifications.success(`Скрипт ${scriptName} сохранен`);
			await onSaved(restartAfter);
		} catch (e) {
			notifications.error(errorMessage(e, 'Ошибка сохранения скрипта'));
		} finally {
			saving = false;
		}
	}
</script>

<Modal
	open={item !== null}
	title={`Редактирование скрипта службы: ${item?.name || ''}`}
	size="lg"
	{onclose}
>
	{#if item}
		<div class="edit-modal-root">
			<div class="edit-meta-bar">
				<code>{item.script}</code>
				<span class="edit-hint">Права доступа 0755 (rwxr-xr-x) сохраняются автоматически</span>
			</div>

			{#if loading}
				<div class="empty-state">
					<RefreshCw size={24} class="spin" />
					<p>Загрузка содержимого скрипта…</p>
				</div>
			{:else}
				<textarea
					rows="16"
					class="code-textarea"
					bind:value={content}
				></textarea>
			{/if}
		</div>
	{/if}

	{#snippet actions()}
		<div class="modal-footer-btns">
			<Button variant="ghost" onclick={onclose}>Отмена</Button>
			<Button variant="secondary" loading={saving} onclick={() => handleSave(true)}>
				{#snippet iconBefore()}<RotateCw size={13} />{/snippet}
				Сохранить и перезапустить
			</Button>
			<Button variant="primary" loading={saving} onclick={() => handleSave(false)}>
				{#snippet iconBefore()}<Check size={13} />{/snippet}
				Сохранить
			</Button>
		</div>
	{/snippet}
</Modal>

<style>
	.edit-modal-root {
		display: flex;
		flex-direction: column;
		gap: 1rem;
	}

	.edit-meta-bar {
		display: flex;
		justify-content: space-between;
		align-items: center;
		font-size: 0.78rem;
		background: var(--color-bg-secondary);
		padding: 0.35rem 0.6rem;
		border-radius: var(--radius-sm, 6px);
		border: 1px solid var(--color-border);
	}
	.edit-hint {
		color: var(--color-text-muted);
		font-size: 0.72rem;
	}

	.code-textarea {
		width: 100%;
		box-sizing: border-box;
		padding: 0.6rem;
		border-radius: var(--radius-sm, 6px);
		border: 1px solid var(--color-border);
		background: var(--color-bg-tertiary);
		color: var(--color-text-primary);
		font-family: var(--font-mono, monospace);
		font-size: 0.8rem;
		line-height: 1.4;
		resize: vertical;
	}
	.code-textarea:focus {
		border-color: var(--color-accent);
		outline: none;
	}

	.empty-state {
		padding: 3rem;
		text-align: center;
		color: var(--color-text-muted);
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 0.5rem;
	}

	.modal-footer-btns {
		display: flex;
		justify-content: flex-end;
		gap: 0.5rem;
		width: 100%;
	}
</style>
