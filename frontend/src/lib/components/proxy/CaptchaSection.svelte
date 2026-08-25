<script lang="ts">
	// EX-25..29 — «Подтверждение VK». Только FreeTurn-клиент (у wdtt капча —
	// лишь режим -captcha-mode, детекта ожидания в менеджере нет) и только пока
	// статус говорит waiting/queued: в типовом случае подтверждать нечего.
	import { onDestroy, onMount } from 'svelte';
	import { Button } from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import { createSelfReschedulingPoll } from '$lib/utils/selfReschedulingPoll';
	import { pluralForm, STREAM_WORDS } from '$lib/utils/pluralize';
	import type { FreeTurnCaptchaClientStatus } from '$lib/types';
	import DetailSection from './DetailSection.svelte';

	interface Props {
		clientId: string;
	}

	let { clientId }: Props = $props();

	let entry = $state<FreeTurnCaptchaClientStatus | null>(null);

	async function load() {
		try {
			const overview = await api.getFreeTurnCaptchaStatus();
			entry = overview.clients?.find((c) => c.clientId === clientId) ?? null;
		} catch {
			// Молча: капча — не повод засыпать экран ошибками поллинга.
			entry = null;
		}
	}

	const poll = createSelfReschedulingPoll(load, 5000);

	onMount(async () => {
		await load();
		poll.start();
	});
	onDestroy(() => poll.stop());

	const pending = $derived(!!entry && (entry.waiting || entry.queued));
	const streams = $derived(entry?.pendingStreams ?? 0);

	function openCaptcha() {
		// Запасной адрес собирается по ключу инстанса (роль:id) — так адресует
		// инстансы новая поверхность.
		const url =
			entry?.url?.trim() ||
			`/api/proxyrt/instances/${encodeURIComponent(`freeturn-client:${clientId}`)}/captcha/`;
		window.open(url, '_blank', 'noopener');
	}
</script>

{#if pending}
	<DetailSection
		title="Подтверждение VK"
		hint="Пока подтверждение не пройдено, потоки не поднимаются. Капча открывается через менеджер, отдельного порта наружу не нужно."
	>
		<p class="line">Ожидает подтверждения: {streams} {pluralForm(streams, STREAM_WORDS)}</p>
		{#if entry?.portContention}
			<p class="line">Порт капчи занят другим инстансом</p>
		{/if}
		<div class="btn-row">
			<Button variant="primary" disabled={!entry?.canOpen} onclick={openCaptcha}>Открыть капчу</Button>
		</div>
	</DetailSection>
{/if}

<style>
	.line {
		margin: 0 0 0.375rem;
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
	}
</style>
