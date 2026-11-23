<script lang="ts">
	import Button from '#lib/ui/Button.svelte';
	import Icon from '#lib/ui/Icon.svelte';
	import Poster from '#lib/ui/Poster.svelte';
	import ProgressBar from '#lib/ui/ProgressBar.svelte';
	import { appLocale, t as tr } from '#lib/core/i18n/i18n.svelte.ts';
	import { to } from '#lib/core/paths.ts';
	import { langName, type Language } from './api.ts';
	import {
		fileBaseName,
		formatSpeed,
		jobName,
		jobPhaseTone,
		jobPhaseWord,
		jobShare,
		jobWord,
		type OverviewJob
	} from './overview.ts';

	let {
		activeJobs,
		runningCount,
		queuedCount,
		langs,
		art,
		stoppingId = null,
		onstop,
		onopenwaiting
	}: {
		activeJobs: OverviewJob[];
		runningCount: number;
		queuedCount: number;
		langs: Language[];
		art: Map<number, string>;
		stoppingId?: number | null;
		onstop: (job: OverviewJob) => void;
		onopenwaiting: () => void;
	} = $props();

	const num = (n: number) => n.toLocaleString(appLocale());
</script>

{#if activeJobs.length > 0 || runningCount > 0}
	<section class="tp-sheet active-card" aria-labelledby="sub-jobs" data-testid="subtitles-jobs">
		<div class="sec-head">
			<div class="active-head-left">
				<h2 id="sub-jobs" class="head">{tr('subtitles.jobs.activeTitle')}</h2>
				<span class="tp-note active-count">
					{tr('subtitles.jobs.activeCount', { n: num(activeJobs.length || runningCount) })}
				</span>
			</div>
			{#if queuedCount > 0}
				<button
					type="button"
					class="waiting-link"
					onclick={onopenwaiting}
					data-testid="subtitles-goto-waiting"
				>
					<span>{tr('subtitles.jobs.viewWaiting', { n: num(queuedCount) })}</span>
					<Icon name="arrow" size={14} />
				</button>
			{/if}
		</div>
		{#if activeJobs.length === 0}
			<p class="tp-note pad">{tr('subtitles.page.jobs.none')}</p>
		{:else}
			<ul class="active-grid" aria-live="polite">
				{#each activeJobs as j (j.id)}
					{@const pct = jobShare(j) ?? 0}
					{@const name = jobName(j) || fileBaseName(j.relative_path) || jobWord(j.kind)}
					{@const phaseRaw = j.phase ?? j.progress_note}
					{@const phase = jobPhaseWord(phaseRaw)}
					{@const speedText = formatSpeed(j.speed, j.speed_unit)}
					<li class="active-tile" data-testid="subtitles-job">
						<div class="tile-top">
							<span class="tp-art" style="--poster-ratio: 2 / 3">
								<Poster src={j.title_id ? art.get(j.title_id) : undefined} {name} />
							</span>
							<div class="tile-title-col">
								<div class="jtitle-wrap">
									{#if j.title_id}
										<a class="jname" href={to(`/library/${j.title_id}`)}>{name}</a>
									{:else}
										<span class="jname">{name}</span>
									{/if}
									{#if phase}
										<span class="pill {jobPhaseTone(phaseRaw)}">{phase}</span>
									{/if}
								</div>
							</div>
							<div class="tile-stop">
								<Button
									variant="plain"
									size="sm"
									icon="x"
									busy={stoppingId === j.id}
									disabled={stoppingId === j.id}
									aria-label={tr('subtitles.page.jobs.stopFor', { name })}
									onclick={() => onstop(j)}
									data-testid="subtitles-job-stop"
								>{tr('subtitles.page.jobs.stop')}</Button>
							</div>
						</div>

						<div class="tile-prog">
							<div class="tile-prog-head">
								<span class="jspeed">{speedText}</span>
								<span class="jstate run">{pct}%</span>
							</div>
							<ProgressBar value={pct} size="sm" label={tr('subtitles.page.jobs.progress', { name })} />
						</div>

						<div class="jpills">
							<span class="pill">{jobWord(j.kind)}</span>
							{#if j.language}<span class="pill">{langName(j.language, langs)}</span>{/if}
							{#if j.forced}<span class="pill">{tr('subtitles.kind.forced')}</span>{/if}
						</div>
					</li>
				{/each}
			</ul>
		{/if}
	</section>
{:else if queuedCount > 0}
	<section
		class="tp-sheet waiting-callout"
		aria-label={tr('subtitles.tabs.waiting')}
		data-testid="subtitles-waiting-callout"
	>
		<div class="callout-text">
			<Icon name="clock" size={16} />
			<span>
				{tr(
					queuedCount === 1
						? 'subtitles.jobs.waitingCallout.one'
						: 'subtitles.jobs.waitingCallout.other',
					{ n: num(queuedCount) }
				)}
			</span>
		</div>
		<Button
			variant="secondary"
			size="sm"
			iconAfter="chevronRight"
			onclick={onopenwaiting}
			data-testid="subtitles-goto-waiting"
		>{tr('subtitles.jobs.openWaiting')}</Button>
	</section>
{/if}

<style>
	.sec-head { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 8px 12px; padding: 16px 20px 6px; }
	.active-head-left { display: inline-flex; align-items: baseline; gap: 10px; min-width: 0; }
	.active-count { font-variant-numeric: tabular-nums; }
	.head { margin: 0; font: var(--t-headline); }
	.pad { padding: 6px 20px 16px; }

	.waiting-link {
		display: inline-flex;
		align-items: center;
		gap: 5px;
		height: 28px;
		padding: 0 10px;
		border-radius: 999px;
		border: 1px solid var(--line);
		background: color-mix(in srgb, var(--text) 4%, transparent);
		color: var(--text);
		font: var(--t-footnote);
		font-weight: 600;
		font-variant-numeric: tabular-nums;
		cursor: pointer;
		transition: background-color var(--dur-fast) var(--ease-out), border-color var(--dur-fast) var(--ease-out);
	}
	.waiting-link:hover { background: color-mix(in srgb, var(--text) 8%, transparent); border-color: var(--line-strong); }
	.waiting-link:focus-visible { outline: none; box-shadow: var(--focus-ring); }

	.waiting-callout {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		justify-content: space-between;
		gap: 10px 14px;
		padding: 12px 20px;
	}
	.callout-text {
		display: inline-flex;
		align-items: center;
		gap: 8px;
		color: var(--text);
		font: var(--t-callout);
		font-weight: 500;
		font-variant-numeric: tabular-nums;
	}
	.callout-text :global(.icon) { color: var(--text-muted); }

	.active-grid {
		display: grid;
		grid-template-columns: repeat(2, minmax(0, 1fr));
		gap: 14px;
		padding: 12px 20px 20px;
		margin: 0;
		list-style: none;
	}
	.active-tile {
		background: color-mix(in srgb, var(--text) 3.5%, transparent);
		border: 1px solid var(--line);
		border-radius: 14px;
		padding: 14px 16px;
		display: flex;
		flex-direction: column;
		gap: 10px;
		min-width: 0;
	}
	.tile-top { display: flex; align-items: flex-start; gap: 12px; min-width: 0; }
	.tile-title-col { display: flex; flex-direction: column; gap: 4px; min-width: 0; flex: 1 1 auto; }
	.tile-stop { flex: none; margin-left: auto; }
	.tile-prog { display: flex; flex-direction: column; gap: 6px; min-width: 0; }
	.tile-prog-head { display: flex; align-items: baseline; justify-content: space-between; gap: 10px; min-height: 18px; }

	.jtitle-wrap { display: flex; flex-wrap: wrap; align-items: center; gap: 6px 8px; min-width: 0; }
	.jspeed { color: var(--text-muted); font: var(--t-footnote); font-variant-numeric: tabular-nums; white-space: nowrap; }
	.jname { color: var(--text); font: 600 14px/1.3 var(--font-ui); text-decoration: none; overflow-wrap: anywhere; }
	a.jname:hover { text-decoration: underline; text-underline-offset: 3px; }
	.jstate { flex: none; color: var(--text-muted); font: var(--t-footnote); font-variant-numeric: tabular-nums; }
	.jstate.run { color: var(--text); font-weight: 600; }
	.jpills { display: flex; flex-wrap: wrap; gap: 5px; }

	.pill { flex: none; display: inline-flex; align-items: center; height: 22px; padding: 0 8px; border-radius: 999px; font: 600 11.5px/1 var(--font-ui); font-variant-numeric: tabular-nums; background: color-mix(in srgb, var(--text) 8%, transparent); color: color-mix(in srgb, var(--text) 82%, var(--bg)); }
	.pill.ok { background: color-mix(in srgb, var(--ok) 16%, transparent); color: var(--ok-text, var(--ok)); }
	.pill.wait { background: color-mix(in srgb, var(--wait, var(--accent)) 16%, transparent); color: var(--wait-text, var(--wait, var(--text))); }

	@media (max-width: 900px) {
		.active-grid { grid-template-columns: minmax(0, 1fr); }
	}
	@media (max-width: 639px) {
		.sec-head { padding: 14px 16px 6px; }
		.waiting-callout { padding: 12px 16px; }
		.active-grid { padding: 10px 16px 16px; }
	}
	@media (prefers-reduced-motion: reduce) {
		.waiting-link { transition: none; }
	}
</style>
