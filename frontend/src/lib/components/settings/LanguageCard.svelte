<!-- Карточка «Язык и формат»: доступна на любом уровне использования. -->
<script lang="ts">
	import { m } from '$lib/i18n';
	import { Languages } from 'lucide-svelte';
	import DateFormatSettingRow from './DateFormatSettingRow.svelte';
	import LanguageSettingRow from './LanguageSettingRow.svelte';
	import SettingsSectionLabel from './SettingsSectionLabel.svelte';
</script>

<div class="settings-block">
	<div class="card">
		<SettingsSectionLabel label={m.settings_language_card_title()} icon={Languages} tone="teal" header />
		<LanguageSettingRow />
		<DateFormatSettingRow />
	</div>
</div>
