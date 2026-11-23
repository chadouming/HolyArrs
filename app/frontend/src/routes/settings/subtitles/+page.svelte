<script lang="ts">
	import { t } from '#lib/core/i18n/i18n.svelte.ts';
	import { SubtitlesSettingsTab } from '#lib/subtitles/index.ts';
	import PageHeader from '#lib/ui/PageHeader.svelte';
</script>

<svelte:head><title>{t('settings.subtitles.title')} · HolyArrs</title></svelte:head>

<PageHeader title={t('settings.subtitles.title')} />

<SubtitlesSettingsTab />
