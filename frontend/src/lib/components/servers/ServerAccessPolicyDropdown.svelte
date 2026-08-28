<script lang="ts">
	import type { Snippet } from 'svelte';
	import { onMount } from 'svelte';
	import { api } from '$lib/api/client';
	import { Dropdown, type DropdownOption } from '$lib/components/ui';
	import { isStandardAccessPolicyName } from '$lib/utils/accessPolicy';

	interface Props {
		policy: string;
		disabled?: boolean;
		onchange: (policy: string) => void | Promise<void>;
		extra?: Snippet;
		/**
		 * Без собственной подписи: строка формы уже дала метку и подсказку.
		 * Заведено для детали «Прокси», где все поля живут в одной сетке
		 * «метка — контрол»; на странице «Серверы» подпись остаётся своя.
		 */
		labelless?: boolean;
	}

	let { policy, disabled = false, onchange, extra, labelless = false }: Props = $props();

	let policies = $state<{ id: string; description: string }[]>([]);
	let selectedPolicy = $state('');

	$effect(() => {
		selectedPolicy = policy;
	});

	let orphanedPolicy = $derived.by(() => {
		const p = policy;
		if (!p || p === 'none' || p === 'permit' || p === 'deny') return null;
		if (policies.some((o) => o.id === p)) return null;
		return p;
	});

	let standardPolicies = $derived(policies.filter((p) => isStandardAccessPolicyName(p.id)));

	let policyOptions = $derived<DropdownOption[]>([
		{ value: 'none', label: 'Политика по умолчанию' },
		...(orphanedPolicy ? [{ value: orphanedPolicy, label: `${orphanedPolicy} (отсутствует)` }] : []),
		...standardPolicies.map((p) => ({
			value: p.id,
			label: p.description ? `${p.id} — ${p.description}` : p.id,
		})),
	]);

	onMount(async () => {
		try {
			policies = await api.getManagedServerPolicies();
		} catch {
			policies = [];
		}
	});
</script>

{#if labelless}
	<Dropdown value={selectedPolicy} options={policyOptions} {disabled} {onchange} fullWidth />
	{#if extra}{@render extra()}{/if}
{:else}
	<div class="setting-row">
		<div class="setting-copy">
			<span class="setting-title">Политика доступа</span>
			<span class="setting-description"
				>Регулирует выход в интернет для клиентов сервера. Применяется ко всем клиентам этого сервера.</span
			>
			{#if extra}{@render extra()}{/if}
		</div>
		<div class="setting-control">
			<Dropdown value={selectedPolicy} options={policyOptions} {disabled} {onchange} fullWidth />
		</div>
	</div>
{/if}
