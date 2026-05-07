<script lang="ts">
	import Modal from '$lib/components/ui/Modal.svelte';
	import { Dropdown, type DropdownOption } from '$lib/components/ui';
	import type { SingboxRouterRule } from '$lib/types';
	import type { OutboundGroup } from './outboundOptions';

	interface Props {
		rule?: SingboxRouterRule;
		outboundOptions: OutboundGroup[];
		onClose: () => void;
		onSave: (rule: SingboxRouterRule) => Promise<void> | void;
	}
	let { rule, outboundOptions, onClose, onSave }: Props = $props();

	const outboundDropdownOptions = $derived<DropdownOption[]>([
		{ value: '', label: '— выберите —' },
		...outboundOptions.flatMap((g) =>
			g.items.map((i) => ({ value: i.value, label: i.label, group: g.group })),
		),
	]);

	// svelte-ignore state_referenced_locally
	let domainSuffixStr = $state((rule?.domain_suffix ?? []).join('\n'));
	// svelte-ignore state_referenced_locally
	let ipCidrStr = $state((rule?.ip_cidr ?? []).join('\n'));
	// svelte-ignore state_referenced_locally
	let sourceIpCidrStr = $state((rule?.source_ip_cidr ?? []).join('\n'));
	// svelte-ignore state_referenced_locally
	let ruleSetStr = $state((rule?.rule_set ?? []).join(', '));
	// svelte-ignore state_referenced_locally
	let portStr = $state((rule?.port ?? []).join(', '));

	// svelte-ignore state_referenced_locally
	let action: 'route' | 'reject' = $state((rule?.action === 'reject' ? 'reject' : 'route'));
	// svelte-ignore state_referenced_locally
	let outbound = $state(rule?.outbound ?? '');

	let busy = $state(false);
	let error = $state('');

	const customDomainsCount = $derived(
		domainSuffixStr
			.split('\n')
			.map((s) => s.trim())
			.filter(Boolean).length,
	);

	const customIPsCount = $derived(
		ipCidrStr
			.split('\n')
			.map((s) => s.trim())
			.filter(Boolean).length,
	);

	const sourceIPsCount = $derived(
		sourceIpCidrStr
			.split('\n')
			.map((s) => s.trim())
			.filter(Boolean).length,
	);

	function parseLines(text: string): string[] {
		return text
			.split('\n')
			.map((s) => s.trim())
			.filter(Boolean);
	}

	async function save(): Promise<void> {
		busy = true;
		error = '';
		try {
			const domain_suffix = parseLines(domainSuffixStr);
			const ip_cidr = parseLines(ipCidrStr);
			const source_ip_cidr = parseLines(sourceIpCidrStr);
			const rule_set = ruleSetStr.split(',').map((s) => s.trim()).filter(Boolean);
			const port = portStr
				.split(',')
				.map((s) => parseInt(s.trim(), 10))
				.filter((n) => !isNaN(n));

			const hasMatcher =
				domain_suffix.length > 0 ||
				ip_cidr.length > 0 ||
				source_ip_cidr.length > 0 ||
				rule_set.length > 0 ||
				port.length > 0;
			if (!hasMatcher) {
				error = 'Нужен хотя бы один matcher';
				busy = false;
				return;
			}
			if (action === 'route' && !outbound) {
				error = 'Выберите outbound для действия "Направить"';
				busy = false;
				return;
			}

			const built: SingboxRouterRule = {
				domain_suffix: domain_suffix.length ? domain_suffix : undefined,
				ip_cidr: ip_cidr.length ? ip_cidr : undefined,
				source_ip_cidr: source_ip_cidr.length ? source_ip_cidr : undefined,
				rule_set: rule_set.length ? rule_set : undefined,
				port: port.length ? port : undefined,
				action,
				outbound: action === 'route' ? outbound : undefined,
			};

			await onSave(built);
		} catch (e) {
			error = (e as Error).message;
		} finally {
			busy = false;
		}
	}
</script>

<Modal open onclose={onClose} title={rule ? 'Редактировать правило' : 'Новое правило'}>
	<div class="form">
		<div class="section-label">Готовые наборы и кастомные блоки</div>

		<label class="field">
			<div class="lbl">Готовые списки / rule set</div>
			<input bind:value={ruleSetStr} placeholder="geosite-youtube, geosite-discord, geosite-openai" />
			<div class="hint">
				Здесь указываются уже существующие наборы. Для своих доменов и подсетей используйте кастомные блоки ниже.
			</div>
		</label>

		<div class="block">
			<div class="block-head">
				<div class="lbl strong">Кастомный блок доменов</div>
				{#if customDomainsCount > 0}
					<span class="count-chip">{customDomainsCount} доменов</span>
				{/if}
			</div>
			<textarea
				bind:value={domainSuffixStr}
				rows="6"
				placeholder="youtube.com&#10;ytimg.com&#10;googlevideo.com&#10;.openai.com"
			></textarea>
			<div class="hint">
				По одному домену на строке. Это ваш локальный список, аналогично кастомным блокам в AWG-маршрутизации.
			</div>
		</div>

		<div class="block">
			<div class="block-head">
				<div class="lbl strong">Кастомный блок IP / подсетей</div>
				{#if customIPsCount > 0}
					<span class="count-chip">{customIPsCount} подсетей</span>
				{/if}
			</div>
			<textarea bind:value={ipCidrStr} rows="4" placeholder="142.250.0.0/15&#10;104.16.0.0/12"></textarea>
			<div class="hint">
				По одной сети на строке, в формате IP или CIDR. Полезно, когда нужно дописать свои адресные блоки вручную.
			</div>
		</div>

		<div class="block">
			<div class="block-head">
				<div class="lbl strong">Источник трафика по IP</div>
				{#if sourceIPsCount > 0}
					<span class="count-chip">{sourceIPsCount} адресов</span>
				{/if}
			</div>
			<textarea bind:value={sourceIpCidrStr} rows="3" placeholder="192.168.1.50&#10;192.168.1.0/24"></textarea>
			<div class="hint">
				Необязательный блок. Нужен, если правило должно срабатывать только для конкретных клиентов или подсетей LAN.
			</div>
		</div>

		<label class="field">
			<div class="lbl">Порты (через запятую)</div>
			<input bind:value={portStr} placeholder="443, 80" />
			<div class="hint">
				Необязательно. Можно дополнительно ограничить правило конкретными портами.
			</div>
		</label>

		<div class="action-section">
			<div class="section-label">Действие</div>
			<div class="segment">
				<button class:active={action === 'route'} onclick={() => (action = 'route')} type="button">Направить</button>
				<button class:active={action === 'reject'} onclick={() => (action = 'reject')} type="button">Заблокировать</button>
			</div>

			{#if action === 'route'}
				<label class="field">
					<div class="lbl">Куда направить</div>
					<Dropdown bind:value={outbound} options={outboundDropdownOptions} fullWidth />
				</label>
			{/if}
		</div>

		{#if error}<div class="error">{error}</div>{/if}

		<div class="actions">
			<button class="btn btn-secondary" onclick={onClose} type="button">Отмена</button>
			<button class="btn btn-primary" onclick={save} disabled={busy} type="button">Сохранить</button>
		</div>
	</div>
</Modal>

<style>
	.form {
		display: grid;
		gap: 0.6rem;
		min-width: 0;
	}
	.section-label {
		font-size: 0.7rem;
		text-transform: uppercase;
		letter-spacing: 0.5px;
		color: var(--muted-text);
		margin-bottom: 0.25rem;
	}
	.field {
		display: grid;
		gap: 0.25rem;
	}
	.lbl {
		font-size: 0.75rem;
		color: var(--muted-text);
	}
	.lbl.strong {
		color: var(--text);
		font-weight: 600;
	}
	.field input {
		background: var(--bg);
		border: 1px solid var(--border);
		padding: 0.4rem 0.6rem;
		border-radius: 4px;
		color: var(--text);
		font-family: ui-monospace, monospace;
		font-size: 0.85rem;
		box-sizing: border-box;
		width: 100%;
		resize: vertical;
	}
	.block {
		display: grid;
		gap: 0.35rem;
		padding: 0.75rem;
		border: 1px solid var(--border);
		border-radius: 8px;
		background:
			linear-gradient(180deg, color-mix(in srgb, var(--bg) 88%, var(--accent, #3b82f6) 12%), var(--bg));
	}
	.block textarea {
		background: var(--bg);
		border: 1px solid var(--border);
		padding: 0.5rem 0.6rem;
		border-radius: 6px;
		color: var(--text);
		font-family: ui-monospace, monospace;
		font-size: 0.85rem;
		box-sizing: border-box;
		width: 100%;
		resize: vertical;
	}
	.block-head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.5rem;
	}
	.count-chip {
		font-size: 0.72rem;
		color: var(--muted-text);
		padding: 0.12rem 0.45rem;
		border: 1px solid var(--border);
		border-radius: 999px;
		background: var(--surface-bg);
		white-space: nowrap;
	}
	.hint {
		font-size: 0.75rem;
		color: var(--muted-text);
		line-height: 1.4;
	}
	.action-section {
		border-top: 1px solid var(--border);
		padding-top: 0.75rem;
		margin-top: 0.25rem;
		display: grid;
		gap: 0.5rem;
	}
	.segment {
		display: inline-flex;
		border: 1px solid var(--border);
		border-radius: 4px;
		overflow: hidden;
		width: fit-content;
	}
	.segment button {
		background: transparent;
		border: none;
		padding: 0.4rem 0.9rem;
		font-size: 0.85rem;
		cursor: pointer;
		color: var(--muted-text);
	}
	.segment button + button {
		border-left: 1px solid var(--border);
	}
	.segment button.active {
		background: var(--accent, #3b82f6);
		color: white;
		font-weight: 600;
	}
	.error {
		color: var(--danger, #dc2626);
		font-size: 0.85rem;
	}
	.actions {
		display: flex;
		justify-content: flex-end;
		gap: 0.5rem;
	}
</style>
