<script lang="ts" module>
	/** Откуда берём ключ: сохранённый на роутере или введённый заново. */
	export type PremiumKeySource = 'stored' | 'new';
</script>

<script lang="ts">
	import Button from '$lib/components/ui/Button.svelte';

	interface Props {
		/** Введённый ключ; владелец — мастер: он же чистит поле при сбросе. */
		value: string;
		remember: boolean;
		busy: boolean;
		/** Ключ на роутере лежит, но не расшифровывается: единственный выход — забыть его. */
		unusableStored: boolean;
		/** На роутере лежит ПРИГОДНЫЙ ключ — значит есть из чего выбирать. */
		hasStoredKey: boolean;
		/** Что делаем: берём сохранённый ключ или вводим другой. */
		source: PremiumKeySource;
		oninput: (value: string) => void;
		onremember: (remember: boolean) => void;
		onsource: (source: PremiumKeySource) => void;
		onforget: () => void;
	}

	let {
		value,
		remember,
		busy,
		unusableStored,
		hasStoredKey,
		source,
		oninput,
		onremember,
		onsource,
		onforget
	}: Props = $props();

	// Поле ключа прячется, а не гасится: выбран сохранённый ключ — вводить
	// нечего, и пустое неактивное поле рядом с выбором читается как «сюда всё
	// равно надо что-то вписать».
	const showInput = $derived(!hasStoredKey || source === 'new');
</script>

<div class="premium-key-form">
	{#if hasStoredKey}
		<!-- Хранение ключа имеет смысл ровно тогда, когда им можно
		     воспользоваться не вводя его заново. Выбор явный: молча применять
		     сохранённый ключ значит не дать сменить подписку, а молча просить
		     ввод — обесценить хранение. -->
		<fieldset class="premium-key-source" disabled={busy}>
			<legend class="premium-key-source-legend">На роутере сохранён ключ подписки.</legend>
			<label class="premium-key-source-option">
				<input
					type="radio"
					name="premium-key-source"
					value="stored"
					checked={source === 'stored'}
					onchange={() => onsource('stored')}
				/>
				<span>Использовать сохранённый ключ</span>
			</label>
			<label class="premium-key-source-option">
				<input
					type="radio"
					name="premium-key-source"
					value="new"
					checked={source === 'new'}
					onchange={() => onsource('new')}
				/>
				<span>Ввести другой ключ</span>
			</label>
		</fieldset>
	{/if}
	{#if unusableStored}
		<p class="premium-key-unusable">
			Сохранённый на роутере ключ не читается — его зашифровали другим секретом устройства.
			Забудьте его и введите ключ заново.
		</p>
	{/if}
	{#if showInput}
		<label class="field-label" for="premium-key-input">Ключ подписки Amnezia Premium</label>
		<textarea
			id="premium-key-input"
			class="field-textarea premium-key-input"
			placeholder="vpn://…"
			spellcheck="false"
			disabled={busy}
			{value}
			oninput={(e) => oninput(e.currentTarget.value)}
		></textarea>
		<label class="premium-key-remember">
			<input
				type="checkbox"
				checked={remember}
				disabled={busy}
				onchange={(e) => onremember(e.currentTarget.checked)}
			/>
			<span>Запомнить ключ на роутере (хранится в зашифрованном виде)</span>
		</label>
	{/if}
	<p class="premium-key-direct">
		Запрос к сервису Amnezia уходит с роутера напрямую, поэтому список стран и выдача зависят от
		того, доступен ли сервис из вашего региона.
	</p>
	<p class="premium-cp-note">
		Если распознан ключ для получения параметров подписки, приложение может обратиться к внешнему сервису по вашей инициативе.<br>
		AWG Manager не связан с операторами таких сервисов, не проверяет и не гарантирует ключи, доступность их API а так же стабильность работы данного функционала.<br>
		В рамках использования приложения и связанных решений вы принимаете на себя ответственность за соблюдение законодательства страны, в которой находитесь.<br>
		Данный функционал не является официальной интеграцией и не подлежит технической поддержке.
	</p>
	{#if unusableStored}
		<div class="premium-key-actions">
			<Button variant="ghost" size="md" disabled={busy} onclick={onforget}>Забыть ключ</Button>
		</div>
	{/if}
</div>

<style>
	.premium-key-form {
		display: flex;
		flex-direction: column;
		gap: 8px;
	}

	.premium-key-input {
		min-height: 84px;
		width: 100%;
	}

	.premium-key-remember {
		display: flex;
		align-items: flex-start;
		gap: 8px;
		font-size: 0.8125rem;
		color: var(--text-secondary, var(--color-text-secondary));
		cursor: pointer;
	}

	.premium-key-remember input {
		margin-top: 2px;
		flex-shrink: 0;
	}

	.premium-key-unusable {
		margin: 0;
		padding: 8px 12px;
		font-size: 0.8125rem;
		line-height: 1.45;
		color: var(--warning, var(--color-warning));
		background: var(--color-warning-tint);
		border: 1px solid var(--color-warning-border);
		border-radius: 8px;
	}

	.premium-key-direct {
		margin: 0;
		font-size: 0.8125rem;
		line-height: 1.45;
		color: var(--text-secondary, var(--color-text-secondary));
	}

	.premium-key-actions {
		display: flex;
		justify-content: flex-start;
	}

	.premium-key-source {
		display: flex;
		flex-direction: column;
		gap: 6px;
		margin: 0;
		padding: 10px 12px;
		border: 1px solid var(--border, var(--color-border));
		border-radius: 8px;
	}

	.premium-key-source-legend {
		padding: 0 4px;
		font-size: 0.8125rem;
		color: var(--text-secondary, var(--color-text-secondary));
	}

	.premium-key-source-option {
		display: flex;
		align-items: center;
		gap: 8px;
		font-size: 0.875rem;
		cursor: pointer;
	}

	.premium-key-source-option input {
		flex-shrink: 0;
	}

	/* Перенесено дословно из VpnLinkPasteImport вместе со стилями: юридический
	   дисклеймер обязан стоять там, где ключ действительно вводят. */
	.premium-cp-note {
		font-size: 12px;
		line-height: 1.45;
		color: var(--text-muted, var(--color-text-muted));
		margin: 10px 0 0;
		padding: 8px 12px;
		background: var(--bg-secondary, var(--color-bg-secondary));
		border: 1px solid var(--border, var(--color-border));
		border-radius: 8px;
	}
</style>
