<script lang="ts">
	// Subtitles: what is missing and what can make it (a site, the translator, or speech to text), the sites' state, and the subtitle jobs at work.
	import PageLoader from '#lib/ui/PageLoader.svelte';
	import Poster from '#lib/ui/Poster.svelte';
	import Button from '#lib/ui/Button.svelte';
	import EmptyState from '#lib/ui/EmptyState.svelte';
	import LoadError from '#lib/ui/LoadError.svelte';
	import ProgressBar from '#lib/ui/ProgressBar.svelte';
	import SegmentedControl from '#lib/ui/SegmentedControl.svelte';
	import Icon from '#lib/ui/Icon.svelte';
	import { titleArt, pager, LibraryToolPage } from '#lib/mediafiles/index.ts';
	import {
		doneParts, howWord, jobName, jobShare, jobWord, langName, missingName, missingNeedQuery, missingWords, nextStep, siteState, siteUse, siteUseWords, speechState, stepWords, waitingMore, wantLabel, workingCount,
		type Language, type Need, type NeedRow, type Overview, type SiteKind, type SiteView
	} from '#lib/subtitles/index.ts';
	import { onMount } from 'svelte';
	import { to } from '#lib/core/paths.ts';
	import { api, message } from '#lib/core/api.ts';
	import { dateTime } from '#lib/core/format.ts';
	import { live } from '#lib/core/live.svelte.ts';
	import { session } from '#lib/core/session.svelte.ts';
	import { toasts } from '#lib/core/toasts.svelte.ts';
	import { plainReason } from '#lib/core/plain.ts';
	import { appLocale, t as tr } from '#lib/core/i18n/i18n.svelte.ts';

	const limit = 50;
	let over = $state<Overview | null>(null);
	let sites = $state<SiteView[]>([]);
	let kinds = $state<SiteKind[]>([]);
	let langs = $state<Language[]>([]);
	let rows = $state<NeedRow[]>([]);
	let total = $state(0);
	let offset = $state(0);
	let need = $state<Need>('');
	let language = $state('');
	let loaded = $state(false);
	let error = $state('');
	let busyRow = $state('');
	let searchingAll = $state(false);
	let pausing = $state(false);
	let art = $state<Map<number, string>>(new Map());

	const num = (n: number) => n.toLocaleString(appLocale());
	const pages = $derived(pager(total, limit, offset));
	const work = $derived(over ? workingCount(over) : { running: 0, queued: 0 });
	const more = $derived(over ? waitingMore(over) : 0);
	const speech = $derived(over ? speechState(over) : 'notHere');
	const canSettings = $derived(session.can('settings'));
	const needOptions = $derived([
		{ value: '' as Need, label: tr('subtitles.page.need.all') },
		{ value: 'speech' as Need, label: tr('subtitles.page.job.transcribe') },
		{ value: 'text' as Need, label: tr('subtitles.page.need.text') }
	]);

	// Only the newest read may write the page.
	let seq = 0;
	async function load() {
		const n = ++seq;
		const [o, list, st] = await Promise.allSettled([
			api.get<Overview>('/subtitles/overview'),
			api.get<{ missing: NeedRow[]; total: number }>('/subtitles/missing?' + missingNeedQuery({ need, language }, limit, offset)),
			api.get<{ sites: SiteView[] }>('/subtitle-sites')
		]);
		if (n !== seq) return;
		if (o.status === 'rejected') {
			error = message(o.reason);
		} else {
			over = o.value;
			error = '';
		}
		if (list.status === 'fulfilled') {
			rows = list.value.missing;
			total = list.value.total;
		}
		if (st.status === 'fulfilled') sites = st.value.sites;
		loaded = true;
	}

	// Jobs move often; only the overview is read again for them.
	async function loadJobs() {
		try {
			const o = await api.get<Overview>('/subtitles/overview');
			over = o;
		} catch {
			// The next tick tries again.
		}
	}

	onMount(() => {
		void load();
		void titleArt().then((m) => (art = m));
		api.get<{ languages: Language[] }>('/subtitles/languages').then((r) => (langs = r.languages), () => {});
		api.get<{ kinds: SiteKind[] }>('/subtitle-sites/kinds').then((r) => (kinds = r.kinds), () => {});
		let timer: ReturnType<typeof setTimeout> | undefined;
		const soon = () => {
			clearTimeout(timer);
			timer = setTimeout(load, 600);
		};
		let jobTimer: ReturnType<typeof setTimeout> | undefined;
		const jobsSoon = () => {
			if (jobTimer) return;
			jobTimer = setTimeout(() => {
				jobTimer = undefined;
				void loadJobs();
			}, 1000);
		};
		// While something runs, the page keeps itself fresh even if the live stream is quiet.
		const tick = setInterval(() => {
			if (work.running + work.queued > 0) void loadJobs();
		}, 5000);
		const offs = [
			live.on('job', (d) => {
				const j = d as { kind?: string; Kind?: string; state?: string; State?: string };
				const kind = String(j?.kind ?? j?.Kind ?? '');
				if (!kind.startsWith('subtitle_')) return;
				const state = String(j?.state ?? j?.State ?? '');
				if (['done', 'failed', 'cancelled'].includes(state)) soon();
				else jobsSoon();
			}),
			live.on('event', (d) => {
				if (String((d as { Kind?: string })?.Kind).startsWith('subtitle_')) soon();
			}),
			live.on('resync', soon)
		];
		return () => {
			clearTimeout(timer);
			clearTimeout(jobTimer);
			clearInterval(tick);
			offs.forEach((off) => off());
		};
	});

	function filter() {
		offset = 0;
		void load();
	}

	function pick(n: Need) {
		need = n;
		filter();
	}

	function go(off: number | null) {
		if (off === null) return;
		offset = off;
		void load();
	}

	const rowKey = (r: NeedRow) => `${r.file_id}:${r.language}:${r.forced}`;

	async function searchAll() {
		if (searchingAll) return;
		searchingAll = true;
		try {
			const body: Record<string, unknown> = {};
			if (language) body.language = language;
			const r = await api.post<{ queued: number }>('/subtitles/missing/search', body);
			toasts.push(tr('shows.sheet.searchingSubs'), 'info', { body: tr('mediafiles.unit.file.' + (r.queued === 1 ? 'one' : 'other'), { n: r.queued }) });
			void loadJobs();
		} catch (e) {
			toasts.push(tr('mediafiles.title.startFailed'), 'fail', { body: message(e) });
		} finally {
			searchingAll = false;
		}
	}

	// Pause holds searches, translations and speech to text in the queue; what runs now ends by itself.
	async function togglePause() {
		if (!over || pausing) return;
		pausing = true;
		try {
			const r = await api.put<{ paused: boolean }>('/subtitles/pause', { paused: !over.paused });
			over = { ...over, paused: r.paused };
			void loadJobs();
		} catch (e) {
			toasts.push(tr('subtitles.page.pauseFailed'), 'fail', { body: message(e) });
		} finally {
			pausing = false;
		}
	}

	async function searchOne(r: NeedRow) {
		busyRow = rowKey(r);
		try {
			await api.post('/subtitles/search', { file_id: r.file_id });
			toasts.push(tr('shows.sheet.searchingSubs'), 'info', { body: missingName(r) });
			void loadJobs();
		} catch (e) {
			toasts.push(tr('mediafiles.title.startFailed'), 'fail', { body: message(e) });
		} finally {
			busyRow = '';
		}
	}

	async function transcribe(r: NeedRow) {
		busyRow = rowKey(r);
		try {
			await api.post(`/files/${r.file_id}/subtitles/transcribe`);
			toasts.push(tr('subtitles.panel.transcribing'), 'info', { body: missingName(r) });
			void loadJobs();
		} catch (e) {
			toasts.push(tr('subtitles.panel.transcribeFailed'), 'fail', { body: message(e) });
		} finally {
			busyRow = '';
		}
	}
</script>

<svelte:head><title>{tr('subtitles.page.title')} · HolyArrs</title></svelte:head>

<LibraryToolPage title={tr('subtitles.page.title')} count={over ? tr('subtitles.page.count', { n: num(over.missing) }) : ''}>
	{#snippet bar()}
		<span class="tp-gap"></span>
		{#if canSettings}
			<span class="tp-cap"><Button icon="settings" href={to('/settings/subtitles')} data-testid="subtitles-settings">{tr('subtitles.page.settings')}</Button></span>
		{/if}
		{#if over}
			<span class="tp-cap"><Button icon={over.paused ? 'play' : 'pause'} onclick={togglePause} busy={pausing} aria-pressed={over.paused} data-testid="subtitles-pause">{over.paused ? tr('subtitles.page.resume') : tr('subtitles.page.pause')}</Button></span>
		{/if}
		<span class="tp-cap">
			<Button variant="primary" icon="search" onclick={searchAll} disabled={!over || over.missing === 0} busy={searchingAll} data-testid="subtitles-search-all">{tr('library.missingSubs.searchAll')}</Button>
		</span>
	{/snippet}

	{#if error && !over}
		<LoadError hasData={false} message={plainReason(error).text} onretry={load} />
	{:else}
	<PageLoader loading={!loaded} label={tr('subtitles.page.loading')}>
	{#if over}
		{#if over.paused}<p class="tp-msg" role="status" data-testid="subtitles-paused"><Icon name="pause" size={16} />{tr('subtitles.page.pausedNote')}</p>{/if}
		<div class="tiles" data-testid="subtitles-tiles">
			<button type="button" class="tile tp-sheet" class:on={need === ''} aria-pressed={need === ''} onclick={() => pick('')} data-testid="tile-missing">
				<span class="lab"><Icon name="subtitle" size={16} />{tr('subtitles.page.tile.missing')}</span>
				<span class="n">{num(over.missing)}</span>
				<span class="note">{tr('subtitles.page.tile.files', { n: num(over.files) })}</span>
			</button>
			<button type="button" class="tile tp-sheet" class:on={need === 'speech'} aria-pressed={need === 'speech'} onclick={() => pick('speech')} data-testid="tile-speech">
				<span class="lab"><Icon name="music" size={16} />{tr('subtitles.page.tile.speech')}</span>
				<span class="n">{num(over.need_speech)}</span>
				<span class="note" class:warn={speech !== 'auto'}>{tr(`subtitles.page.speech.${speech}`)}{#if over.speech_held > 0} {tr('subtitles.page.speech.held', { n: num(over.speech_held) })}{/if}</span>
			</button>
			<button type="button" class="tile tp-sheet" class:on={need === 'text'} aria-pressed={need === 'text'} onclick={() => pick('text')} data-testid="tile-translate">
				<span class="lab"><Icon name="sparkle" size={16} />{tr('subtitles.page.tile.translate')}</span>
				<span class="n">{num(over.can_translate)}</span>
				<span class="note" class:warn={!over.translator_ready}>{over.translator_ready ? tr('subtitles.page.translate.with', { name: over.translator_name }) : tr('subtitles.page.translate.none')}</span>
			</button>
			<div class="tile tp-sheet still" data-testid="tile-working">
				<span class="lab"><Icon name="clock" size={16} />{tr('subtitles.page.tile.working')}</span>
				<span class="n">{num(work.running)}</span>
				<span class="note">{tr('subtitles.page.tile.waiting', { n: num(work.queued) })}</span>
			</div>
		</div>

		<div class="cols">
			<section class="tp-sheet" aria-labelledby="sub-jobs">
				<h2 id="sub-jobs" class="head">{tr('subtitles.page.tile.working')}</h2>
				{#if over.jobs.length === 0}
					<p class="tp-note pad" data-testid="subtitles-no-jobs">{tr('subtitles.page.jobs.none')}</p>
				{:else}
					<ul class="tp-rows" aria-live="polite">
						{#each over.jobs as j (j.id)}
							{@const share = jobShare(j)}
							{@const name = jobName(j)}
							<li class="job" data-testid="subtitles-job">
								<div class="jtop">
									<span class="jkind">{jobWord(j.kind)}{#if j.language} · {langName(j.language, langs)}{/if}</span>
									<span class="jstate" class:run={share !== null}>{share === null ? tr('subtitles.page.jobs.waiting') : `${share}%`}</span>
								</div>
								{#if name}
									{#if j.title_id}<a class="jname" href={to(`/library/${j.title_id}`)}>{name}</a>{:else}<span class="jname">{name}</span>{/if}
								{/if}
								{#if share !== null}<ProgressBar value={share} size="sm" label={tr('subtitles.page.jobs.progress', { name: name || jobWord(j.kind) })} />{/if}
							</li>
						{/each}
					</ul>
					{#if more > 0}<p class="tp-note pad">{tr('subtitles.page.jobs.more', { n: num(more) })}</p>{/if}
				{/if}
			</section>

			<section class="tp-sheet" aria-labelledby="sub-sites">
				<h2 id="sub-sites" class="head">{tr('subtitles.page.sites.title')}</h2>
				{#if sites.length === 0}
					<p class="tp-note pad" data-testid="subtitles-no-sites">{tr('subtitles.page.sites.none')}</p>
				{:else}
					<ul class="tp-rows">
						{#each sites as s (s.ID)}
							{@const st = siteState(s, kinds.find((k) => k.kind === s.kind), dateTime)}
							{@const use = siteUse(s)}
							<li class="site" data-testid="subtitles-site">
								<div class="jtop">
									<span class="jname">{s.name}</span>
									<span class="pill {st.tone}">{st.label}</span>
								</div>
								<p class="tp-note">{st.text}</p>
								<p class="tp-note">{siteUseWords(s)}</p>
								{#if use.cap > 0}<ProgressBar value={use.percent} size="sm" tone={use.percent >= 100 ? 'info' : 'accent'} label={tr('subtitles.page.sites.usage', { name: s.name })} />{/if}
							</li>
						{/each}
					</ul>
				{/if}
			</section>
		</div>

		<section class="tp-sheet" aria-labelledby="sub-done" data-testid="subtitles-done">
			<div class="dhead">
				<h2 id="sub-done" class="head">{tr('subtitles.page.done.title')}</h2>
				<span class="dcounts">
					{#each doneParts(over.done_counts) as p (p.how)}<span class="pill" data-testid="subtitles-done-count" data-how={p.how}>{howWord(p.how)} {num(p.n)}</span>{/each}
				</span>
			</div>
			{#if over.done.length === 0}
				<p class="tp-note pad" data-testid="subtitles-no-done">{tr('subtitles.page.done.none')}</p>
			{:else}
				<div class="table-wrap">
					<table class="tp-table done">
						<caption class="sr-only">{tr('subtitles.page.done.title')}</caption>
						<thead><tr><th scope="col">{tr('mediafiles.list.title')}</th><th scope="col">{tr('library.missingSubs.language')}</th><th scope="col">{tr('subtitles.page.done.how')}</th><th scope="col">{tr('subtitles.page.done.when')}</th></tr></thead>
						<tbody>
							{#each over.done as d (d.id)}
								<tr data-testid="subtitles-done-row" data-how={d.how}>
									<td><span class="tp-who"><span class="tp-art" style="--poster-ratio: 2 / 3"><Poster src={art.get(d.title_id)} name={missingName(d)} /></span><a href={to(`/library/${d.title_id}`)}>{missingName(d)}</a></span></td>
									<td>{wantLabel({ language: d.language, hearing_impaired: d.hearing_impaired ? 'hi' : 'normal', forced: d.forced }, langs)}</td>
									<td><span class="step">{howWord(d.how)}</span>{#if d.by}<span class="tp-sub">{d.by}</span>{/if}</td>
									<td class="status">{dateTime(d.saved_at)}</td>
								</tr>
							{/each}
						</tbody>
					</table>
				</div>
			{/if}
		</section>

		<div class="listbar">
			<SegmentedControl options={needOptions} bind:value={need} label={tr('subtitles.page.need.label')} onchange={filter} testid="subtitles-need" />
			<span class="tp-cap">
				<select aria-label={tr('library.missingSubs.language')} bind:value={language} onchange={filter}>
					<option value="">{tr('library.missingSubs.every')}</option>
					{#each langs as l (l.code)}<option value={l.code}>{l.name}</option>{/each}
				</select>
			</span>
		</div>

		{#if total === 0}
			<EmptyState compact icon="subtitle" title={need || language ? tr('library.copies.nothingMatches') : tr('mediafiles.list.nothingMissing')} />
		{:else}
			<div class="tp-sheet table-wrap">
				<table class="tp-table missing">
					<caption class="sr-only">{tr('library.missingSubs.caption')}</caption>
					<thead><tr><th scope="col">{tr('mediafiles.list.title')}</th><th scope="col">{tr('library.missingSubs.language')}</th><th scope="col">{tr('subtitles.page.next')}</th><th scope="col"><span class="sr-only">{tr('mediafiles.dialogs.search')}</span></th></tr></thead>
					<tbody>
						{#each rows as r (rowKey(r))}
							{@const step = nextStep(r, over)}
							<tr data-testid="subtitles-missing-row">
								<td><span class="tp-who"><span class="tp-art" style="--poster-ratio: 2 / 3"><Poster src={art.get(r.title_id)} name={missingName(r)} /></span><a href={to(`/library/${r.title_id}`)}>{missingName(r)}</a></span></td>
								<td>{wantLabel({ language: r.language, hearing_impaired: r.hearing_impaired ? 'hi' : 'normal', forced: r.forced }, langs)}</td>
								<td class="status"><span class="step" class:warn={step === 'speechOff' || step === 'speechFinal' || step === 'speechFailed'} data-testid="subtitles-step" data-step={step}>{stepWords(step, r.speech_reason)}</span><span class="tp-sub">{missingWords(r, dateTime)}</span></td>
								<td class="act">
									{#if !r.has_text && over.transcriber.ready}
										<Button variant="plain" size="sm" disabled={busyRow === rowKey(r)} onclick={() => transcribe(r)} data-testid="subtitles-transcribe">{tr('subtitles.panel.makeFromAudio')}</Button>
									{/if}
									<Button
										icon="search"
										onclick={() => searchOne(r)}
										disabled={busyRow === rowKey(r)}
										aria-label={tr('library.missingSubs.searchFor', { language: langName(r.language, langs), name: missingName(r) })}
										data-testid="subtitles-search">{tr('mediafiles.dialogs.search')}</Button
									>
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
	{/if}
	</PageLoader>
	{/if}
</LibraryToolPage>

<style>
	.tiles { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 14px; }
	.tile { display: flex; flex-direction: column; align-items: flex-start; gap: 6px; padding: 16px 20px; border: 0; color: var(--text); text-align: start; font: inherit; cursor: pointer; transition: transform var(--dur-fast) var(--ease-out), box-shadow var(--dur-fast) var(--ease-out); }
	.tile.still { cursor: default; }
	.tile:not(.still):hover { transform: translateY(-1px); }
	.tile:focus-visible { outline: none; box-shadow: var(--focus-ring); }
	.tile.on { box-shadow: var(--edge), var(--drop), inset 0 0 0 2px var(--accent); }
	.lab { display: inline-flex; align-items: center; gap: 6px; color: var(--text-muted); font: 600 12px/1.3 var(--font-ui); }
	.n { font: 700 30px/1.1 var(--font-display, var(--font-ui)); letter-spacing: -0.022em; font-variant-numeric: tabular-nums; }
	.note { color: var(--text-muted); font: var(--t-footnote); }
	.note.warn, .step.warn { color: var(--wait-text, var(--wait, var(--text-muted))); }

	.cols { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 14px; align-items: start; }
	.head { margin: 0; padding: 16px 20px 6px; font: var(--t-headline); }
	.pad { padding: 6px 20px 16px; }
	.job, .site { display: grid; gap: 6px; }
	.jtop { display: flex; align-items: baseline; justify-content: space-between; gap: 12px; min-width: 0; }
	.jkind { color: var(--text-muted); font: 600 12px/1.3 var(--font-ui); }
	.jname { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--text); font: 600 14px/1.3 var(--font-ui); text-decoration: none; }
	a.jname:hover { text-decoration: underline; text-underline-offset: 3px; }
	.jstate { flex: none; color: var(--text-muted); font: var(--t-footnote); font-variant-numeric: tabular-nums; }
	.jstate.run { color: var(--text); font-weight: 600; }
	.pill { flex: none; display: inline-flex; align-items: center; height: 22px; padding: 0 8px; border-radius: 999px; font: 600 11.5px/1 var(--font-ui); background: color-mix(in srgb, var(--text) 8%, transparent); color: var(--text-muted); }
	.pill.ok { background: color-mix(in srgb, var(--ok) 16%, transparent); color: var(--ok-text, var(--ok)); }
	.pill.wait { background: color-mix(in srgb, var(--wait, var(--accent)) 16%, transparent); color: var(--wait-text, var(--wait, var(--text))); }
	.pill.fail { background: color-mix(in srgb, var(--fail) 16%, transparent); color: var(--fail-text, var(--fail)); }

	.dhead { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 6px 12px; padding-inline-end: 20px; }
	.dcounts { display: flex; flex-wrap: wrap; gap: 6px; }
	.done { margin-top: 6px; }
	.listbar { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; margin-top: 6px; }
	.table-wrap { overflow-x: auto; }
	.status { color: var(--text-muted); font: var(--t-footnote); }
	.step { display: block; color: var(--text); }
	.act { width: 1%; text-align: right; white-space: nowrap; }
	.act :global(.ui-btn + .ui-btn) { margin-inline-start: 6px; }

	@media (max-width: 1023px) { .tiles { grid-template-columns: repeat(2, minmax(0, 1fr)); } }
	@media (max-width: 767px) { .cols { grid-template-columns: minmax(0, 1fr); } }
	@media (max-width: 639px) {
		.missing th:nth-child(3), .missing td:nth-child(3) { display: none; }
		.done th:nth-child(4), .done td:nth-child(4) { display: none; }
		.n { font-size: 24px; }
	}
	@media (pointer: coarse) { .missing .tp-who > a { display: inline-block; padding-block: 13px; } }
	@media (prefers-reduced-motion: reduce) { .tile { transition: none; } .tile:not(.still):hover { transform: none; } }
</style>
