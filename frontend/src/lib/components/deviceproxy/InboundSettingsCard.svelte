<script lang="ts">
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { Toggle } from '$lib/components/ui';
	import type { DeviceProxyConfig } from '$lib/types';

	interface Props {
		config: DeviceProxyConfig;
		bridgeInterfaces: { id: string; label: string }[];
		onSaved: (cfg: DeviceProxyConfig) => void;
	}

	let { config, bridgeInterfaces, onSaved }: Props = $props();

	// Draft is a one-time snapshot of the prop so the user can edit
	// without the form snapping back when the store polls. `config`
	// changes intentionally do NOT re-sync the draft — reset() is the
	// explicit resync affordance.
	// svelte-ignore state_referenced_locally
	let draft = $state<DeviceProxyConfig>(structuredClone(config));
	let saving = $state(false);

	function reset() {
		draft = structuredClone(config);
	}

	function generatePassword() {
		const charset = 'ABCDEFGHIJKLMNPQRSTUVWXYZabcdefghijkmnpqrstuvwxyz23456789';
		let out = '';
		const arr = new Uint32Array(16);
		crypto.getRandomValues(arr);
		for (const n of arr) out += charset[n % charset.length];
		draft.auth.password = out;
	}

	async function save() {
		saving = true;
		try {
			const saved = await api.saveDeviceProxyConfig(draft);
			onSaved(saved);
			notifications.success('Настройки сохранены');
		} catch (e) {
			notifications.error(`Ошибка: ${(e as Error).message}`);
		} finally {
			saving = false;
		}
	}
</script>

<section class="card">
	<h2 class="section-title">Настройки inbound</h2>

	<div class="settings-stack">
		<div class="setting-row">
			<div class="flex flex-col gap-1">
				<span class="font-medium">Включён</span>
				<span class="setting-description">Запустить SOCKS5/HTTP-прокси на роутере</span>
			</div>
			<Toggle checked={draft.enabled} onchange={(v) => (draft.enabled = v)} />
		</div>

		<div class="setting-row">
			<div class="flex flex-col gap-1">
				<span class="font-medium">Порт</span>
				<span class="setting-description">На котором слушает inbound. По умолчанию 1099.</span>
			</div>
			<input id="dp-port" type="number" min="1024" max="65535" bind:value={draft.port} class="port-input" />
		</div>

		<div class="setting-row">
			<div class="flex flex-col gap-1">
				<span class="font-medium">Слушать на</span>
				<span class="setting-description">Все интерфейсы или конкретный bridge роутера</span>
			</div>
			<div class="radio-stack">
				<label class="radio-item">
					<input type="radio" bind:group={draft.listenAll} value={true} />
					Все интерфейсы
				</label>
				<label class="radio-item">
					<input type="radio" bind:group={draft.listenAll} value={false} />
					Конкретный интерфейс
				</label>
				{#if !draft.listenAll}
					<select bind:value={draft.listenInterface}>
						{#each bridgeInterfaces as br (br.id)}
							<option value={br.id}>{br.label}</option>
						{/each}
					</select>
				{/if}
			</div>
		</div>

		<div class="setting-row">
			<div class="flex flex-col gap-1">
				<span class="font-medium">Требовать auth</span>
				<span class="setting-description">SOCKS5 и HTTP-прокси запросят логин/пароль</span>
			</div>
			<Toggle checked={draft.auth.enabled} onchange={(v) => (draft.auth.enabled = v)} />
		</div>

		{#if draft.auth.enabled}
			<div class="setting-row">
				<div class="flex flex-col gap-1">
					<span class="font-medium">Имя пользователя</span>
				</div>
				<input id="dp-user" type="text" bind:value={draft.auth.username} class="text-input" />
			</div>
			<div class="setting-row">
				<div class="flex flex-col gap-1">
					<span class="font-medium">Пароль</span>
				</div>
				<div class="password-group">
					<input id="dp-pass" type="text" bind:value={draft.auth.password} />
					<button type="button" class="btn btn-ghost btn-sm" onclick={generatePassword}>
						Сгенерировать
					</button>
				</div>
			</div>
		{/if}
	</div>

	<div class="form-actions">
		<button type="button" class="btn btn-ghost" onclick={reset} disabled={saving}>Отменить</button>
		<button type="button" class="btn btn-primary" onclick={save} disabled={saving}>Сохранить</button>
	</div>
</section>

<style>
	.section-title {
		font-size: 1rem;
		font-weight: 600;
		margin-bottom: 1rem;
	}

	.radio-stack {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
		min-width: 220px;
	}

	.radio-item {
		display: inline-flex;
		align-items: center;
		gap: 0.5rem;
		cursor: pointer;
		margin: 0;
		font-size: 0.875rem;
		color: var(--text-primary);
	}

	.radio-item input[type='radio'] {
		width: auto;
		margin: 0;
	}

	.port-input {
		width: 120px;
		flex-shrink: 0;
	}

	.text-input {
		min-width: 200px;
		flex-shrink: 0;
	}

	.password-group {
		display: flex;
		gap: 0.5rem;
		align-items: center;
		min-width: 260px;
	}

	.form-actions {
		display: flex;
		justify-content: flex-end;
		gap: 0.5rem;
		margin-top: 1rem;
		padding-top: 1rem;
		border-top: 1px solid var(--border);
	}
</style>
