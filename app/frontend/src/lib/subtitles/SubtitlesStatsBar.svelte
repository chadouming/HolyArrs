<script lang="ts">
	import Icon from '#lib/ui/Icon.svelte';
	import ProgressBar from '#lib/ui/ProgressBar.svelte';
	import { appLocale, t as tr } from '#lib/core/i18n/i18n.svelte.ts';
	import { formatBacklogEta, formatSpeed, libraryPercent, type Overview, type SpeedStat } from './overview.ts';

	let { overview }: { overview: Overview } = $props();

	const num = (n: number) => n.toLocaleString(appLocale());
	const lib = $derived(overview.library);
	const pct = $derived(libraryPercent(lib));
	const stats = $derived(overview.stats);
	const etaText = $derived(formatBacklogEta(overview.eta_seconds));

	function samplesLabel(samples: number): string {
		return tr(samples === 1 ? 'subtitles.page.stats.samples.one' : 'subtitles.page.stats.samples.other', { n: num(samples) });
	}

	function recentLabel(stat: SpeedStat | undefined): string {
		if (!stat) return '';
		const formatted = formatSpeed(stat.avg_speed, stat.unit);
		if (!formatted) return '';
		return stat.unit === 'x' ? tr('subtitles.page.stats.realtime', { speed: formatted }) : formatted;
	}
</script>

{#if lib || stats}
	<section class="tp-sheet stats" aria-label={tr('subtitles.page.stats.label')} data-testid="subtitles-stats">
		{#if lib}
			{@const pctLabel = `${pct.toLocaleString(appLocale(), { maximumFractionDigits: 1 })}%`}
			<div class="cov" data-testid="subtitles-coverage">
				<div class="cov-head">
					<span class="slab">{tr('subtitles.page.stats.coverage')}</span>
					<span class="spct">{pctLabel}</span>
				</div>
				<ProgressBar value={pct} label={tr('subtitles.page.stats.coverageBar')} />
				<div class="cov-foot">
					<p class="tp-note cov-detail">
						{tr('subtitles.page.stats.coverageDetail', {
							have: num(lib.have),
							total: num(lib.wanted),
							missing: num(lib.missing),
							files: num(lib.files_total)
						})}
						{#if lib.unlooked_files > 0}
							<span class="unlooked">· {tr(lib.unlooked_files === 1 ? 'subtitles.page.stats.unlooked.one' : 'subtitles.page.stats.unlooked.other', { n: num(lib.unlooked_files) })}</span>
						{/if}
					</p>
					{#if etaText}
						<span class="cov-eta" data-testid="subtitles-eta"><Icon name="clock" size={13} />{etaText}</span>
					{/if}
				</div>
			</div>
		{/if}

		{#if stats}
			<div class="speeds" class:solo={!lib}>
				{@render speedBlock(tr('subtitles.page.job.transcribe'), stats.speech, 'speech')}
				{@render speedBlock(tr('subtitles.page.stats.translateTitle'), stats.translate, 'translate')}
			</div>
		{/if}
	</section>
{/if}

{#snippet speedBlock(title: string, stat: SpeedStat | undefined, kind: string)}
	{@const liveText = stat ? formatSpeed(stat.live_speed, stat.unit) : ''}
	{@const avgText = recentLabel(stat)}
	{@const samples = stat?.samples ?? 0}
	<div class="sp" data-testid="subtitles-speed-{kind}">
		<span class="slab">{title}</span>
		{#if stat && liveText}
			<div class="sp-val-row">
				<span class="sp-val">{liveText}</span>
				<span class="live-pill">{tr('subtitles.page.stats.live')}</span>
			</div>
			{#if avgText && samples > 0}
				<span class="tp-note">{`${tr('subtitles.page.stats.avgShort', { speed: formatSpeed(stat.avg_speed, stat.unit) })} · ${samplesLabel(samples)}`}</span>
			{/if}
		{:else if avgText}
			<span class="sp-val">{avgText}</span>
			{#if samples > 0}
				<span class="tp-note">{samplesLabel(samples)}</span>
			{/if}
		{:else}
			<span class="tp-note sp-empty">{tr('subtitles.page.stats.noRuns')}</span>
		{/if}
	</div>
{/snippet}

<style>
	.stats { display: grid; grid-template-columns: minmax(0, 1.35fr) minmax(0, 1fr); align-items: stretch; }
	.cov, .speeds { padding: 16px 20px; min-width: 0; }
	.cov { display: grid; gap: 10px; align-content: center; }
	.cov-head { display: flex; align-items: baseline; justify-content: space-between; gap: 12px; }
	.slab { color: var(--text-muted); font: 600 12px/1.3 var(--font-ui); }
	.spct { color: var(--text); font: 700 24px/1.1 var(--font-display, var(--font-ui)); letter-spacing: -0.02em; font-variant-numeric: tabular-nums; }
	.cov-foot { display: flex; flex-wrap: wrap; align-items: baseline; justify-content: space-between; gap: 4px 12px; }
	.cov-detail { overflow-wrap: anywhere; }
	.cov-eta {
		display: inline-flex; align-items: center; gap: 4px; flex: none; white-space: nowrap;
		color: var(--text-muted); font: var(--t-footnote); font-weight: 500; font-variant-numeric: tabular-nums;
	}
	.unlooked { color: var(--text-muted); }

	.speeds { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 16px; border-inline-start: 1px solid var(--line); align-content: center; }
	.speeds.solo { grid-column: 1 / -1; border-inline-start: 0; }
	.sp { display: flex; flex-direction: column; gap: 4px; min-width: 0; }
	.sp-val-row { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; min-width: 0; }
	.sp-val { color: var(--text); font: 700 18px/1.2 var(--font-display, var(--font-ui)); letter-spacing: -0.015em; font-variant-numeric: tabular-nums; }
	.sp-empty { padding-top: 2px; }
	.live-pill {
		display: inline-flex; align-items: center; height: 20px; padding: 0 7px; border-radius: 999px;
		font: 600 11px/1 var(--font-ui); background: color-mix(in srgb, var(--ok) 16%, transparent); color: var(--ok-text, var(--ok));
	}

	@media (max-width: 900px) {
		.stats { grid-template-columns: minmax(0, 1fr); }
		.speeds { border-inline-start: 0; border-top: 1px solid var(--line); }
	}
	@media (max-width: 639px) {
		.cov, .speeds { padding: 14px 16px; }
		.speeds { grid-template-columns: minmax(0, 1fr); gap: 12px; }
		.spct { font-size: 21px; }
	}
</style>
