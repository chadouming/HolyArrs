<script lang="ts">
	import Button from '#lib/ui/Button.svelte';
	import EmptyState from '#lib/ui/EmptyState.svelte';
	import LoadError from '#lib/ui/LoadError.svelte';
	import PageLoader from '#lib/ui/PageLoader.svelte';
	import Poster from '#lib/ui/Poster.svelte';
	import { pager } from '#lib/mediafiles/index.ts';
	import { api, message } from '#lib/core/api.ts';
	import { dateTime } from '#lib/core/format.ts';
	import { appLocale, t as tr } from '#lib/core/i18n/i18n.svelte.ts';
	import { serverWords } from '#lib/core/i18n/server-words.ts';
	import { to } from '#lib/core/paths.ts';
	import { plainReason } from '#lib/core/plain.ts';
	import { toasts } from '#lib/core/toasts.svelte.ts';
	import { onMount, untrack } from 'svelte';
	import { fileBaseName, itemTitle } from './helpers.ts';
	import type { FailedJob } from './types.ts';

	interface FailedResponse {
		jobs?: FailedJob[];
		failed?: FailedJob[];
		total: number;
	}

	let {
		art,
		reloadSignal = 0,
		onchanged
	}: {
		art: Map<number, string>;
		reloadSignal?: number;
		onchanged?: () => void;
	} = $props();

	const limit = 50;
	let offset = $state(0);
	let jobs = $state<FailedJob[]>([]);
	let total = $state(0);
	let loaded = $state(false);
	let error = $state('');
	let retryingId = $state<number | null>(null);
	let retryingAll = $state(false);

	const num = (n: number) => n.toLocaleString(appLocale());
	const pages = $derived(pager(total, limit, offset));

	let seq = 0;
	async function load() {
		const n = ++seq;
		try {
			const q = new URLSearchParams({ limit: String(limit), offset: String(offset) });
			const res = await api.get<FailedResponse>(`/transcode/failed?${q.toString()}`);
			if (n !== seq) return;
			jobs = res.jobs ?? res.failed ?? [];
			total = res.total ?? 0;
			error = '';
		} catch (e) {
			if (n !== seq) return;
			error = message(e);
		} finally {
			if (n === seq) loaded = true;
		}
	}

	onMount(() => {
		void load();
	});

	let lastSignal = -1;
	$effect(() => {
		const sig = reloadSignal;
		if (lastSignal === -1) {
			lastSignal = sig;
			return;
		}
		if (sig !== lastSignal) {
			lastSignal = sig;
			untrack(() => void load());
		}
	});

	function go(nextOffset: number | null) {
		if (nextOffset === null) return;
		offset = nextOffset;
		void load();
	}

	async function retryOne(j: FailedJob) {
		if (retryingId !== null || retryingAll) return;
		retryingId = j.id;
		try {
			await api.post(`/transcode/jobs/${j.id}/retry`);
			const label = itemTitle(j) || fileBaseName(j.relative_path);
			toasts.push(tr('transcode.failed.retried'), 'info', label ? { body: label } : undefined);
			await load();
			onchanged?.();
		} catch (e) {
			toasts.push(tr('transcode.failed.retryFailed'), 'fail', { body: message(e) });
		} finally {
			retryingId = null;
		}
	}

	async function retryAll() {
		if (retryingAll || retryingId !== null) return;
		retryingAll = true;
		try {
			const res = await api.post<{ retried: number }>('/transcode/jobs/retry-failed');
			toasts.push(tr('transcode.failed.retriedAll', { n: num(res.retried) }), 'info');
			await load();
			onchanged?.();
		} catch (e) {
			toasts.push(tr('transcode.failed.retryFailed'), 'fail', { body: message(e) });
		} finally {
			retryingAll = false;
		}
	}

	function attemptsText(j: FailedJob): string {
		if (!j.attempts) return '';
		if (j.max_attempts) {
			return tr('transcode.failed.attemptsOf', { n: num(j.attempts), max: num(j.max_attempts) });
		}
		return tr('transcode.failed.attempt', { n: num(j.attempts) });
	}
</script>

<div class="failed-tab" data-testid="transcode-failed-tab">
	<div class="fhead-row">
		<span class="tp-note">
			{tr(total === 1 ? 'transcode.failed.count.one' : 'transcode.failed.count.other', { n: num(total) })}
		</span>
		{#if total > 0}
			<span class="tp-cap">
				<Button
					variant="secondary"
					icon="refresh"
					busy={retryingAll}
					disabled={retryingId !== null}
					onclick={retryAll}
					data-testid="transcode-retry-all"
				>{tr('transcode.failed.retryAll')}</Button>
			</span>
		{/if}
	</div>

	{#if error && jobs.length === 0}
		<LoadError hasData={false} message={plainReason(error).text} onretry={load} />
	{:else}
		<PageLoader loading={!loaded} label={tr('transcode.failed.loading')}>
			{#if total === 0}
				<EmptyState compact icon="check" title={tr('transcode.failed.none')} />
			{:else}
				<section class="tp-sheet" aria-label={tr('transcode.tabs.failed')}>
					<ul class="tp-rows">
						{#each jobs as j (j.id)}
							{@const name = itemTitle(j) || fileBaseName(j.relative_path) || tr('transcode.page.title')}
							{@const attempts = attemptsText(j)}
							{@const when = j.finished_at ?? j.updated_at}
							<li class="frow" data-testid="transcode-failed-row" data-state={j.state}>
								<div class="fmain">
									<span class="tp-art" style="--poster-ratio: 2 / 3">
										<Poster src={j.title_id ? art.get(j.title_id) : undefined} {name} />
									</span>
									<div class="finfo">
										<div class="fhead">
											{#if j.title_id}
												<a class="fname" href={to(`/library/${j.title_id}`)}>{name}</a>
											{:else}
												<span class="fname">{name}</span>
											{/if}
											<span class="pill" class:fail={j.state === 'failed'}>
												{j.state === 'cancelled' ? tr('transcode.state.cancelled') : tr('transcode.state.failed')}
											</span>
										</div>
										{#if j.relative_path}
											<p class="fpath">{j.relative_path}</p>
										{/if}
										<p class="fmeta">
											{#if attempts}<span>{attempts}</span>{/if}
											{#if when}<span>{dateTime(when)}</span>{/if}
										</p>
										{#if j.error}
											<p class="ferr">{serverWords(j.error)}</p>
										{/if}
									</div>
								</div>
								<div class="fact">
									<Button
										variant="secondary"
										size="sm"
										icon="refresh"
										busy={retryingId === j.id}
										disabled={retryingAll || retryingId === j.id}
										onclick={() => retryOne(j)}
										data-testid="transcode-retry-job"
									>{tr('transcode.failed.retry')}</Button>
								</div>
							</li>
						{/each}
					</ul>
				</section>

				{#if pages.prev !== null || pages.next !== null}
					<nav class="tp-foot" aria-label={tr('mediafiles.releaseList.pages')}>
						<Button icon="chevronLeft" disabled={pages.prev === null} onclick={() => go(pages.prev)}>{tr('mediafiles.releaseList.back')}</Button>
						<span class="tp-note">{tr('library.copies.range', { from: num(pages.from), to: num(pages.to), total: num(total) })}</span>
						<Button iconAfter="chevronRight" disabled={pages.next === null} onclick={() => go(pages.next)}>{tr('mediafiles.releaseList.more')}</Button>
					</nav>
				{/if}
			{/if}
		</PageLoader>
	{/if}
</div>

<style>
	.failed-tab { display: flex; flex-direction: column; gap: 14px; min-width: 0; }
	.fhead-row { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 8px; }
	.frow { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px 16px; min-width: 0; }
	.fmain { display: flex; align-items: flex-start; gap: 12px; min-width: 0; flex: 1 1 auto; }
	.finfo { display: flex; flex-direction: column; gap: 4px; min-width: 0; flex: 1 1 auto; }
	.fhead { display: flex; flex-wrap: wrap; align-items: center; gap: 6px 8px; min-width: 0; }
	.fname { color: var(--text); font: 600 14px/1.3 var(--font-ui); text-decoration: none; overflow-wrap: anywhere; }
	a.fname:hover { text-decoration: underline; text-underline-offset: 3px; }
	.fpath { margin: 0; color: var(--text-muted); font: var(--t-footnote); overflow-wrap: anywhere; }
	.fmeta { margin: 0; display: flex; flex-wrap: wrap; gap: 4px 10px; color: var(--text-muted); font: var(--t-footnote); font-variant-numeric: tabular-nums; }
	.ferr { margin: 2px 0 0; color: var(--fail-text, var(--fail)); font: var(--t-footnote); overflow-wrap: anywhere; word-break: break-word; }
	.fact { flex: none; display: flex; align-items: center; }
	.pill { flex: none; display: inline-flex; align-items: center; height: 22px; padding: 0 8px; border-radius: 999px; font: 600 11.5px/1 var(--font-ui); background: color-mix(in srgb, var(--text) 8%, transparent); color: color-mix(in srgb, var(--text) 82%, var(--bg)); }
	.pill.fail { background: color-mix(in srgb, var(--fail) 16%, transparent); color: var(--fail-text, var(--fail)); }

	@media (max-width: 639px) {
		.frow { flex-direction: column; align-items: stretch; gap: 10px; }
		.fact { justify-content: flex-end; }
	}
</style>
