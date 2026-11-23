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
	import { fileName, langName, missingName, wantLabel, type Language } from './api.ts';
	import { formatTimestamp, integrityIssueWord, type IntegrityRow, type IntegritySummary } from './overview.ts';

	interface IntegrityResponse {
		summary: IntegritySummary;
		issues: IntegrityRow[];
		total: number;
	}

	let {
		langs,
		art,
		fallbackSummary,
		verifying = false,
		reloadSignal = 0,
		onchanged
	}: {
		langs: Language[];
		art: Map<number, string>;
		fallbackSummary?: IntegritySummary;
		verifying?: boolean;
		reloadSignal?: number;
		onchanged?: () => void;
	} = $props();

	const limit = 50;
	let offset = $state(0);
	let summary = $state<IntegritySummary | null>(null);
	let issues = $state<IntegrityRow[]>([]);
	let total = $state(0);
	let loaded = $state(false);
	let error = $state('');
	let startingVerify = $state(false);
	let busyId = $state<number | null>(null);
	let busyKind = $state<'replace' | 'keep' | null>(null);

	const num = (n: number) => n.toLocaleString(appLocale());
	const pages = $derived(pager(total, limit, offset));
	const shownSummary = $derived(summary ?? fallbackSummary ?? { ok: 0, bad: 0, accepted: 0, unchecked: 0 });

	let seq = 0;
	async function load() {
		const n = ++seq;
		try {
			const q = new URLSearchParams({ limit: String(limit), offset: String(offset) });
			const res = await api.get<IntegrityResponse>(`/subtitles/integrity?${q.toString()}`);
			if (n !== seq) return;
			summary = res.summary;
			issues = res.issues ?? [];
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

	async function verifyAll() {
		if (startingVerify || verifying) return;
		startingVerify = true;
		try {
			const res = await api.post<{ queued: boolean }>('/subtitles/verify');
			toasts.push(res.queued ? tr('subtitles.integrity.verifyQueued') : tr('subtitles.integrity.verifyRunning'), 'info');
			onchanged?.();
		} catch (e) {
			toasts.push(tr('subtitles.integrity.verifyFailed'), 'fail', { body: message(e) });
		} finally {
			startingVerify = false;
		}
	}

	async function replaceRow(r: IntegrityRow) {
		if (busyId !== null) return;
		busyId = r.id;
		busyKind = 'replace';
		try {
			await api.post(`/subtitles/${r.id}/replace`);
			toasts.push(tr('subtitles.integrity.replaced'), 'info', { body: `${missingName(r)} · ${langName(r.language, langs)}` });
			await load();
			onchanged?.();
		} catch (e) {
			toasts.push(tr('subtitles.integrity.actionFailed'), 'fail', { body: message(e) });
		} finally {
			busyId = null;
			busyKind = null;
		}
	}

	async function keepRow(r: IntegrityRow) {
		if (busyId !== null) return;
		busyId = r.id;
		busyKind = 'keep';
		try {
			await api.post(`/subtitles/${r.id}/keep`);
			toasts.push(tr('subtitles.integrity.keptToast'), 'ok', { body: `${missingName(r)} · ${langName(r.language, langs)}` });
			await load();
			onchanged?.();
		} catch (e) {
			toasts.push(tr('subtitles.integrity.actionFailed'), 'fail', { body: message(e) });
		} finally {
			busyId = null;
			busyKind = null;
		}
	}

	function detailsLine(r: IntegrityRow): string {
		const parts: string[] = [];
		const note = r.note ? serverWords(r.note) : '';
		if (note) parts.push(note);
		if (r.last_ms !== undefined && r.video_seconds !== undefined && r.video_seconds > 0) {
			const lastText = formatTimestamp(r.last_ms);
			const videoText = formatTimestamp(r.video_seconds * 1000);
			if (lastText && videoText && !note.includes(lastText)) parts.push(`${lastText} / ${videoText}`);
		} else if (r.coverage !== undefined && Number.isFinite(r.coverage)) {
			const cov = `${Math.round(r.coverage * 100)}%`;
			if (!note.includes(cov)) parts.push(cov);
		}
		return parts.join(' · ');
	}
</script>

<div class="integrity-tab" data-testid="subtitles-integrity-tab">
	<section class="tp-sheet tp-pad sum" aria-label={tr('subtitles.tabs.integrity')} data-testid="subtitles-integrity-summary">
		<div class="sum-info">
			<div class="sum-pills">
				<span class="pill ok">{tr('subtitles.integrity.okCount', { n: num(shownSummary.ok) })}</span>
				<span class="pill" class:fail={shownSummary.bad > 0}>{tr(shownSummary.bad === 1 ? 'subtitles.integrity.badCount.one' : 'subtitles.integrity.badCount.other', { n: num(shownSummary.bad) })}</span>
				<span class="pill">{tr('subtitles.integrity.keptCount', { n: num(shownSummary.accepted) })}</span>
				<span class="pill">{tr('subtitles.integrity.uncheckedCount', { n: num(shownSummary.unchecked) })}</span>
			</div>
			<p class="tp-note">
				{shownSummary.last_checked_at
					? tr('subtitles.integrity.lastChecked', { when: dateTime(shownSummary.last_checked_at) })
					: tr('subtitles.integrity.neverChecked')}
			</p>
		</div>
		<div class="sum-act">
			<Button
				variant="secondary"
				icon="shield"
				busy={startingVerify || verifying}
				disabled={startingVerify || verifying}
				onclick={verifyAll}
				data-testid="subtitles-verify-btn"
			>{verifying ? tr('subtitles.integrity.verifying') : tr('subtitles.integrity.verify')}</Button>
		</div>
	</section>

	{#if error && issues.length === 0}
		<LoadError hasData={false} message={plainReason(error).text} onretry={load} />
	{:else}
		<PageLoader loading={!loaded} label={tr('subtitles.integrity.loading')}>
			{#if total === 0}
				<EmptyState
					compact
					icon="shield"
					title={tr('subtitles.integrity.none')}
					text={shownSummary.unchecked > 0
						? tr(shownSummary.unchecked === 1 ? 'subtitles.integrity.uncheckedHint.one' : 'subtitles.integrity.uncheckedHint.other', { n: num(shownSummary.unchecked) })
						: tr('subtitles.integrity.allClean')}
				/>
			{:else}
				<div class="tp-sheet table-wrap">
					<table class="tp-table issues">
						<caption class="sr-only">{tr('subtitles.tabs.integrity')}</caption>
						<thead>
							<tr>
								<th scope="col">{tr('mediafiles.list.title')}</th>
								<th scope="col">{tr('library.missingSubs.language')}</th>
								<th scope="col">{tr('subtitles.integrity.colIssue')}</th>
								<th scope="col"><span class="sr-only">{tr('subtitles.integrity.colActions')}</span></th>
							</tr>
						</thead>
						<tbody>
							{#each issues as r (r.id)}
								{@const detail = detailsLine(r)}
								{@const fname = fileName(r.relative_path)}
								{@const severe = r.issue === 'unreadable' || r.issue === 'empty'}
								<tr data-testid="subtitles-integrity-row" data-issue={r.issue}>
									<td class="ctitle">
										<span class="tp-who">
											<span class="tp-art" style="--poster-ratio: 2 / 3"><Poster src={art.get(r.title_id)} name={missingName(r)} /></span>
											<span class="tp-tx">
												<a href={to(`/library/${r.title_id}`)}>{missingName(r)}</a>
												{#if fname}<span class="tp-sub fname" title={r.relative_path}>{fname}</span>{/if}
											</span>
										</span>
									</td>
									<td class="clang">{wantLabel({ language: r.language, hearing_impaired: r.hearing_impaired ? 'hi' : 'normal', forced: r.forced }, langs)}</td>
									<td class="cissue">
										<span class="pill" class:fail={severe} class:wait={!severe}>{integrityIssueWord(r.issue)}</span>
										{#if detail}<span class="tp-sub note">{detail}</span>{/if}
									</td>
									<td class="act">
										<Button
											variant="secondary"
											size="sm"
											icon="refresh"
											busy={busyId === r.id && busyKind === 'replace'}
											disabled={busyId !== null}
											onclick={() => replaceRow(r)}
											data-testid="subtitles-integrity-replace"
										>{tr('subtitles.integrity.replace')}</Button>
										<Button
											variant="plain"
											size="sm"
											busy={busyId === r.id && busyKind === 'keep'}
											disabled={busyId !== null}
											onclick={() => keepRow(r)}
											data-testid="subtitles-integrity-keep"
										>{tr('subtitles.integrity.keep')}</Button>
									</td>
								</tr>
							{/each}
						</tbody>
					</table>
				</div>

				{#if pages.prev !== null || pages.next !== null}
					<nav class="tp-foot" aria-label={tr('mediafiles.releaseList.pages')}>
						<Button icon="chevronLeft" disabled={pages.prev === null} onclick={() => go(pages.prev)}>{tr('mediafiles.releaseList.back')}</Button>
						<span class="tp-note">{tr('library.copies.range', { from: pages.from, to: pages.to, total })}</span>
						<Button iconAfter="chevronRight" disabled={pages.next === null} onclick={() => go(pages.next)}>{tr('mediafiles.releaseList.more')}</Button>
					</nav>
				{/if}
			{/if}
		</PageLoader>
	{/if}
</div>

<style>
	.integrity-tab { display: flex; flex-direction: column; gap: 14px; min-width: 0; }
	.sum { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 12px 16px; }
	.sum-info { display: flex; flex-direction: column; gap: 8px; min-width: 0; }
	.sum-pills { display: flex; flex-wrap: wrap; gap: 6px; }
	.sum-act { flex: none; }
	.table-wrap { overflow-x: auto; }
	.fname, .note { overflow-wrap: anywhere; word-break: break-word; }
	.act { width: 1%; text-align: right; white-space: nowrap; }
	.act :global(.ui-btn + .ui-btn) { margin-inline-start: 6px; }
	.pill { flex: none; display: inline-flex; align-items: center; height: 22px; padding: 0 8px; border-radius: 999px; font: 600 11.5px/1 var(--font-ui); font-variant-numeric: tabular-nums; background: color-mix(in srgb, var(--text) 8%, transparent); color: color-mix(in srgb, var(--text) 82%, var(--bg)); }
	.pill.ok { background: color-mix(in srgb, var(--ok) 16%, transparent); color: var(--ok-text, var(--ok)); }
	.pill.wait { background: color-mix(in srgb, var(--wait, var(--accent)) 16%, transparent); color: var(--wait-text, var(--wait, var(--text))); }
	.pill.fail { background: color-mix(in srgb, var(--fail) 16%, transparent); color: var(--fail-text, var(--fail)); }

	@media (max-width: 639px) {
		.sum { flex-direction: column; align-items: stretch; }
		.sum-act { display: flex; justify-content: flex-end; }
		.table-wrap { overflow-x: visible; }
		.issues thead { position: absolute; width: 1px; height: 1px; overflow: hidden; clip: rect(0 0 0 0); }
		.issues, .issues tbody { display: block; width: 100%; }
		.issues tbody tr { display: grid; grid-template-columns: minmax(0, 1fr); gap: 8px; padding: 14px 16px; box-shadow: none; }
		.issues tbody tr + tr { border-top: 1px solid var(--line); }
		.issues tbody tr + tr td { box-shadow: none; }
		.issues td { display: block; padding: 0; width: auto; }
		.issues .clang { color: var(--text-muted); font: var(--t-footnote); }
		.issues .act { display: flex; flex-wrap: wrap; justify-content: flex-end; gap: 6px; white-space: normal; text-align: left; }
		.issues .act :global(.ui-btn + .ui-btn) { margin-inline-start: 0; }
	}
</style>
