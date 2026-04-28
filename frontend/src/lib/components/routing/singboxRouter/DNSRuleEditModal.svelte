<script lang="ts">
	import Modal from '$lib/components/ui/Modal.svelte';
	import type { SingboxRouterDNSRule, SingboxRouterDNSServer } from '$lib/types';

	interface Props {
		rule?: SingboxRouterDNSRule;
		servers: SingboxRouterDNSServer[];
		onClose: () => void;
		onSave: (rule: SingboxRouterDNSRule) => Promise<void> | void;
	}
	let { rule, servers, onClose, onSave }: Props = $props();

	// svelte-ignore state_referenced_locally
	let ruleSetStr = $state((rule?.rule_set ?? []).join(', '));
	// svelte-ignore state_referenced_locally
	let domainSuffixStr = $state((rule?.domain_suffix ?? []).join('\n'));
	// svelte-ignore state_referenced_locally
	let domainStr = $state((rule?.domain ?? []).join('\n'));
	// svelte-ignore state_referenced_locally
	let domainKeywordStr = $state((rule?.domain_keyword ?? []).join(', '));
	// svelte-ignore state_referenced_locally
	let queryTypeStr = $state((rule?.query_type ?? []).join(', '));
	// svelte-ignore state_referenced_locally
	let action: 'route' | 'reject' = $state(rule?.action === 'reject' ? 'reject' : 'route');
	// svelte-ignore state_referenced_locally
	let server = $state(rule?.server ?? '');

	let busy = $state(false);
	let error = $state('');

	async function save(): Promise<void> {
		busy = true;
		error = '';
		try {
			const rule_set = ruleSetStr.split(',').map((s) => s.trim()).filter(Boolean);
			const domain_suffix = domainSuffixStr.split('\n').map((s) => s.trim()).filter(Boolean);
			const domain = domainStr.split('\n').map((s) => s.trim()).filter(Boolean);
			const domain_keyword = domainKeywordStr.split(',').map((s) => s.trim()).filter(Boolean);
			const query_type = queryTypeStr.split(',').map((s) => s.trim().toUpperCase()).filter(Boolean);

			const hasMatcher =
				rule_set.length > 0 ||
				domain_suffix.length > 0 ||
				domain.length > 0 ||
				domain_keyword.length > 0 ||
				query_type.length > 0;
			if (!hasMatcher) {
				error = 'Нужен хотя бы один matcher';
				busy = false;
				return;
			}
			if (action === 'route' && !server) {
				error = 'Выберите DNS сервер';
				busy = false;
				return;
			}

			const built: SingboxRouterDNSRule = {
				rule_set: rule_set.length ? rule_set : undefined,
				domain_suffix: domain_suffix.length ? domain_suffix : undefined,
				domain: domain.length ? domain : undefined,
				domain_keyword: domain_keyword.length ? domain_keyword : undefined,
				query_type: query_type.length ? query_type : undefined,
				action: action === 'reject' ? 'reject' : undefined,
				server: action === 'route' ? server : undefined,
			};

			await onSave(built);
		} catch (e) {
			error = (e as Error).message;
		} finally {
			busy = false;
		}
	}
</script>

<Modal open onclose={onClose} title={rule ? 'Редактировать DNS правило' : 'Новое DNS правило'}>
	<div class="form">
		<div class="section-label">Matchers (минимум один)</div>

		<label class="field">
			<div class="lbl">Rule sets (через запятую)</div>
			<input bind:value={ruleSetStr} placeholder="geosite-youtube, geoip-ru" />
		</label>

		<label class="field">
			<div class="lbl">Domain suffix</div>
			<textarea bind:value={domainSuffixStr} rows="3" placeholder="по одному на строке: .youtube.com"></textarea>
		</label>

		<label class="field">
			<div class="lbl">Domain (точное совпадение)</div>
			<textarea bind:value={domainStr} rows="2" placeholder="example.com"></textarea>
		</label>

		<label class="field">
			<div class="lbl">Domain keyword (через запятую)</div>
			<input bind:value={domainKeywordStr} placeholder="tracker, analytics" />
		</label>

		<label class="field">
			<div class="lbl">Query type (через запятую)</div>
			<input bind:value={queryTypeStr} placeholder="A, AAAA, HTTPS" />
		</label>

		<div class="action-section">
			<div class="section-label">Действие</div>
			<div class="segment">
				<button class:active={action === 'route'} onclick={() => (action = 'route')} type="button">Резолвить</button>
				<button class:active={action === 'reject'} onclick={() => (action = 'reject')} type="button">Заблокировать</button>
			</div>

			{#if action === 'route'}
				<label class="field">
					<div class="lbl">DNS сервер</div>
					<select bind:value={server}>
						<option value="">— выберите —</option>
						{#each servers as s}
							<option value={s.tag}>{s.tag} ({s.type} · {s.server})</option>
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
