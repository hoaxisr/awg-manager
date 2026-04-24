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

<div class="card">
	<h3>Настройки inbound</h3>

	<div class="row">
		<label>Включён</label>
		<Toggle checked={draft.enabled} onchange={(v) => (draft.enabled = v)} />
	</div>

	<div class="row">
		<label for="dp-port">Порт</label>
		<input id="dp-port" type="number" min="1024" max="65535" bind:value={draft.port} />
	</div>

	<div class="row">
		<span>Слушать на:</span>
		<div class="listen-group">
			<label>
				<input type="radio" bind:group={draft.listenAll} value={true} />
				Все интерфейсы
			</label>
			<label>
				<input type="radio" bind:group={draft.listenAll} value={false} />
				Конкретном интерфейсе
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

	<div class="row">
		<label>Требовать auth</label>
		<Toggle checked={draft.auth.enabled} onchange={(v) => (draft.auth.enabled = v)} />
	</div>

	{#if draft.auth.enabled}
		<div class="row">
			<label for="dp-user">Имя пользователя</label>
			<input id="dp-user" type="text" bind:value={draft.auth.username} />
		</div>
		<div class="row">
			<label for="dp-pass">Пароль</label>
			<input id="dp-pass" type="text" bind:value={draft.auth.password} />
			<button type="button" class="btn btn-sm" onclick={generatePassword}>Сгенерировать</button>
		</div>
	{/if}

	<div class="actions">
		<button type="button" class="btn btn-secondary" onclick={reset} disabled={saving}>Отменить</button>
		<button type="button" class="btn btn-primary" onclick={save} disabled={saving}>Сохранить</button>
	</div>
</div>

<style>
	.card { padding: 12px; border: 1px solid var(--border); border-radius: 8px; margin-bottom: 12px; }
	.row { display: grid; grid-template-columns: 180px 1fr auto; gap: 8px; align-items: center; margin: 6px 0; }
	.listen-group { display: flex; flex-direction: column; gap: 4px; }
	.actions { display: flex; justify-content: flex-end; gap: 8px; margin-top: 12px; }
</style>
