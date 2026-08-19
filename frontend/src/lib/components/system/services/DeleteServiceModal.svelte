<script lang="ts">
	import { api, type SystemServiceItem } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { Button, Modal } from '$lib/components/ui';
	import { errorMessage } from '$lib/utils/errorMessage';
	import { Trash2, AlertTriangle } from 'lucide-svelte';

	interface Props {
		/** Служба, помеченная на удаление; null — модалка закрыта. */
		item: SystemServiceItem | null;
		onclose: () => void;
		onDeleted: () => Promise<void>;
	}

	let { item, onclose, onDeleted }: Props = $props();

	let deleting = $state(false);

	async function handleDelete() {
		if (!item) return;
		deleting = true;
		try {
			await api.systemServicesDelete(item.script);
			notifications.success(`Служба ${item.name} удалена`);
			onclose();
			await onDeleted();
		} catch (e) {
			notifications.error(errorMessage(e, 'Ошибка удаления службы'));
		} finally {
			deleting = false;
		}
	}
</script>

<Modal open={item !== null} title="Удаление службы" size="sm" {onclose}>
	{#if item}
		<div class="delete-modal-content">
			<AlertTriangle size={28} class="danger-icon" />
			<div>
				<p>Вы действительно хотите удалить службу <strong>{item.name}</strong>?</p>
				<p class="muted-p">Служба будет остановлена, а скрипт <code>{item.script}</code> безвозвратно удален с роутера.</p>
			</div>
		</div>
	{/if}

	{#snippet actions()}
		<div class="modal-footer-btns">
			<Button variant="ghost" onclick={onclose}>Отмена</Button>
			<Button variant="danger" loading={deleting} onclick={handleDelete}>
				{#snippet iconBefore()}<Trash2 size={13} />{/snippet}
				Удалить службу
			</Button>
		</div>
	{/snippet}
</Modal>

<style>
	.delete-modal-content {
		display: flex;
		gap: 0.75rem;
		align-items: flex-start;
		padding: 0.5rem 0;
	}
	:global(.danger-icon) {
		color: var(--color-error, #f87171);
		flex-shrink: 0;
	}
	.muted-p {
		color: var(--color-text-muted);
		font-size: 0.8rem;
		margin: 0.35rem 0 0 0;
	}

	.modal-footer-btns {
		display: flex;
		justify-content: flex-end;
		gap: 0.5rem;
		width: 100%;
	}
</style>
