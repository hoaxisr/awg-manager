<script lang="ts">
	import Modal from '$lib/components/ui/Modal.svelte';
	import type { SingboxRouterRule } from '$lib/types';
	import type { OutboundGroup } from './outboundOptions';

	interface Props {
		rule?: SingboxRouterRule;
		outboundOptions: OutboundGroup[];
		onClose: () => void;
		onSave: (rule: SingboxRouterRule) => Promise<void> | void;
	}
	let { rule, outboundOptions, onClose, onSave }: Props = $props();

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

	async function save(): Promise<void> {
		busy = true;
		error = '';
		try {
			const domain_suffix = domainSuffixStr.split('\n').map((s) => s.trim()).filter(Boolean);
			const ip_cidr = ipCidrStr.split('\n').map((s) => s.trim()).filter(Boolean);
			const source_ip_cidr = sourceIpCidrStr.split('\n').map((s) => s.trim()).filter(Boolean);
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
		<div class="section-label">Matchers (минимум один)</div>

		<label class="field">
			<div class="lbl">Domain suffix</div>
			<textarea bind:value={domainSuffixStr} rows="3" placeholder="по одному на строке, например youtube.com"></textarea>
		</label>

		<label class="field">
			<div class="lbl">IP CIDR</div>
			<textarea bind:value={ipCidrStr} rows="2" placeholder="142.250.0.0/15"></textarea>
		</label>

		<label class="field">
			<div class="lbl">Source IP CIDR</div>
			<textarea bind:value={sourceIpCidrStr} rows="2" placeholder="192.168.1.50"></textarea>
		</label>

		<label class="field">
			<div class="lbl">Rule sets (через запятую)</div>
			<input bind:value={ruleSetStr} placeholder="geosite-youtube, geoip-ru" />
		</label>

		<label class="field">
			<div class="lbl">Порты (через запятую)</div>
			<input bind:value={portStr} placeholder="443, 80" />
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
					<select bind:value={outbound}>
						<option value="">— выберите —</option>
						{#each outboundOptions as group}
							<optgroup label={group.group}>
								{#each group.items as item}
									<option value={item.value}>{item.label}</option>
								{/each}
							</optgroup>
						{/each}
					</select>
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
		min-width: 420px;
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
	.field textarea,
	.field input,
	.field select {
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
