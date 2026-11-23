<script lang="ts">
	import Icon from '#lib/ui/Icon.svelte';
	import ProgressBar from '#lib/ui/ProgressBar.svelte';
	import { appLocale, t as tr } from '#lib/core/i18n/i18n.svelte.ts';
	import {
		coveragePercent,
		formatBacklogEta,
		formatBytes,
		formatFps,
		formatRealtime,
		gpuSummary,
		savingsPercent
	} from './helpers.ts';
	import type { Overview } from './types.ts';

	let { overview }: { overview: Overview } = $props();

	const num = (n: number) => n.toLocaleString(appLocale());
	const covPct = $derived(coveragePercent(overview));
	const etaText = $derived(formatBacklogEta(overview.eta_seconds));
	const savPct = $derived(savingsPercent(overview));
	const savedBytes = $derived(overview.savings?.saved_bytes ?? 0);
	const spaceLost = $derived(savedBytes < 0);
	const gpus = $derived(gpuSummary(overview.gpus));
	const isLive = $derived(
		(overview.speed?.live_fps ?? 0) > 0 || (overview.speed?.live_realtime_x ?? 0) > 0
	);
	const liveFps = $derived(formatFps(overview.speed?.live_fps));
	const liveX = $derived(formatRealtime(overview.speed?.live_realtime_x));
	const avgFps = $derived(formatFps(overview.speed?.fps));
	const avgX = $derived(formatRealtime(overview.speed?.realtime_x));
	const samples = $derived(overview.speed?.samples ?? 0);
</script>

<section
	class="stats-grid"
	aria-label={tr('transcode.stats.label')}
	data-testid="transcode-stats"
>
	<article class="tp-sheet stat-card" data-testid="transcode-stat-coverage">
		<div class="stat-head">
			<span class="stat-lab"><Icon name="transcode" size={15} />{tr('transcode.stats.coverage')}</span>
			<span class="stat-pct">{covPct.toLocaleString(appLocale(), { maximumFractionDigits: 1 })}%</span>
		</div>
		<ProgressBar value={covPct} size="sm" label={tr('transcode.stats.coverage')} />
		<div class="stat-foot">
			<p class="tp-note stat-sub">
				{tr('transcode.stats.coverageDetail', {
					completed: num(overview.coverage?.completed ?? 0),
					total: num(overview.coverage?.total ?? 0)
				})}
			</p>
			{#if etaText}
				<span class="stat-eta" data-testid="transcode-eta"><Icon name="clock" size={12} />{etaText}</span>
			{/if}
		</div>
	</article>

	<article class="tp-sheet stat-card" data-testid="transcode-stat-savings">
		<div class="stat-head">
			<span class="stat-lab"><Icon name="archive" size={15} />{spaceLost ? tr('transcode.stats.spaceLost') : tr('transcode.stats.savings')}</span>
			{#if (overview.savings?.files_done ?? 0) > 0}
				{#if spaceLost}
					<span class="pill fail">+{Math.abs(savPct).toLocaleString(appLocale(), { maximumFractionDigits: 1 })}%</span>
				{:else}
					<span class="pill ok">{savPct.toLocaleString(appLocale(), { maximumFractionDigits: 1 })}%</span>
				{/if}
			{/if}
		</div>
		<div class="stat-val" class:lost={spaceLost}>{spaceLost ? formatBytes(-savedBytes) : formatBytes(savedBytes)}</div>
		<p class="tp-note stat-sub">
			{tr(
				(overview.savings?.files_done ?? 0) === 1
					? 'transcode.stats.savingsFiles.one'
					: 'transcode.stats.savingsFiles.other',
				{ n: num(overview.savings?.files_done ?? 0) }
			)}
		</p>
	</article>

	<article class="tp-sheet stat-card" data-testid="transcode-stat-speed">
		<div class="stat-head">
			<span class="stat-lab"><Icon name="gauge" size={15} />{tr('transcode.stats.speed')}</span>
			{#if isLive}
				<span class="live-pill">
					<i class="live-dot" aria-hidden="true"></i>
					{tr('transcode.stats.live')}
				</span>
			{/if}
		</div>
		{#if isLive}
			<div class="stat-val">
				{liveFps || liveX}
				{#if liveFps && liveX}<span class="stat-unit">{liveX}</span>{/if}
			</div>
			{#if avgFps || avgX}
				<p class="tp-note stat-sub">
					{tr('transcode.stats.avgShort', { speed: [avgFps, avgX].filter(Boolean).join(' · ') })}
				</p>
			{/if}
		{:else if avgFps || avgX}
			<div class="stat-val">
				{avgFps || avgX}
				{#if avgFps && avgX}<span class="stat-unit">{avgX}</span>{/if}
			</div>
			<p class="tp-note stat-sub">
				{tr(samples === 1 ? 'transcode.stats.runs.one' : 'transcode.stats.runs.other', {
					n: num(samples)
				})}
			</p>
		{:else}
			<div class="stat-val muted">{tr('transcode.stats.noRuns')}</div>
			<p class="tp-note stat-sub">{tr('transcode.stats.noRunsHint')}</p>
		{/if}
	</article>

	<article class="tp-sheet stat-card" data-testid="transcode-stat-hardware">
		<div class="stat-head">
			<span class="stat-lab"><Icon name="film" size={15} />{tr('transcode.stats.hardware')}</span>
			{#if overview.preferred_encoder}
				<span class="pill">{overview.preferred_encoder}</span>
			{/if}
		</div>
		{#if gpus.total > 0}
			<div class="stat-val">
				{tr(gpus.total === 1 ? 'transcode.stats.gpuCount.one' : 'transcode.stats.gpuCount.other', {
					n: num(gpus.total)
				})}
			</div>
			<p class="tp-note stat-sub" title={gpus.names}>
				{#if gpus.busy > 0}
					{tr('transcode.stats.gpuBusy', { n: num(gpus.busy) })}
				{:else}
					{gpus.names || tr('transcode.stats.gpuIdle')}
				{/if}
			</p>
		{:else}
			<div class="stat-val">{overview.ready ? tr('transcode.stats.cpuOnly') : tr('transcode.stats.notReady')}</div>
			<p class="tp-note stat-sub">
				{overview.ready ? tr('transcode.stats.noGpuDetected') : tr('transcode.stats.checkSettings')}
			</p>
		{/if}
	</article>
</section>

<style>
	.stats-grid {
		display: grid;
		grid-template-columns: repeat(4, minmax(0, 1fr));
		gap: 14px;
	}
	.stat-card {
		display: flex;
		flex-direction: column;
		justify-content: space-between;
		gap: 8px;
		padding: 16px 18px;
		min-width: 0;
	}
	.stat-head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 8px;
		min-width: 0;
	}
	.stat-lab {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		color: var(--text-muted);
		font: 600 12px/1.3 var(--font-ui);
		min-width: 0;
	}
	.stat-pct {
		color: var(--text);
		font: 700 20px/1.1 var(--font-display, var(--font-ui));
		letter-spacing: -0.02em;
		font-variant-numeric: tabular-nums;
	}
	.stat-val {
		color: var(--text);
		font: 700 22px/1.15 var(--font-display, var(--font-ui));
		letter-spacing: -0.02em;
		font-variant-numeric: tabular-nums;
		display: flex;
		align-items: baseline;
		flex-wrap: wrap;
		gap: 6px;
	}
	.stat-val.muted {
		color: var(--text-muted);
		font-size: 16px;
		font-weight: 600;
	}
	.stat-val.lost {
		color: var(--fail-text, var(--fail));
	}
	.stat-unit {
		color: var(--text-muted);
		font: 600 13px/1 var(--font-ui);
	}
	.stat-foot {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		justify-content: space-between;
		gap: 4px 8px;
		min-width: 0;
	}
	.stat-sub {
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.stat-eta {
		display: inline-flex;
		align-items: center;
		gap: 4px;
		flex: none;
		white-space: nowrap;
		color: var(--text-muted);
		font: var(--t-footnote);
		font-weight: 500;
		font-variant-numeric: tabular-nums;
	}
	.pill {
		flex: none;
		display: inline-flex;
		align-items: center;
		height: 22px;
		padding: 0 8px;
		border-radius: 999px;
		font: 600 11.5px/1 var(--font-ui);
		font-variant-numeric: tabular-nums;
		background: color-mix(in srgb, var(--text) 8%, transparent);
		color: var(--text-muted);
	}
	.pill.ok {
		background: color-mix(in srgb, var(--ok) 16%, transparent);
		color: var(--ok-text, var(--ok));
	}
	.pill.fail {
		background: color-mix(in srgb, var(--fail) 16%, transparent);
		color: var(--fail-text, var(--fail));
	}
	.live-pill {
		display: inline-flex;
		align-items: center;
		gap: 5px;
		height: 22px;
		padding: 0 8px;
		border-radius: 999px;
		font: 600 11.5px/1 var(--font-ui);
		background: color-mix(in srgb, var(--ok) 16%, transparent);
		color: var(--ok-text, var(--ok));
	}
	.live-dot {
		width: 6px;
		height: 6px;
		border-radius: 50%;
		background: var(--ok);
		animation: tc-pulse 1.6s ease-in-out infinite;
	}
	@keyframes tc-pulse {
		0%, 100% { opacity: 1; transform: scale(1); }
		50% { opacity: 0.45; transform: scale(0.8); }
	}
	@media (max-width: 1023px) {
		.stats-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); }
	}
	@media (max-width: 639px) {
		.stats-grid { grid-template-columns: minmax(0, 1fr); }
		.stat-card { padding: 14px 16px; }
	}
	@media (prefers-reduced-motion: reduce) {
		.live-dot { animation: none; }
	}
</style>
