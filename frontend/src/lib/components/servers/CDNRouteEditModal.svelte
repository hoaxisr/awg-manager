<script lang="ts" module>
	/** Код ошибки домена; текст выбирается при показе. */
	export type DomainError = '' | 'empty' | 'fqdn' | 'format' | 'duplicate';
</script>

<script lang="ts">
	// Добавление/правка узла диспетчера входящих подключений.
	import { m } from '$lib/i18n';
	import type { CDNRouteRule } from '$lib/types';
	import { Button, Modal } from '$lib/components/ui';

	interface Props {
		open: boolean;
		isNew: boolean;
		route: CDNRouteRule;
		domainError: DomainError;
		xrayPathPrefix: string;
		onsave: () => void;
	}

	let {
		open = $bindable(false),
		isNew,
		route = $bindable(),
		domainError = $bindable(''),
		xrayPathPrefix,
		onsave,
	}: Props = $props();

	const errorText = $derived.by(() => {
		if (domainError === 'empty') return m.servers_cdn_domain_error_empty();
		if (domainError === 'fqdn') return m.servers_cdn_domain_error_fqdn();
		if (domainError === 'format') return m.servers_cdn_domain_error_format();
		if (domainError === 'duplicate') return m.servers_cdn_domain_error_duplicate();
		return '';
	});
</script>

<Modal
	bind:open
	title={isNew ? m.servers_cdn_modal_add_title() : m.servers_cdn_modal_edit_title()}
	onclose={() => open = false}
	size="md"
>
	<div class="modal-body">
		<div class="form-group">
			<label for="route-domain" class="form-label">{m.servers_cdn_field_domain()} <span class="text-danger">*</span></label>
			<input
				id="route-domain"
				type="text"
				class="cdn-input font-mono"
				placeholder={m.servers_cdn_field_domain_placeholder()}
				bind:value={route.domain}
				oninput={() => domainError = ''}
			/>
			{#if errorText}
				<span class="form-error">{errorText}</span>
			{/if}
			<span class="hint">{m.servers_cdn_field_domain_hint()}</span>
		</div>

		<div class="form-group">
			<label for="route-description" class="form-label">{m.servers_cdn_field_description()}</label>
			<input
				id="route-description"
				type="text"
				class="cdn-input"
				placeholder={m.servers_cdn_field_description_placeholder()}
				bind:value={route.description}
			/>
		</div>

		<div class="form-group">
			<span class="form-label">{m.servers_cdn_field_services()}</span>
			<label class="checkbox-label">
				<input type="checkbox" bind:checked={route.route_xray} />
				<span>{m.servers_cdn_proxy_word()} <strong>VLESS (XHTTP)</strong> <small class="text-xs text-muted">({xrayPathPrefix}/*)</small></span>
			</label>
			<label class="checkbox-label">
				<input type="checkbox" bind:checked={route.route_telegram} />
				<span>{m.servers_cdn_proxy_word()} <strong>{m.servers_cdn_tg_proxy()}</strong> <small class="text-xs text-muted">{m.servers_cdn_tg_paths_hint()}</small></span>
			</label>
		</div>

		<div class="form-group">
			<label class="checkbox-label">
				<input type="checkbox" bind:checked={route.enabled} />
				<span>{m.servers_cdn_field_enabled()}</span>
			</label>
		</div>
	</div>

	{#snippet actions()}
		<Button variant="secondary" onclick={() => open = false}>{m.common_cancel()}</Button>
		<Button variant="primary" onclick={onsave}>
			{isNew ? m.common_add() : m.common_apply()}
		</Button>
	{/snippet}
</Modal>

<style>
	.modal-body {
		display: flex;
		flex-direction: column;
		gap: 1rem;
		padding: 0.5rem 0;
	}

	.form-group {
		display: flex;
		flex-direction: column;
		gap: 0.35rem;
	}

	.form-label {
		font-size: 0.875rem;
		font-weight: 500;
		color: var(--color-text-primary);
	}

	.cdn-input {
		width: 100%;
		box-sizing: border-box;
		padding: 0.5rem 0.75rem;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		background: var(--color-bg-primary);
		color: var(--color-text-primary);
		font-size: 0.875rem;
	}

	.cdn-input:focus {
		outline: none;
		border-color: var(--color-accent);
	}

	.checkbox-label {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		font-size: 0.875rem;
		color: var(--color-text-primary);
		cursor: pointer;
	}

	.form-error {
		font-size: 0.75rem;
		color: var(--color-error);
	}

	.hint {
		font-size: 0.75rem;
		color: var(--color-text-muted);
	}

	.text-muted { color: var(--color-text-muted); }
	.text-danger { color: var(--color-error); }
</style>
