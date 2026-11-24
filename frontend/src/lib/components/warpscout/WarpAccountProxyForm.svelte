<script lang="ts">
	import { m } from '$lib/i18n';
	import { Input } from '$lib/components/ui';

	interface Props {
		proxyUrl: string;
		relayUrl: string;
		accountKeyInUseBy?: string;
	}

	let { proxyUrl = $bindable(), relayUrl = $bindable(), accountKeyInUseBy = '' }: Props = $props();
</script>

<p class="alt-hint">{m.warpscout_proxy_hint()}</p>
{#if accountKeyInUseBy}
	<p class="alt-warn">{m.warpscout_acct_key_in_use_warn({ name: accountKeyInUseBy })}</p>
{/if}
<div class="alt-field">
	<label class="field-label" for="warp-proxy-url">{m.warpscout_proxy_url_label()}</label>
	<Input
		id="warp-proxy-url"
		type="text"
		placeholder="socks5://127.0.0.1:1080"
		autocomplete="off"
		fullWidth
		bind:value={proxyUrl}
	/>
</div>
<div class="alt-field">
	<label class="field-label" for="warp-relay-url">{m.warpscout_relay_url_label()}</label>
	<Input
		id="warp-relay-url"
		type="text"
		placeholder="https://warp-relay.example.com"
		autocomplete="off"
		fullWidth
		bind:value={relayUrl}
	/>
</div>
<p class="alt-warn">{m.warpscout_relay_trust_warn()}</p>

<style>
	.alt-hint {
		margin: 0;
		font-size: 0.75rem;
		line-height: 1.4;
		color: var(--color-text-muted);
	}
	.alt-field {
		display: flex;
		flex-direction: column;
		gap: 0.3rem;
	}
	.field-label {
		font-size: 0.72rem;
		font-weight: 500;
		color: var(--color-text-secondary);
	}
	.alt-warn {
		margin: 0;
		padding: 0.45rem 0.6rem;
		border-radius: var(--radius-sm);
		font-size: 0.74rem;
		line-height: 1.4;
		color: var(--color-warning);
		background: var(--color-warning-tint);
		border: 1px solid var(--color-warning-border);
	}
</style>
