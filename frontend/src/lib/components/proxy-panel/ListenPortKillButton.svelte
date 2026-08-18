<script lang="ts">
	// Строка секции «Освобождение порта» страницы «Прокси» (EX-47/EX-48,
	// SH-70/SH-71) — единственная форма компонента.
	import { Button, ConfirmModal } from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { parseListenHostPort } from '$lib/utils/listenPortUtils';
	import { errText } from '$lib/utils/errorMessage';

	interface Props {
		listen: string;
		proto?: 'udp' | 'tcp';
		defaultHost?: string;
	}

	let { listen, proto = 'udp', defaultHost = '127.0.0.1' }: Props = $props();

	let killing = $state(false);
	let pid = $state<number | null>(null);
	let comm = $state('');
	let open = $state(false);
	/** SH-94: подтверждение — своя модалка страницы, а не браузерный confirm. */
	let confirmOpen = $state(false);

	const parsed = $derived(parseListenHostPort(listen, defaultHost));

	async function refresh() {
		if (!parsed) {
			pid = null;
			comm = '';
			open = false;
			return;
		}
		try {
			const info = await api.lookupProxyListener(parsed.host, parsed.port, proto);
			open = info.open;
			pid = info.pid ?? null;
			comm = info.comm ?? '';
		} catch {
			open = false;
			pid = null;
			comm = '';
		}
	}

	async function kill() {
		if (!parsed) return;
		confirmOpen = false;
		killing = true;
		try {
			const res = await api.killProxyListener(parsed.host, parsed.port, proto);
			notifications.success(res.message ?? `PID ${res.pid} остановлен`);
			await refresh();
		} catch (e) {
			notifications.error(errText(e));
		} finally {
			killing = false;
		}
	}

	// $effect срабатывает и при монтировании — отдельный onMount дал бы
	// два одинаковых запроса подряд (каждый — обход /proc на роутере).
	$effect(() => {
		listen;
		proto;
		void refresh();
	});
</script>

{#if parsed}
	<div class="kill-row">
		{#if open && pid}
			<span class="kill-text">Порт {parsed.port} занят процессом {comm} (PID {pid})</span>
		{:else}
			<span class="kill-text">Порт {parsed.port} — свободен</span>
		{/if}
		<Button
			variant="secondary"
			size="sm"
			loading={killing}
			disabled={!open || !pid}
			onclick={() => (confirmOpen = true)}
		>
			Освободить порт
		</Button>
	</div>

	<ConfirmModal
		open={confirmOpen}
		title="Освободить порт {parsed.port}? Процесс, занявший его, будет завершён."
		message=""
		confirmLabel="Освободить порт"
		busy={killing}
		onConfirm={kill}
		onClose={() => (confirmOpen = false)}
	/>
{/if}

<style>
	.kill-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.75rem;
		flex-wrap: wrap;
		margin-top: 0.5rem;
	}

	.kill-text {
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
	}
</style>
