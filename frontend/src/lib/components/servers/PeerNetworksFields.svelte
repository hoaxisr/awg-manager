<script lang="ts">
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
	const allowedError = $derived(validateClientAllowedIPs(clientAllowedIPs));
	const subnetsError = $derived(validateRemoteSubnets(remoteSubnets));

	async function applyPreset(kind: keyof PeerPresets) {
		loading = true;
		try {
			clientAllowedIPs = (await loadPresets())[kind];
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Не удалось получить пресет');
		} finally {
			loading = false;
		}
	}
</script>

<div class="form-group">
	<label class="label" for="{idPrefix}-allowed">AllowedIPs клиента</label>
	<input
		type="text"
		id="{idPrefix}-allowed"
		class="input"
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
	<label class="label" for="{idPrefix}-subnets">Сети за клиентом</label>
	<textarea
		id="{idPrefix}-subnets"
		class="input"
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
	/* Локальные копии стилей модалок пиров: их .form-group/.label/.input scoped и
	   до этого компонента не доходят. Общие классы (.field-hint) — из app.css. */
	.form-group {
		display: flex;
		flex-direction: column;
		gap: 0.375rem;
	}

	.label {
		font-size: 0.8125rem;
		font-weight: 500;
		color: var(--text-secondary);
	}

	.input {
		padding: 8px 12px;
		font-size: 13px;
		background: var(--bg-primary);
		border: 1px solid var(--border);
		border-radius: 6px;
		color: var(--text-primary);
	}

	.input:focus {
		outline: none;
		border-color: var(--accent);
	}

	textarea.input {
		resize: vertical;
		font-family: inherit;
	}

	.preset-row {
		display: flex;
		gap: 0.5rem;
		flex-wrap: wrap;
	}
</style>
