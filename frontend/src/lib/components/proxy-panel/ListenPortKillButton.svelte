<script lang="ts">
	import { onMount } from 'svelte';
	import { Button } from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { parseListenHostPort } from '$lib/utils/listenPortUtils';
	import { errText } from '$lib/utils/errorMessage';

	interface Props {
		listen: string;
		proto?: 'udp' | 'tcp';
		defaultHost?: string;
		compact?: boolean;
	}

	let { listen, proto = 'udp', defaultHost = '127.0.0.1', compact = true }: Props = $props();

	let loading = $state(false);
	let killing = $state(false);
	let pid = $state<number | null>(null);
	let comm = $state('');
	let open = $state(false);

	const parsed = $derived(parseListenHostPort(listen, defaultHost));

	async function refresh() {
		if (!parsed) {
			pid = null;
			comm = '';
			open = false;
		 return;
		}
		loading = true;
		try {
			const info = await api.lookupProxyListener(parsed.host, parsed.port, proto);
			open = info.open;
			pid = info.pid ?? null;
			comm = info.comm ?? '';
		} catch {
			open = false;
			pid = null;
			comm = '';
		} finally {
			loading = false;
		}
	}

	async function kill() {
		if (!parsed) return;
		if (!confirm(`Остановить процесс${pid ? ` PID ${pid}` : ''} на порту ${parsed.port}?`)) return;
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

	onMount(() => {
		void refresh();
	});

	$effect(() => {
		listen;
		proto;
		void refresh();
	});
</script>

{#if parsed}
	<span class="listen-kill" class:compact>
		{#if loading}
			<span class="listen-kill-hint">…</span>
		{:else if open && pid}
			<span class="listen-kill-hint" title={comm || undefined}>PID {pid}{comm ? ` · ${comm}` : ''}</span>
		{/if}
		<Button
			variant="ghost"
			size="sm"
			loading={killing}
			disabled={!open || !pid}
			onclick={kill}
			title={pid ? `Освободить порт ${parsed.port} (kill PID ${pid})` : `Порт ${parsed.port} свободен`}
		>
			Kill port
		</Button>
	</span>
{/if}

<style>
	.listen-kill {
		display: inline-flex;
		align-items: center;
		gap: 0.375rem;
	}

	.listen-kill.compact :global(button) {
		font-size: 0.6875rem;
		padding: 0.15rem 0.45rem;
		min-height: 0;
	}

	.listen-kill-hint {
		font-size: 0.6875rem;
		font-family: var(--font-mono);
		color: var(--color-text-secondary);
	}
</style>
