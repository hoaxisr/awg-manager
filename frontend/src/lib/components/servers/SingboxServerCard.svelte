<script lang="ts">
	import type { SingboxInboundServer } from '$lib/types';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { Button, Modal } from '$lib/components/ui';

	interface Props {
		server: SingboxInboundServer;
		onDeleted?: () => void;
	}

	let { server, onDeleted = () => {} }: Props = $props();

	let confirmOpen = $state(false);
	let isDeleting = $state(false);

	async function deleteServer() {
		try {
			isDeleting = true;
			await api.singboxDeleteServer(server.tag);
			notifications.success('Сервер удалён');
			onDeleted();
			confirmOpen = false;
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка');
		} finally {
			isDeleting = false;
		}
	}
</script>

<div class="card">
	<div class="header">
		<h3>{server.tag}</h3>
		<span class="protocol">{server.protocol}</span>
		<span class="status" class:running={server.running}>
			{server.running ? 'Запущен' : 'Остановлен'}
		</span>
	</div>
	<div class="details">
		<p>Listen: {server.listen}:{server.listenPort}</p>
		{#if server.tls?.enabled}
			<p>TLS: {server.tls.serverName || 'enabled'}</p>
		{/if}
		{#if server.protocol === 'vless'}
			<p>UUID: {server.users?.[0]?.uuid || 'не задан'}</p>
			{#if server.reality?.enabled}
				<p>Reality: {server.reality.handshakeServer}:{server.reality.handshakePort}</p>
			{/if}
		{:else if server.protocol === 'hysteria2'}
			<p>Пароль: {server.users?.[0]?.password ? 'задан' : 'не задан'}</p>
			{#if server.hysteria2?.upMbps || server.hysteria2?.downMbps}
				<p>Лимиты: ↑{server.hysteria2?.upMbps || 0}Mbps ↓{server.hysteria2?.downMbps || 0}Mbps</p>
			{/if}
		{:else if server.protocol === 'naive'}
			<p>Пользователь: {server.users?.[0]?.username || 'не задан'}</p>
			{#if server.naive?.network}
				<p>Сеть: {server.naive.network}</p>
			{/if}
		{/if}
	</div>
	<div class="actions">
		<Button variant="danger" size="sm" onclick={() => (confirmOpen = true)}>Удалить</Button>
	</div>
</div>

<Modal
	open={confirmOpen}
	title="Удалить сервер"
	size="sm"
	onclose={() => {
		if (!isDeleting) confirmOpen = false;
	}}
>
	<p>Удалить сервер <b>{server.tag}</b>?</p>
	{#snippet actions()}
		<Button variant="ghost" onclick={() => (confirmOpen = false)} disabled={isDeleting}>Отмена</Button>
		<Button variant="danger" onclick={deleteServer} loading={isDeleting}>Удалить</Button>
	{/snippet}
</Modal>

<style>
	.card {
		border: 1px solid #e5e7eb;
		border-radius: 0.5rem;
		padding: 1rem;
		margin: 0.5rem 0;
		background: white;
		box-shadow: 0 1px 3px rgba(0,0,0,0.1);
	}
	.header {
		display: flex;
		justify-content: space-between;
		align-items: center;
		margin-bottom: 0.5rem;
	}
	.protocol {
		background: #f3f4f6;
		padding: 0.25rem 0.5rem;
		border-radius: 0.25rem;
		font-size: 0.875rem;
	}
	.status.running {
		color: #10b981;
	}
	.status:not(.running) {
		color: #ef4444;
	}
	.details p {
		margin: 0.25rem 0;
		font-size: 0.875rem;
		color: #6b7280;
	}
	.actions {
		margin-top: 0.5rem;
	}
</style>
