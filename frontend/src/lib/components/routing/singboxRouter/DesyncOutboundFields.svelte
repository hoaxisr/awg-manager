<script lang="ts">
	import { m } from '$lib/i18n';
	import { Dropdown, Toggle, type DropdownOption } from '$lib/components/ui';
	import { DESYNC_FOOLING, DESYNC_MODES, DESYNC_PRESETS, DESYNC_UDP_MODES, type DesyncDraft } from './desyncDraft';

	interface Props {
		draft: DesyncDraft;
	}
	let { draft = $bindable() }: Props = $props();

	// Пустое значение = ключ не пишется, действует пресет.
	function opts(values: string[]): DropdownOption[] {
		return [{ value: '', label: m.routing_desync_by_preset() }, ...values.map((v) => ({ value: v, label: v }))];
	}
	const presetOptions = opts(DESYNC_PRESETS);
	const modeOptions = opts(DESYNC_MODES);
	const foolingOptions = opts(DESYNC_FOOLING);
	const udpModeOptions = opts(DESYNC_UDP_MODES);
	const autoTuneOptions: DropdownOption[] = [
		{ value: '', label: m.routing_desync_by_preset() },
		{ value: 'on', label: m.routing_desync_on() },
		{ value: 'off', label: m.routing_desync_off() },
	];
</script>

<div class="field">
	<div class="lbl">preset</div>
	<Dropdown bind:value={draft.preset} options={presetOptions} fullWidth />
	<div class="type-hint">{m.routing_desync_preset_hint()}</div>
</div>

<div class="form-section form-section-divided"><div class="section-label">TCP</div></div>
<div class="row2">
	<div class="field">
		<div class="lbl">mode</div>
		<Dropdown bind:value={draft.mode} options={modeOptions} fullWidth />
	</div>
	<label class="field">
		<div class="lbl">split_pos</div>
		<input bind:value={draft.split_pos} placeholder="midsld" />
	</label>
</div>
<div class="row2">
	<label class="field">
		<div class="lbl">fake_sni</div>
		<input bind:value={draft.fake_sni} placeholder="example.org" />
	</label>
	<label class="field">
		<div class="lbl">fake_host</div>
		<input bind:value={draft.fake_host} placeholder="example.org" />
	</label>
</div>
<label class="field">
	<div class="lbl">fake_sni_pool</div>
	<input bind:value={draft.fake_sni_pool} placeholder="a.example, b.example" />
	<div class="type-hint">{m.routing_desync_pool_hint()}</div>
</label>
<div class="row3">
	<label class="field">
		<div class="lbl">ttl</div>
		<input bind:value={draft.ttl} inputmode="numeric" placeholder="3" />
	</label>
	<label class="field">
		<div class="lbl">repeats</div>
		<input bind:value={draft.repeats} inputmode="numeric" placeholder="1" />
	</label>
	<div class="field">
		<div class="lbl">fooling</div>
		<Dropdown bind:value={draft.fooling} options={foolingOptions} fullWidth />
	</div>
</div>
<div class="row2">
	<Toggle checked={draft.autottl} onchange={(v) => (draft.autottl = v)} label="autottl" hint={m.routing_desync_autottl_hint()} size="sm" />
	<Toggle checked={draft.tlsrec_first} onchange={(v) => (draft.tlsrec_first = v)} label="tlsrec_first" size="sm" />
</div>

<div class="form-section form-section-divided"><div class="section-label">UDP / QUIC</div></div>
<div class="row3">
	<div class="field">
		<div class="lbl">udp_mode</div>
		<Dropdown bind:value={draft.udp_mode} options={udpModeOptions} fullWidth />
	</div>
	<label class="field">
		<div class="lbl">udp_ttl</div>
		<input bind:value={draft.udp_ttl} inputmode="numeric" placeholder="3" />
	</label>
	<label class="field">
		<div class="lbl">udp_repeats</div>
		<input bind:value={draft.udp_repeats} inputmode="numeric" placeholder="1" />
	</label>
</div>

<div class="form-section form-section-divided"><div class="section-label">Adaptive</div></div>
<div class="field">
	<div class="lbl">auto_tune</div>
	<Dropdown bind:value={draft.auto_tune} options={autoTuneOptions} fullWidth />
	<div class="type-hint">{m.routing_desync_adaptive_hint()}</div>
</div>
<div class="row3">
	<label class="field">
		<div class="lbl">max_attempts</div>
		<input bind:value={draft.max_attempts} inputmode="numeric" placeholder="3" />
	</label>
	<label class="field">
		<div class="lbl">max_concurrent</div>
		<input bind:value={draft.max_concurrent} inputmode="numeric" placeholder="2" />
	</label>
	<label class="field">
		<div class="lbl">max_entries</div>
		<input bind:value={draft.max_entries} inputmode="numeric" placeholder="1024" />
	</label>
</div>
<div class="row2">
	<label class="field">
		<div class="lbl">ttl</div>
		<input bind:value={draft.adaptive_ttl} placeholder="24h" />
	</label>
	<label class="field">
		<div class="lbl">timeout</div>
		<input bind:value={draft.adaptive_timeout} placeholder="10s" />
	</label>
</div>

<label class="field">
	<div class="lbl">strategy</div>
	<input bind:value={draft.strategy} placeholder="seqovl=1 ipfrag=24" />
	<div class="type-hint">{m.routing_desync_strategy_hint()}</div>
</label>

<style>
	.row3 {
		display: grid;
		grid-template-columns: repeat(3, minmax(0, 1fr));
		gap: 0.5rem;
	}

	@media (max-width: 560px) {
		.row3 {
			grid-template-columns: 1fr;
		}
	}
</style>
