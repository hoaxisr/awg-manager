<script lang="ts">
	import { Button, Dropdown, type DropdownOption } from '$lib/components/ui';
	import SingboxSettingsModal from './SingboxSettingsModal.svelte';
	import type { SingboxRouterDNSServer, SingboxRouterDNSStrategy } from '$lib/types';

	interface Props {
		servers: SingboxRouterDNSServer[];
		final: string;
		strategy: SingboxRouterDNSStrategy;
		timeout?: string;
		onClose: () => void;
		onSave: (globals: {
			final: string;
			strategy: SingboxRouterDNSStrategy;
			timeout: string;
		}) => Promise<void> | void;
	}

	let { servers, final, strategy, timeout = '', onClose, onSave }: Props = $props();

	const STRATEGY_OPTIONS: DropdownOption<SingboxRouterDNSStrategy>[] = [
		{ value: '', label: '— default —' },
		{ value: 'ipv4_only', label: 'ipv4_only' },
		{ value: 'ipv6_only', label: 'ipv6_only' },
		{ value: 'prefer_ipv4', label: 'prefer_ipv4' },
		{ value: 'prefer_ipv6', label: 'prefer_ipv6' },
	];

	const TIMEOUT_OPTIONS: DropdownOption[] = [
		{ value: '', label: '— по умолчанию (10 с) —' },
		{ value: '3s', label: '3 с' },
		{ value: '5s', label: '5 с' },
		{ value: '10s', label: '10 с' },
		{ value: '15s', label: '15 с' },
		{ value: '30s', label: '30 с' },
	];

	const finalOptions = $derived<DropdownOption[]>([
		{ value: '', label: '— не задан —' },
		...servers.map((s) => ({ value: s.tag, label: s.tag })),
	]);

	// svelte-ignore state_referenced_locally
	let draftFinal = $state(final);
	// svelte-ignore state_referenced_locally
	let draftStrategy = $state<SingboxRouterDNSStrategy>(strategy);
	// svelte-ignore state_referenced_locally
	let draftTimeout = $state(timeout);

	let initialFinal = $state('');
	let initialStrategy = $state<SingboxRouterDNSStrategy>('');
	let initialTimeout = $state('');

	$effect(() => {
		initialFinal = final;
		initialStrategy = strategy;
		initialTimeout = timeout;
		draftFinal = final;
		draftStrategy = strategy;
		draftTimeout = timeout;
	});

	const isDirty = $derived(
		draftFinal !== initialFinal || draftStrategy !== initialStrategy || draftTimeout !== initialTimeout
	);

	let busy = $state(false);
	let error = $state('');

	async function save(): Promise<void> {
		if (!isDirty || busy) return;
		busy = true;
		error = '';
		try {
			await onSave({ final: draftFinal, strategy: draftStrategy, timeout: draftTimeout });
		} catch (e) {
			error = (e as Error).message;
		} finally {
			busy = false;
		}
	}
</script>

<SingboxSettingsModal
	title="DNS по умолчанию"
	onClose={onClose}
	size="md"
	hasUnsavedChanges={() => isDirty}
>
	<div class="form">
		<label class="field">
			<div class="lbl">Final-сервер</div>
			<Dropdown
				bind:value={draftFinal}
				options={finalOptions}
				disabled={servers.length === 0}
				fullWidth
			/>
			<div class="hint">
				Сервер по умолчанию для запросов, не попавших ни под одно правило —
				простой запасной путь (рекомендуется). Отдельное catch-all правило
				(без условий) делает то же, но как явное последнее правило; если есть
				и то и другое, правило важнее (проверяется раньше final).
			</div>
		</label>

		<label class="field">
			<div class="lbl">Стратегия</div>
			<Dropdown bind:value={draftStrategy} options={STRATEGY_OPTIONS} fullWidth />
			<div class="hint">Для роутера без IPv6 обычно prefer_ipv4 или ipv4_only.</div>
		</label>

		<label class="field">
			<div class="lbl">Таймаут запроса</div>
			<Dropdown bind:value={draftTimeout} options={TIMEOUT_OPTIONS} fullWidth />
			<div class="hint">
				Сколько ждать ответ одного DNS-сервера. Короче — быстрее переход к
				следующему в цепочке, но чаще ложные отказы на медленном канале.
			</div>
		</label>

		{#if error}<div class="error">{error}</div>{/if}
	</div>

	{#snippet actions()}
		<Button variant="ghost" size="md" onclick={onClose} type="button">Отмена</Button>
		<Button variant="primary" size="md" onclick={save} disabled={busy || !isDirty} loading={busy} type="button">
			Сохранить
		</Button>
	{/snippet}
</SingboxSettingsModal>
