<script lang="ts">
	import Button from '#lib/ui/Button.svelte';
	import Icon from '#lib/ui/Icon.svelte';
	import Poster from '#lib/ui/Poster.svelte';
	import ProgressBar from '#lib/ui/ProgressBar.svelte';
	import { appLocale, t as tr } from '#lib/core/i18n/i18n.svelte.ts';
	import { to } from '#lib/core/paths.ts';
	import {
		fileBaseName,
		formatBytes,
		formatFps,
		formatRealtime,
		itemTitle,
		jobPercent,
		phaseLabel,
		phaseTone
	} from './helpers.ts';
	import type { OverviewJob } from './types.ts';

	let {
		activeJobs,
		runningCount,
		queuedCount,
		art,
		stoppingId = null,
		onstop,
		onopenwaiting
	}: {
		activeJobs: OverviewJob[];
		runningCount: number;
		queuedCount: number;
		art: Map<number, string>;
		stoppingId?: number | null;
		onstop: (job: OverviewJob) => void;
		onopenwaiting: () => void;
	} = $props();

	const num = (n: number) => n.toLocaleString(appLocale());
</script>

{#if activeJobs.length > 0 || runningCount > 0}
	<section class="tp-sheet active-card" aria-labelledby="tc-jobs" data-testid="transcode-jobs">
		<div class="sec-head">
			<div class="active-head-left">
				<h2 id="tc-jobs" class="head">{tr('transcode.jobs.activeTitle')}</h2>
				<span class="tp-note active-count">
					{tr('transcode.jobs.activeCount', { n: num(activeJobs.length || runningCount) })}
				</span>
			</div>
			{#if queuedCount > 0}
				<button
					type="button"
					class="waiting-link"
					onclick={onopenwaiting}
					data-testid="transcode-goto-waiting"
				>
					<span>{tr('transcode.jobs.viewWaiting', { n: num(queuedCount) })}</span>
					<Icon name="arrow" size={14} />
				</button>
			{/if}
		</div>
		{#if activeJobs.length === 0}
			<p class="tp-note pad">{tr('transcode.jobs.none')}</p>
		{:else}
			<ul class="active-grid" aria-live="polite">
				{#each activeJobs as j (j.id)}
					{@const pct = jobPercent(j) ?? 0}
					{@const name = itemTitle(j) || fileBaseName(j.relative_path) || j.kind.replaceAll('_', ' ')}
					{@const fpsText = formatFps(j.fps)}
					{@const speedText = formatRealtime(j.speed)}
					{@const speedLine = [fpsText, speedText].filter(Boolean).join(' · ')}
					{@const phase = phaseLabel(j.phase)}
					<li class="active-tile" data-testid="transcode-job">
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
										<span class="pill {phaseTone(j.phase)}">{phase}</span>
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
									aria-label={tr('transcode.jobs.stopFor', { name })}
									onclick={() => onstop(j)}
									data-testid="transcode-stop-job"
								>{tr('transcode.jobs.stop')}</Button>
							</div>
						</div>

						<div class="tile-prog">
							<div class="tile-prog-head">
								<span class="jspeed">{speedLine}</span>
								<span class="jstate run">{pct}%</span>
							</div>
							<ProgressBar value={pct} size="sm" label={tr('transcode.jobs.progress', { name })} />
						</div>

						{#if j.profile_name || j.encoder || j.gpu_device || j.selected_q != null || j.mean_vmaf != null || j.input_bytes > 0}
							<div class="jpills">
								{#if j.profile_name}<span class="pill">{j.profile_name}</span>{/if}
								{#if j.encoder}<span class="pill">{j.encoder}</span>{/if}
								{#if j.gpu_device}<span class="pill">{j.gpu_device}</span>{/if}
								{#if j.selected_q != null}<span class="pill">Q {j.selected_q}</span>{/if}
								{#if j.mean_vmaf != null}<span class="pill">VMAF {Number(j.mean_vmaf.toFixed(1))}</span>{/if}
								{#if j.input_bytes > 0}<span class="pill">{formatBytes(j.input_bytes)}</span>{/if}
							</div>
						{/if}

						{#if j.note}
							<p class="tp-note">{j.note}</p>
						{/if}
					</li>
				{/each}
			</ul>
		{/if}
	</section>
{:else if queuedCount > 0}
	<section
		class="tp-sheet waiting-callout"
		aria-label={tr('transcode.tabs.waiting')}
		data-testid="transcode-waiting-callout"
	>
		<div class="callout-text">
			<Icon name="clock" size={16} />
			<span>
				{tr(
					queuedCount === 1
						? 'transcode.jobs.waitingCallout.one'
						: 'transcode.jobs.waitingCallout.other',
					{ n: num(queuedCount) }
				)}
			</span>
		</div>
		<Button
			variant="secondary"
			size="sm"
			iconAfter="chevronRight"
			onclick={onopenwaiting}
			data-testid="transcode-goto-waiting"
		>{tr('transcode.jobs.openWaiting')}</Button>
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
