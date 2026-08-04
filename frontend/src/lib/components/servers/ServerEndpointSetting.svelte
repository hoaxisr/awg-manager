<script lang="ts">
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { servers } from '$lib/stores/servers';
	import {
		emptyEndpointDescription,
		emptyEndpointPlaceholder,
		isValidEndpointHost,
	} from '$lib/utils/endpoint';

	interface Props {
		serverId: string;
		endpoint?: string;
		listenPort: number;
		wanIP?: string;
		keenDnsDomain?: string;
		loadingWanIP?: boolean;
		disabled?: boolean;
	}

	let {
		serverId,
		endpoint = '',
		listenPort,
		wanIP = '',
		keenDnsDomain = '',
		loadingWanIP = false,
		disabled = false,
	}: Props = $props();

	let draft = $state('');
	let editing = $state(false);
	let saving = $state(false);

	const storedEndpoint = $derived(endpoint ?? '');
	const endpointDescription = $derived(emptyEndpointDescription(keenDnsDomain));
	const endpointPlaceholder = $derived(
		emptyEndpointPlaceholder(keenDnsDomain, wanIP, loadingWanIP),
	);

	$effect(() => {
		if (!editing) {
			draft = storedEndpoint;
		}
	});

	async function commitEndpoint() {
		if (saving || disabled) return;
		const next = draft.trim();
		if (next === storedEndpoint) {
			editing = false;
			return;
		}
		if (!isValidEndpointHost(next)) {
			notifications.error('Endpoint должен быть IP-адресом или доменным именем');
			draft = storedEndpoint;
			editing = false;
			return;
		}
		saving = true;
		try {
			const fresh = await api.setWireguardServerEndpoint(serverId, next);
			servers.applyMutationResponse(fresh);
			editing = false;
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка сохранения endpoint');
			draft = storedEndpoint;
		} finally {
			saving = false;
		}
	}

	function handleFocus() {
		editing = true;
	}

	function handleBlur() {
		void commitEndpoint();
	}

	function handleKeydown(e: KeyboardEvent) {
		if (e.key === 'Enter') {
			e.preventDefault();
			(e.currentTarget as HTMLInputElement).blur();
		}
		if (e.key === 'Escape') {
			draft = storedEndpoint;
			editing = false;
			(e.currentTarget as HTMLInputElement).blur();
		}
	}
</script>

<div class="setting-row">
	<div class="setting-copy">
		<span class="setting-title">Endpoint клиентов</span>
		<span class="setting-description" title={endpointDescription}>
			Хост для подключения в .conf
		</span>
	</div>
	<div class="setting-control">
		<input
			type="text"
			class="endpoint-input"
			class:saving
			bind:value={draft}
			placeholder={endpointPlaceholder}
			{disabled}
			onfocus={handleFocus}
			onblur={handleBlur}
			onkeydown={handleKeydown}
			autocomplete="off"
			spellcheck="false"
		/>
	</div>
</div>

<style>
	.endpoint-input {
		width: 100%;
		padding: 8px 12px;
		font-size: 13px;
		font-family: var(--font-mono, monospace);
		background: var(--color-settings-surface-bg, var(--bg-primary));
		border: 1px solid var(--border);
		border-radius: 6px;
		color: var(--text-primary);
		box-sizing: border-box;
	}

	.endpoint-input:focus {
		outline: none;
		border-color: var(--accent);
	}

	.endpoint-input:disabled,
	.endpoint-input.saving {
		opacity: 0.7;
	}
</style>
