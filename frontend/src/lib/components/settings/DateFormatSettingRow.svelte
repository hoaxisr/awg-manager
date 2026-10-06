<script lang="ts">
	import Dropdown, { type DropdownOption } from '$lib/components/ui/Dropdown.svelte';
	import { m, dateFormat, autoFormatLocale, DATE_FORMATS, type DateFormat } from '$lib/i18n';

	// Пример — одна и та же дата в каждом формате: так понятно без знания кодов локалей.
	const SAMPLE = new Date(2026, 11, 31, 15, 4);
	const sample = (loc: string) =>
		SAMPLE.toLocaleString(loc, { day: '2-digit', month: '2-digit', year: 'numeric', hour: 'numeric', minute: '2-digit' });

	// $derived: пример «Авто» пересчитывается при смене языка интерфейса.
	const options: DropdownOption<DateFormat>[] = $derived(
		DATE_FORMATS.map((value) => ({
			value,
			label:
				value === 'auto'
					? m.settings_date_format_auto({ example: sample(autoFormatLocale()) })
					: sample(value),
		})),
	);
</script>

<div class="setting-row date-format-row">
	<div class="flex flex-col gap-1">
		<span class="font-medium">{m.settings_date_format_label()}</span>
		<span class="setting-description">{m.settings_date_format_description()}</span>
	</div>
	<div class="date-format-select">
		<Dropdown value={dateFormat.current} {options} onchange={(next) => dateFormat.set(next)} fullWidth />
	</div>
</div>

<style>
	.date-format-row {
		align-items: center;
	}

	.date-format-select {
		min-width: 14rem;
	}

	@media (max-width: 640px) {
		.date-format-row {
			flex-direction: column;
			align-items: stretch;
			gap: 0.5rem;
		}

		.date-format-select {
			min-width: 0;
		}
	}
</style>
