<script lang="ts">
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { Toggle } from '$lib/components/ui';
	import type { DeviceProxyConfig, DeviceProxyOutbound } from '$lib/types';

	interface Props {
		config: DeviceProxyConfig;
		outbounds: DeviceProxyOutbound[];
		bridgeInterfaces: { id: string; label: string }[];
		onSaved: (cfg: DeviceProxyConfig) => void;
	}

	let { config, outbounds, bridgeInterfaces, onSaved }: Props = $props();

	// Draft is a one-time snapshot of the prop. Edits survive store
	// refreshes — reset() is the explicit resync affordance.
	// svelte-ignore state_referenced_locally
	let draft = $state<DeviceProxyConfig>(structuredClone(config));
	let saving = $state(false);

	// "listenChoice" is the UI aggregation of draft.listenAll + draft.listenInterface
	// into a single dropdown value: either '__all' or the interface id.
	let listenChoice = $derived(draft.listenAll ? '__all' : draft.listenInterface);

	function setListenChoice(v: string) {
		if (v === '__all') {
			draft.listenAll = true;
			draft.listenInterface = '';
		} else {
			draft.listenAll = false;
			draft.listenInterface = v;
		}
	}

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

	let grouped = $derived.by(() => {
		const direct = outbounds.filter((o) => o.kind === 'direct');
		const sb = outbounds.filter((o) => o.kind === 'singbox');
		const awg = outbounds.filter((o) => o.kind === 'awg');
		return { direct, sb, awg };
	});
</script>

<section class="card">
	<h2 class="section-title">Настройки прокси-сервера</h2>
	<p class="section-desc">Эти значения сохраняются в конфигурации и применяются при каждом запуске sing-box.</p>

	<div class="settings-stack">
		<div class="setting-row">
			<div class="flex flex-col gap-1">
				<span class="font-medium">Прокси-сервер</span>
				<span class="setting-description">SOCKS5 / HTTP для LAN-устройств</span>
			</div>
			<Toggle checked={draft.enabled} onchange={(v) => (draft.enabled = v)} />
		</div>

		<div class="setting-row">
			<div class="flex flex-col gap-1">
				<span class="font-medium">Порт</span>
				<span class="setting-description">Рекомендуем 1099 или выше</span>
			</div>
			<input type="number" min="1024" max="65535" bind:value={draft.port} class="num-input" />
		</div>

		<div class="setting-row">
			<div class="flex flex-col gap-1">
				<span class="font-medium">Доступен на</span>
				<span class="setting-description">Все интерфейсы или конкретный мост</span>
			</div>
			<select
				class="select"
				value={listenChoice}
				onchange={(e) => setListenChoice((e.target as HTMLSelectElement).value)}
			>
				<option value="__all">Всех интерфейсах роутера</option>
				{#each bridgeInterfaces as br (br.id)}
					<option value={br.id}>{br.label}</option>
				{/each}
			</select>
		</div>

		<div class="setting-row">
			<div class="flex flex-col gap-1">
				<span class="font-medium">По умолчанию направлять в</span>
				<span class="setting-description">Применяется при запуске sing-box</span>
			</div>
			<select class="select" bind:value={draft.selectedOutbound}>
				{#each grouped.direct as ob (ob.tag)}
					<option value={ob.tag}>{ob.label}</option>
				{/each}
				{#if grouped.sb.length > 0}
					<optgroup label="Sing-box туннели">
						{#each grouped.sb as ob (ob.tag)}
							<option value={ob.tag}>{ob.label}</option>
						{/each}
					</optgroup>
				{/if}
				{#if grouped.awg.length > 0}
					<optgroup label="Туннели">
						{#each grouped.awg as ob (ob.tag)}
							<option value={ob.tag}>{ob.label} · {ob.detail}</option>
						{/each}
					</optgroup>
				{/if}
			</select>
		</div>

		<div class="setting-row">
			<div class="flex flex-col gap-1">
				<span class="font-medium">Защита паролем</span>
				<span class="setting-description">Требовать логин и пароль при подключении</span>
			</div>
			<Toggle checked={draft.auth.enabled} onchange={(v) => (draft.auth.enabled = v)} />
		</div>

		{#if draft.auth.enabled}
			<div class="setting-row">
				<div class="flex flex-col gap-1">
					<span class="font-medium">Имя пользователя</span>
				</div>
				<input type="text" bind:value={draft.auth.username} class="text-input" />
			</div>
			<div class="setting-row">
				<div class="flex flex-col gap-1">
					<span class="font-medium">Пароль</span>
				</div>
				<div class="pw-group">
					<input type="text" bind:value={draft.auth.password} class="text-input" />
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
	.section-title { font-size: 1rem; font-weight: 600; margin: 0 0 0.25rem 0; }
	.section-desc { font-size: 0.8125rem; color: var(--text-muted); margin: 0 0 0.75rem 0; }
	.num-input, .text-input, .select {
		padding: 0.4rem 0.6rem;
		background: var(--bg-tertiary);
		border: 1px solid var(--border);
		border-radius: 4px;
		color: var(--text-primary);
		font-size: 0.8125rem;
	}
	.num-input { width: 120px; }
	.text-input { min-width: 200px; }
	.select { min-width: 240px; }
	.pw-group { display: flex; gap: 0.5rem; align-items: center; }
	.form-actions {
		display: flex;
		justify-content: flex-end;
		gap: 0.5rem;
		margin-top: 1rem;
		padding-top: 0.875rem;
		border-top: 1px solid var(--border);
	}
</style>
