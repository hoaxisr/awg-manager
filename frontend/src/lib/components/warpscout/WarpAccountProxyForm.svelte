<script lang="ts">
	import { m } from '$lib/i18n';
	import { Input } from '$lib/components/ui';

	interface Props {
		proxyUrl: string;
		relayUrl: string;
		accountKeyInUseBy?: string;
		accountKeyCheckError?: string;
	}

	let {
		proxyUrl = $bindable(),
		relayUrl = $bindable(),
		accountKeyInUseBy = '',
		accountKeyCheckError = '',
	}: Props = $props();
</script>

<div class="field-hint">{m.warpscout_proxy_hint()}</div>
{#if accountKeyInUseBy.trim()}
	<p class="field-callout-warn">{m.warpscout_acct_key_in_use_warn({ name: accountKeyInUseBy })}</p>
{:else if accountKeyCheckError.trim()}
	<p class="field-callout-warn">{m.warpscout_acct_key_check_err({ err: accountKeyCheckError })}</p>
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
<p class="field-callout-warn">{m.warpscout_relay_trust_warn()}</p>

<style>
	.alt-field {
		display: flex;
		flex-direction: column;
		gap: 0.3rem;
	}
</style>
