<script lang="ts">
	import { onDestroy } from 'svelte';
	import { Button } from '$lib/components/ui';
	import type { PeerPresets } from '$lib/types';
	import { validateClientAllowedIPs, validateRemoteSubnets } from '$lib/utils/peerForm';
	import { notifications } from '$lib/stores/notifications';

	interface Props {
		clientAllowedIPs: string;
		/** Текст textarea: по CIDR в строке (или через запятую). */
		remoteSubnets: string;
		/** Пир без локальной записи: полям негде храниться (NO_PEER_SECRET у бэкенда). */
		disabled?: boolean;
		/** Пресеты с бэкенда под текущий DNS формы: сети роутера знает только он. */
		loadPresets: () => Promise<PeerPresets>;
		idPrefix: string;
	}

	let {
		clientAllowedIPs = $bindable(''),
		remoteSubnets = $bindable(''),
		disabled = false,
		loadPresets,
		idPrefix
	}: Props = $props();

	let loading = $state(false);
	// Модалка размонтирует содержимое при закрытии: поздний ответ пресета из
	// закрытой сессии не должен писать в поля уже переоткрытой.
	let destroyed = $state(false);
	onDestroy(() => {
		destroyed = true;
	});
	const allowedError = $derived(validateClientAllowedIPs(clientAllowedIPs));
	const subnetsError = $derived(validateRemoteSubnets(remoteSubnets));

	async function applyPreset(kind: keyof PeerPresets) {
		loading = true;
		try {
			const presets = await loadPresets();
			if (!destroyed) clientAllowedIPs = presets[kind];
		} catch (e) {
			if (!destroyed) notifications.error(e instanceof Error ? e.message : 'Не удалось получить пресет');
		} finally {
			loading = false;
		}
	}
</script>

<div class="form-group">
	<label class="field-label" for="{idPrefix}-allowed">AllowedIPs клиента</label>
	<input
		type="text"
		id="{idPrefix}-allowed"
		class="field-input"
		bind:value={clientAllowedIPs}
		placeholder="0.0.0.0/0, ::/0"
		{disabled}
	/>
	<div class="preset-row">
		<Button variant="ghost" size="sm" onclick={() => applyPreset('routerOnly')} disabled={disabled || loading}>
			Только сети роутера
		</Button>
		<Button variant="ghost" size="sm" onclick={() => applyPreset('exceptRouter')} disabled={disabled || loading}>
			Всё, кроме сетей роутера
		</Button>
	</div>
	{#if disabled}
		<span class="field-hint">Недоступно: клиент создан вне AWG Manager</span>
	{:else if allowedError}
		<span class="field-hint is-error">{allowedError}</span>
	{:else}
		<span class="field-hint">Пусто — весь трафик через туннель. После изменения перевыдайте конфигурацию клиенту</span>
	{/if}
</div>
<div class="form-group">
	<label class="field-label" for="{idPrefix}-subnets">Сети за клиентом</label>
	<textarea
		id="{idPrefix}-subnets"
		class="field-textarea"
		rows="2"
		bind:value={remoteSubnets}
		placeholder="192.168.77.0/24"
		{disabled}
	></textarea>
	{#if disabled}
		<span class="field-hint">Недоступно: клиент создан вне AWG Manager</span>
	{:else if subnetsError}
		<span class="field-hint is-error">{subnetsError}</span>
	{:else}
		<span class="field-hint">Роутер отправит трафик этим сетям через этого клиента; маршруты awg-manager создаст сам</span>
	{/if}
</div>

<style>
	/* Поля — примитивы app.css (.field-label/.field-input/.field-textarea, там же
	   :disabled). Поправки ниже — под соседние поля модалки: их .label —
	   0.8125rem, а input'ы рисует глобальный сброс `input:not(…)` (он перебивает
	   и .field-input), textarea же он не перебивает — выравниваем руками. */
	.form-group {
		display: flex;
		flex-direction: column;
		gap: 0.375rem;
	}

	.field-label {
		font-size: 0.8125rem;
	}

	.field-textarea {
		resize: vertical;
		font-family: inherit;
		font-size: 0.875rem;
		padding: 0.5rem 0.75rem;
		background: var(--bg-secondary);
	}

	.preset-row {
		display: flex;
		gap: 0.5rem;
		flex-wrap: wrap;
	}
</style>
