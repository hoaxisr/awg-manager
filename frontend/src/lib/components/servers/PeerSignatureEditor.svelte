<script lang="ts">
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { Button, Dropdown, type DropdownOption } from '$lib/components/ui';
	import { protocols, calcTotalSize, MAX_SIGNATURE_BYTES, type ProtocolKey, type SignaturePackets } from '$lib/utils/protocols';

	const FIELDS = ['i1', 'i2', 'i3', 'i4', 'i5'] as const;

	type ProfileValue = ProtocolKey | '';

	interface Props {
		profile: ProfileValue;
		packets: SignaturePackets;
		onchange: (next: { profile: ProfileValue; packets: SignaturePackets }) => void;
	}

	let { profile, packets, onchange }: Props = $props();

	let generating = $state(false);

	const options = $derived<DropdownOption<ProfileValue>[]>([
		// Пустой профиль показываем, только пока он выбран: сигнатура пришла
		// от старого сервера или набрана руками — профиля у неё нет.
		...(profile === '' ? [{ value: '' as ProfileValue, label: '— не задан —' }] : []),
		...Object.entries(protocols).map(([key, proto]) => ({
			value: key as ProfileValue,
			label: proto.name,
			description: proto.description,
		})),
	]);

	const totalBytes = $derived(calcTotalSize(packets));
	const overLimit = $derived(totalBytes > MAX_SIGNATURE_BYTES);

	async function handleGenerate() {
		const target: ProtocolKey = profile === '' ? 'quic_initial' : profile;
		generating = true;
		try {
			const res = await api.generateSignature(target);
			onchange({
				profile: target,
				packets: { i1: res.packets.i1, i2: res.packets.i2, i3: res.packets.i3, i4: res.packets.i4, i5: res.packets.i5 },
			});
			notifications.success(`Сигнатура сгенерирована (${protocols[target].name})`);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка генерации');
		} finally {
			generating = false;
		}
	}
</script>

<section class="peer-signature">
	<span class="section-title">Сигнатура</span>
	<p class="section-desc">
		Пакеты-имитации перед рукопожатием. У каждого клиента своя сигнатура, сервер её не проверяет.
	</p>

	<div class="generate-row">
		<div class="profile-select">
			<Dropdown
				value={profile}
				{options}
				fullWidth
				onchange={(v) => onchange({ profile: v, packets })}
			/>
		</div>
		<Button variant="secondary" size="sm" onclick={handleGenerate} disabled={generating} loading={generating}>
			Сгенерировать
		</Button>
	</div>

	<div class="signature-fields">
		{#each FIELDS as field (field)}
			<div class="form-group">
				<label class="label" for={`peer-sig-${field}`}>{field.toUpperCase()}</label>
				<input
					type="text"
					id={`peer-sig-${field}`}
					class="input"
					value={packets[field]}
					oninput={(e) => onchange({ profile, packets: { ...packets, [field]: e.currentTarget.value } })}
				/>
			</div>
		{/each}
	</div>

	<div class="size-indicator" class:over-limit={overLimit}>
		{totalBytes} / {MAX_SIGNATURE_BYTES} байт
	</div>
</section>

<style>
	.peer-signature {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
		padding-top: 0.75rem;
		border-top: 1px solid var(--border);
	}

	.section-title {
		font-size: 0.8125rem;
		font-weight: 600;
		color: var(--text-primary);
	}

	.section-desc {
		margin: 0;
		font-size: 0.6875rem;
		color: var(--text-muted);
	}

	.generate-row {
		display: flex;
		align-items: center;
		gap: 0.5rem;
	}

	.profile-select {
		flex: 1;
		min-width: 0;
	}

	.signature-fields {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
	}

	.form-group {
		display: flex;
		align-items: center;
		gap: 0.5rem;
	}

	.label {
		width: 1.75rem;
		font-size: 0.75rem;
		font-weight: 500;
		color: var(--text-secondary);
	}

	.input {
		flex: 1;
		min-width: 0;
		padding: 6px 10px;
		font-size: 12px;
		font-family: var(--font-mono, monospace);
		background: var(--bg-primary);
		border: 1px solid var(--border);
		border-radius: 6px;
		color: var(--text-primary);
	}

	.input:focus {
		outline: none;
		border-color: var(--accent);
	}

	.size-indicator {
		font-size: 0.6875rem;
		color: var(--text-muted);
	}

	.size-indicator.over-limit {
		color: var(--error, #ef4444);
		font-weight: 600;
	}
</style>
