<script lang="ts" module>
	export interface ManualObfuscator {
		target: string;
		key: string;
		masking: 'STUN' | 'AUTO' | 'NONE';
		maxDummy: number;
		idleTimeout: number;
	}
</script>

<script lang="ts">
	interface Props {
		flavor: 'phobos' | 'clusterm';
		content?: string;
		installUrl?: string;
		obfuscator?: ManualObfuscator;
	}
	let {
		flavor,
		content = $bindable(''),
		installUrl = $bindable(''),
		obfuscator = $bindable({ target: '', key: '', masking: 'STUN', maxDummy: 4, idleTimeout: 0 })
	}: Props = $props();
</script>

{#if flavor === 'phobos'}
	<div class="flex flex-col gap-1.5">
		<label class="field-label" for="obf-install-url">Ссылка установки Phobos</label>
		<input
			id="obf-install-url"
			class="field-input"
			type="url"
			placeholder="https://panel.example/api/install/<token>"
			bind:value={installUrl}
		>
		<p class="form-hint">
			Роутер сам скачает пакет с панели. Сертификат панели не проверяется (обычно он
			самоподписанный); ссылка живёт ограниченное время.
		</p>
	</div>
	<div class="flex flex-col gap-1.5">
		<label class="field-label" for="obf-content">
			Или конфиг .conf с секцией [instance] / ссылка phobos://
		</label>
		<textarea id="obf-content" class="field-input font-mono" rows="10" bind:value={content}></textarea>
	</div>
{:else}
	<div class="flex flex-col gap-1.5">
		<label class="field-label" for="obf-content">Конфиг WireGuard (.conf)</label>
		<textarea id="obf-content" class="field-input font-mono" rows="8" bind:value={content}></textarea>
		<p class="form-hint">
			Endpoint из файла не используется: WireGuard будет ходить в локальный релей, а релей — на
			сервер ниже.
		</p>
	</div>
	<div class="flex flex-col gap-1.5">
		<label class="field-label" for="obf-target">Сервер (host:port) обфускатора</label>
		<input
			id="obf-target"
			class="field-input"
			placeholder="vpn.example.com:51824"
			bind:value={obfuscator.target}
		>
	</div>
	<div class="flex flex-col gap-1.5">
		<label class="field-label" for="obf-key">Ключ</label>
		<input id="obf-key" class="field-input" bind:value={obfuscator.key}>
	</div>
	<div class="flex flex-col gap-1.5">
		<label class="field-label" for="obf-masking">Маскировка</label>
		<select id="obf-masking" class="field-input" bind:value={obfuscator.masking}>
			<option value="STUN">STUN</option>
			<option value="AUTO">AUTO</option>
			<option value="NONE">NONE</option>
		</select>
	</div>
	<div class="flex gap-3">
		<div class="flex flex-col gap-1.5">
			<label class="field-label" for="obf-dummy">max-dummy</label>
			<input
				id="obf-dummy"
				class="field-input"
				type="number"
				min="0"
				max="1024"
				bind:value={obfuscator.maxDummy}
			>
		</div>
		<div class="flex flex-col gap-1.5">
			<label class="field-label" for="obf-idle">idle-timeout, с (0 = по умолчанию)</label>
			<input id="obf-idle" class="field-input" type="number" min="0" bind:value={obfuscator.idleTimeout}>
		</div>
	</div>
{/if}
