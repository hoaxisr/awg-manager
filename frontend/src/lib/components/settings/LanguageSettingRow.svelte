<script lang="ts">
	import { SegmentedControl } from '$lib/components/ui';
	import type { SegmentedOption } from '$lib/components/ui/segmentedControl';
	import { m, locale, LOCALES, LOCALE_LABELS, type Locale } from '$lib/i18n';

	const options: SegmentedOption<Locale>[] = LOCALES.map((value) => ({
		value,
		label: LOCALE_LABELS[value],
	}));
</script>

<div class="setting-row language-row">
	<div class="flex flex-col gap-1">
		<span class="font-medium">{m.settings_language_label()}</span>
		<span class="setting-description">{m.settings_language_description()}</span>
	</div>
	<SegmentedControl
		value={locale.current}
		{options}
		ariaLabel={m.settings_language_label()}
		onchange={(next) => locale.set(next)}
	/>
</div>

<style>
	.language-row {
		align-items: center;
	}

	@media (max-width: 640px) {
		.language-row {
			flex-direction: row;
			align-items: center;
			flex-wrap: nowrap;
			gap: 0.75rem;
		}

		.language-row > *:first-child {
			flex: 1 1 auto;
			min-width: 0;
		}
	}
</style>
