import { expect, test, type Browser, type Page } from '@playwright/test';
import { mkdirSync } from 'node:fs';
import { join } from 'node:path';
import { noSidewaysScroll, openAdvanced, owner, signIn } from '../helpers/helpers.ts';
import { axe as scan } from '../helpers/axe.ts';

test.describe.configure({ mode: 'serial' });

const axe = (p: Page, where: string) => scan(p, where, { waitAll: false, include: '#main' });

function trackErrors(p: Page) {
	const ce: string[] = [];
	const pe: string[] = [];
	p.on('console', (msg) => {
		if (msg.type() === 'error') ce.push(msg.text());
	});
	p.on('pageerror', (err) => {
		pe.push(err.message);
	});
	return { ce, pe };
}

async function openContext(browser: Browser, viewport: { width: number; height: number }, theme?: 'light' | 'dark') {
	const ctx = await browser.newContext({
		viewport,
		...(theme ? { colorScheme: theme } : {})
	});
	if (theme) {
		await ctx.addInitScript((t) => localStorage.setItem('holyarrs-theme', t), theme);
	}
	const p = await ctx.newPage();
	await signIn(p, owner());
	if (theme) {
		await expect(p.locator('html')).toHaveAttribute('data-theme', theme);
	}
	await p.goto('about:blank');
	return p;
}

async function installQaRoutes(p: Page) {
	let waitingJobs = [
		{
			id: 8101,
			kind: 'subtitle_search',
			state: 'queued',
			priority: 'normal',
			position: 1,
			file_id: 201,
			title_id: 0,
			name: 'Waiting Film One',
			media_kind: 'movie',
			relative_path: 'Waiting Film One (2024)/Waiting.Film.One.2024.mkv',
			language: 'en',
			forced: false,
			created_at: '2026-01-01T00:00:00Z',
			run_after: '2026-01-01T00:00:00Z'
		},
		{
			id: 8102,
			kind: 'subtitle_transcribe',
			state: 'queued',
			priority: 'normal',
			position: 2,
			file_id: 202,
			title_id: 0,
			name: 'Waiting Film Two',
			media_kind: 'movie',
			relative_path: 'Waiting Film Two (2024)/Waiting.Film.Two.2024.mkv',
			language: 'es',
			forced: false,
			created_at: '2026-01-01T00:01:00Z',
			run_after: '2026-01-01T00:01:00Z'
		},
		{
			id: 8103,
			kind: 'subtitle_translate',
			state: 'queued',
			priority: 'normal',
			position: 3,
			file_id: 203,
			title_id: 0,
			name: 'Waiting Show',
			media_kind: 'episode',
			season: 1,
			episode: 3,
			relative_path: 'Waiting Show/Season 01/Waiting.Show.S01E03.mkv',
			language: 'fr',
			forced: false,
			created_at: '2026-01-01T00:02:00Z',
			run_after: '2026-01-01T00:02:00Z'
		}
	];

	const doneItem = {
		id: 301,
		file_id: 101,
		title_id: 0,
		title: 'Completed Movie',
		media_kind: 'movie',
		relative_path: 'Completed Movie (2024)/Completed.Movie.2024.mkv',
		language: 'es',
		forced: false,
		hearing_impaired: false,
		state: 'done',
		how: 'downloaded',
		source: 'Lab subtitle site',
		at: '2026-01-01T00:10:00Z'
	};

	const skippedItem = {
		id: 302,
		file_id: 102,
		title_id: 0,
		title: 'Skipped Episode',
		media_kind: 'episode',
		season: 1,
		episode: 4,
		relative_path: 'Skipped Show/Season 01/Skipped.Show.S01E04.mkv',
		language: 'fr',
		forced: false,
		hearing_impaired: false,
		state: 'skipped',
		how: 'skipped',
		source: '',
		at: '2026-01-01T00:12:00Z'
	};

	const failedJob = {
		id: 8003,
		kind: 'subtitle_search',
		state: 'failed',
		progress: 0,
		error: 'provider returned HTTP 503: rate limit exceeded',
		attempt: 2,
		max_attempts: 3,
		ended_at: '2026-01-01T00:05:00Z',
		file_id: 43,
		title_id: 0,
		name: 'Broken Clip',
		media_kind: 'movie',
		relative_path: 'Broken Clip (2023)/Broken.Clip.2023.mkv',
		language: 'en',
		forced: false
	};

	const missingRows = [
		{
			file_id: 42,
			item_id: 1,
			title_id: 0,
			title: 'Sample Film',
			kind: 'movie',
			language: 'en',
			forced: false,
			hearing_impaired: false,
			state: 'wanted',
			tries: 0,
			has_text: false
		},
		{
			file_id: 44,
			item_id: 2,
			title_id: 0,
			title: 'Night Runner',
			kind: 'episode',
			season: 1,
			episode: 2,
			language: 'es',
			forced: false,
			hearing_impaired: false,
			state: 'wanted',
			tries: 1,
			has_text: true
		}
	];

	await p.route('**/api/v1/discover*', (rt) =>
		rt.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ items: [] }) })
	);

	await p.route('**/api/v1/subtitles/overview', (rt) => {
		const queuedCounts: Record<string, number> = {};
		for (const j of waitingJobs) {
			queuedCounts[j.kind] = (queuedCounts[j.kind] ?? 0) + 1;
		}
		return rt.fulfill({
			status: 200,
			json: {
				missing: 4,
				files: 12,
				need_speech: 2,
				can_translate: 2,
				speech_held: 0,
				transcriber: { available: true, ready: true, model: 'medium' },
				transcribe_missing: true,
				translator_ready: true,
				translator_name: 'Built in',
				paused: false,
				done: [],
				done_counts: { downloaded: 5, translated: 2, speech: 1 },
				skipped_count: 1,
				failed_count: 1,
				cancelled_count: 0,
				library: {
					wanted: 20,
					have: 16,
					missing: 4,
					percent: 80.0,
					files_total: 12,
					files_done: 8,
					files_missing: 4,
					unlooked_files: 0
				},
				stats: {
					speech: { live_speed: 14.2, avg_speed: 12.5, unit: 'x', samples: 3 },
					translate: { live_speed: 24.0, avg_speed: 21.5, unit: 'lines/s', samples: 2 }
				},
				integrity: {
					ok: 15,
					bad: 0,
					accepted: 1,
					unchecked: 0,
					last_checked_at: '2026-01-01T00:00:00Z'
				},
				running: { subtitle_transcribe: 1, subtitle_translate: 1 },
				queued: queuedCounts,
				jobs: [
					{
						id: 8001,
						kind: 'subtitle_transcribe',
						state: 'running',
						progress: 0.62,
						phase: 'transcribing',
						speed: 14.2,
						speed_unit: 'x',
						file_id: 42,
						title_id: 0,
						name: 'Sample Film',
						media_kind: 'movie',
						relative_path: 'Sample Film (2024)/Sample.Film.2024.1080p.mkv',
						language: 'en',
						forced: false
					},
					{
						id: 8002,
						kind: 'subtitle_translate',
						state: 'running',
						progress: 0.34,
						phase: 'translating',
						speed: 24.0,
						speed_unit: 'lines/s',
						file_id: 44,
						title_id: 0,
						name: 'Night Runner',
						media_kind: 'episode',
						season: 1,
						episode: 2,
						relative_path: 'Night Runner/Season 01/Night.Runner.S01E02.1080p.mkv',
						language: 'es',
						forced: false
					}
				]
			}
		});
	});

	await p.route('**/api/v1/subtitles/missing**', (rt) =>
		rt.fulfill({
			status: 200,
			json: { missing: missingRows, total: missingRows.length }
		})
	);

	await p.route('**/api/v1/subtitle-sites', (rt) =>
		rt.request().method() === 'GET'
			? rt.fulfill({
					status: 200,
					json: {
						sites: [
							{
								ID: 1,
								name: 'Lab subtitle site',
								kind: 'fakesubs',
								url: 'http://127.0.0.1:18749',
								state: 'enabled',
								pause_reason: null,
								paused_until: null,
								failures: 0,
								limits: { gap_ms: 10, per_window: 5000, window_seconds: 60, downloads_per_day: 100 },
								downloads_today: 12,
								allow_local_network: true,
								priority: 1,
								has_account: false
							}
						]
					}
				})
			: rt.continue()
	);

	await p.route('**/api/v1/subtitles/waiting**', (rt) =>
		rt.fulfill({
			status: 200,
			json: {
				jobs: waitingJobs,
				total: waitingJobs.length,
				limit: 50,
				offset: 0
			}
		})
	);

	await p.route('**/api/v1/subtitles/jobs/*/bump', (rt) => {
		const m = /\/jobs\/(\d+)\/bump/.exec(rt.request().url());
		const id = Number(m?.[1] ?? 0);
		const idx = waitingJobs.findIndex((j) => j.id === id);
		if (idx >= 0) {
			const [picked] = waitingJobs.splice(idx, 1);
			waitingJobs.unshift({ ...picked, priority: 'now' });
			waitingJobs = waitingJobs.map((j, i) => ({ ...j, position: i + 1 }));
		}
		return rt.fulfill({ status: 200, json: { id, priority: 'now', state: 'queued' } });
	});

	await p.route('**/api/v1/subtitles/jobs/*/skip', (rt) => {
		const m = /\/jobs\/(\d+)\/skip/.exec(rt.request().url());
		const id = Number(m?.[1] ?? 0);
		const target = waitingJobs.find((j) => j.id === id);
		waitingJobs = waitingJobs
			.filter((j) => j.id !== id)
			.map((j, i) => ({ ...j, position: i + 1 }));
		return rt.fulfill({ status: 200, json: { id, skipped: true, file_id: target?.file_id ?? 0 } });
	});

	await p.route('**/api/v1/subtitles/jobs/*/stop', (rt) => {
		const m = /\/jobs\/(\d+)\/stop/.exec(rt.request().url());
		return rt.fulfill({ status: 200, json: { id: Number(m?.[1] ?? 0), state: 'cancelled' } });
	});

	await p.route('**/api/v1/subtitles/jobs/*/retry', (rt) => {
		const m = /\/jobs\/(\d+)\/retry/.exec(rt.request().url());
		return rt.fulfill({ status: 200, json: { id: Number(m?.[1] ?? 0), state: 'queued' } });
	});

	await p.route('**/api/v1/subtitles/jobs/retry-failed', (rt) =>
		rt.fulfill({ status: 200, json: { retried: 1, skipped: 0 } })
	);

	await p.route('**/api/v1/subtitles/history**', (rt) => {
		const state = new URL(rt.request().url()).searchParams.get('state') ?? 'done';
		const items = state === 'all' ? [doneItem, skippedItem] : state === 'skipped' ? [skippedItem] : [doneItem];
		return rt.fulfill({
			status: 200,
			json: {
				items,
				total: items.length,
				done_count: 1,
				skipped_count: 1,
				done_counts: { downloaded: 5, translated: 2, speech: 1 },
				limit: 50,
				offset: 0
			}
		});
	});

	await p.route('**/api/v1/subtitles/search', (rt) =>
		rt.fulfill({ status: 202, json: { queued: 1, file_ids: [101], job_ids: [8104] } })
	);

	await p.route('**/api/v1/subtitles/failed**', (rt) =>
		rt.fulfill({
			status: 200,
			json: {
				jobs: [failedJob],
				total: 1,
				failed_count: 1,
				cancelled_count: 0
			}
		})
	);

	await p.route('**/api/v1/settings/subtitles', (rt) =>
		rt.request().method() === 'GET'
			? rt.fulfill({
					status: 200,
					json: {
						languages: [
							{ language: 'en', hearing_impaired: 'normal', forced: false },
							{ language: 'es', hearing_impaired: 'normal', forced: false, translate_from: 'en' }
						],
						pass_mark_movie: 70,
						pass_mark_episode: 90,
						use_built_in: true,
						ignore_picture_tracks: false,
						hi_suffix: 'hi',
						search_hours: 6,
						better: true,
						better_hours: 12,
						better_days: 7,
						batch: 100,
						adaptive: true,
						transcribe_missing: true,
						transcribe_at_once: 2,
						transcribe_fast: false,
						transcribe_model: '',
						sync_from_fps: 25,
						sync_to_fps: 23.976,
						sync_step_ms: 500
					}
				})
			: rt.continue()
	);

	await p.route('**/api/v1/settings/subtitles/translator', (rt) =>
		rt.request().method() === 'GET'
			? rt.fulfill({
					status: 200,
					json: {
						kind: 'builtin',
						url: '',
						model: '',
						has_key: false,
						built_in_auto: false,
						builtin_model: 'nllb-200',
						threads: 0,
						gpu: '',
						builtin_available: true
					}
				})
			: rt.continue()
	);

	await p.route('**/api/v1/settings/subtitles/translator/models', (rt) =>
		rt.fulfill({
			status: 200,
			json: {
				models: [
					{
						id: 'nllb-200',
						name: 'NLLB 200',
						size: 600_000_000,
						need: 1_200_000_000,
						license: 'CC-BY-NC',
						own: true,
						state: 'present',
						progress: 1,
						fits: true,
						chosen: true,
						kind: ''
					}
				],
				devices: [{ id: 'gpu0', name: 'Sample GPU', memory_mb: 8192 }],
				total_memory: 16_384_000_000,
				threads_max: 16,
				threads_default: 4
			}
		})
	);

	await p.route('**/api/v1/settings/subtitles/transcriber', (rt) =>
		rt.fulfill({
			status: 200,
			json: { available: true, ready: true, model: 'medium' }
		})
	);
}

async function openSettingsMoreAndAdvanced(p: Page) {
	const more = p.locator('details[data-testid="more-options"]:not([open])');
	if ((await more.count()) > 0) {
		await more.evaluateAll((els) => els.forEach((d) => ((d as HTMLDetailsElement).open = true)));
	}
	await openAdvanced(p.locator('.settings-content'));
}

async function assertAlignedGridRows(p: Page, expectThree: boolean) {
	const settings = p.locator('.settings-content');
	const rows = settings.locator('.two, .three');
	const count = await rows.count();
	expect(count).toBeGreaterThan(0);

	const checked = { two: 0, three: 0 };
	for (let i = 0; i < count; i++) {
		const row = rows.nth(i);
		if (!(await row.isVisible())) continue;

		const cls = (await row.getAttribute('class')) ?? '';
		if (cls.includes('two')) checked.two++;
		if (cls.includes('three')) checked.three++;

		const fields = row.locator(':scope > .field');
		const fieldCount = await fields.count();
		expect(fieldCount).toBeGreaterThan(1);

		const firstFieldBox = await fields.nth(0).boundingBox();
		const firstInputBox = await fields.nth(0).locator('input, select').first().boundingBox();
		expect(firstFieldBox).not.toBeNull();
		expect(firstInputBox).not.toBeNull();

		for (let j = 1; j < fieldCount; j++) {
			const fieldBox = await fields.nth(j).boundingBox();
			const inputBox = await fields.nth(j).locator('input, select').first().boundingBox();
			expect(fieldBox).not.toBeNull();
			expect(inputBox).not.toBeNull();
			expect(Math.abs(fieldBox!.y - firstFieldBox!.y), `row ${i} field ${j} top y`).toBeLessThanOrEqual(2);
			expect(Math.abs(inputBox!.y - firstInputBox!.y), `row ${i} input ${j} top y`).toBeLessThanOrEqual(2);
		}
	}

	expect(checked.two).toBeGreaterThan(0);
	if (expectThree) {
		expect(checked.three).toBeGreaterThan(0);
	}
}

async function settleShot(p: Page, resetScroll = true) {
	await p.evaluate(async (doReset) => {
		const hl = document.getElementById('hl');
		if (hl) {
			await Promise.all(
				hl.getAnimations({ subtree: true })
					.filter((a) => a.effect?.getComputedTiming().iterations !== Infinity)
					.map((a) => a.finished.catch(() => undefined))
			);
		}
		await Promise.all(
			document.getAnimations()
				.filter((a) => a.effect?.getComputedTiming().iterations !== Infinity)
				.map((a) => a.finished.catch(() => undefined))
		);
		if (doReset) window.scrollTo(0, 0);
	}, resetScroll);
	await p.waitForFunction(() => {
		const hl = document.getElementById('hl');
		if (!hl) return true;
		const cs = getComputedStyle(hl);
		return cs.display === 'none' || cs.visibility === 'hidden' || parseFloat(cs.opacity) <= 0.01;
	});
}

test('Active, Waiting, History & Failed tabs: 2-col active jobs, sidebar ring, waiting Bump/Skip, history All, and failed Retry', async ({ browser }) => {
	const qaPage = await openContext(browser, { width: 1440, height: 900 }, 'dark');
	const { ce: qaConsoleErrors, pe: qaPageErrors } = trackErrors(qaPage);
	await installQaRoutes(qaPage);

	await qaPage.goto('/subtitles');
	await expect(qaPage.getByTestId('subtitles-stats')).toBeVisible();
	const jobsCard = qaPage.getByTestId('subtitles-jobs');
	await expect(jobsCard).toBeVisible();
	const jobs = jobsCard.getByTestId('subtitles-job');
	await expect(jobs).toHaveCount(2);

	const job0 = jobs.nth(0);
	await expect(job0).toContainText('Sample Film');
	await expect(job0).toContainText('Transcribing speech');
	await expect(job0).toContainText('14.2x');
	await expect(job0).toContainText('62%');
	await expect(job0).toContainText('Speech to text');
	await expect(job0).toContainText('English');
	await expect(job0.getByRole('progressbar')).toBeVisible();

	const job1 = jobs.nth(1);
	await expect(job1).toContainText('Night Runner S01E02');
	await expect(job1).toContainText('Translating lines');
	await expect(job1).toContainText('24 lines/s');
	await expect(job1).toContainText('34%');
	await expect(job1).toContainText('Translation');
	await expect(job1).toContainText('Spanish');
	await expect(job1.getByRole('progressbar')).toBeVisible();

	const box0 = await job0.boundingBox();
	const box1 = await job1.boundingBox();
	expect(box0).not.toBeNull();
	expect(box1).not.toBeNull();
	expect(Math.abs(box0!.y - box1!.y)).toBeLessThanOrEqual(2);
	expect(box1!.x).toBeGreaterThan(box0!.x);

	const gotoWaiting = jobsCard.getByTestId('subtitles-goto-waiting');
	await expect(gotoWaiting).toBeVisible();
	await expect(gotoWaiting).toContainText('3 waiting');

	const nav = qaPage.getByRole('navigation', { name: 'Main' });
	const navSubtitles = nav.locator('a[href="/subtitles"]');
	await expect(navSubtitles.getByTestId('nav-ring')).toBeVisible();
	await expect(navSubtitles.getByTestId('nav-subtitles')).toHaveText('5');

	await noSidewaysScroll(qaPage);
	await axe(qaPage, 'subtitles active desktop dark');

	const stopBtn = job0.getByTestId('subtitles-job-stop');
	await expect(stopBtn).toBeEnabled();
	const stopReq = qaPage.waitForResponse((r) => r.url().includes('/api/v1/subtitles/jobs/8001/stop') && r.request().method() === 'POST');
	await stopBtn.click();
	expect((await stopReq).status()).toBe(200);
	await expect(qaPage.getByRole('status').filter({ hasText: 'Job stopped' })).toBeVisible();

	const waitListReq = qaPage.waitForResponse((r) => r.url().includes('/api/v1/subtitles/waiting') && r.request().method() === 'GET');
	await gotoWaiting.click();
	expect((await waitListReq).status()).toBe(200);
	await expect(qaPage).toHaveURL(/\/subtitles\?tab=waiting$/);
	await expect(qaPage.getByTestId('subtitles-waiting-tab')).toBeVisible();

	const waitRows = qaPage.getByTestId('subtitles-waiting-row');
	await expect(waitRows).toHaveCount(3);
	await expect(waitRows.nth(0)).toContainText('#1');
	await expect(waitRows.nth(1)).toContainText('#2');
	await expect(waitRows.nth(2)).toContainText('#3');

	const bumpReq = qaPage.waitForResponse((r) => r.url().includes('/api/v1/subtitles/jobs/8102/bump') && r.request().method() === 'POST');
	const waitReloadAfterBump = qaPage.waitForResponse((r) => r.url().includes('/api/v1/subtitles/waiting') && r.request().method() === 'GET');
	await waitRows.nth(1).getByTestId('subtitles-bump-job').click();
	expect((await bumpReq).status()).toBe(200);
	expect((await waitReloadAfterBump).status()).toBe(200);
	await expect(qaPage.getByRole('status').filter({ hasText: 'Moved to top of queue' })).toBeVisible();

	await expect(waitRows).toHaveCount(3);
	await expect(waitRows.nth(0)).toHaveAttribute('data-priority', 'now');
	await expect(waitRows.nth(0)).toContainText('#1');
	await expect(waitRows.nth(0)).toContainText('Waiting Film Two');
	await expect(waitRows.nth(0)).toContainText('Priority');
	await expect(waitRows.nth(0).getByTestId('subtitles-bump-job')).toBeDisabled();

	await noSidewaysScroll(qaPage);
	await axe(qaPage, 'subtitles waiting desktop dark');

	const skipReq = qaPage.waitForResponse((r) => r.url().includes('/api/v1/subtitles/jobs/8101/skip') && r.request().method() === 'POST');
	const waitReloadAfterSkip = qaPage.waitForResponse((r) => r.url().includes('/api/v1/subtitles/waiting') && r.request().method() === 'GET');
	await waitRows.nth(1).getByTestId('subtitles-skip-job').click();
	expect((await skipReq).status()).toBe(200);
	expect((await waitReloadAfterSkip).status()).toBe(200);
	await expect(qaPage.getByRole('status').filter({ hasText: 'Job skipped' })).toBeVisible();
	await expect(waitRows).toHaveCount(2);

	const qaHistReq = qaPage.waitForResponse((r) => r.url().includes('/api/v1/subtitles/history') && r.request().method() === 'GET');
	await qaPage.getByTestId('subtitles-tab-history').click();
	expect((await qaHistReq).status()).toBe(200);
	await expect(qaPage).toHaveURL(/\/subtitles\?tab=history$/);
	await expect(qaPage.getByTestId('subtitles-history-tab')).toBeVisible();

	const histAllReq = qaPage.waitForResponse((r) => r.url().includes('/api/v1/subtitles/history') && r.url().includes('state=all') && r.request().method() === 'GET');
	await qaPage.getByTestId('subtitles-history-filter').getByRole('radio', { name: 'All', exact: true }).click();
	expect((await histAllReq).status()).toBe(200);

	const histRows = qaPage.getByTestId('subtitles-history-row');
	await expect(histRows).toHaveCount(2);
	const doneRow = qaPage.locator('[data-testid="subtitles-history-row"][data-state="done"]');
	await expect(doneRow).toContainText('Completed Movie');
	await expect(doneRow).toContainText('Downloaded');
	await expect(doneRow.getByTestId('subtitles-history-retry')).toHaveText('Search again');
	const skippedRow = qaPage.locator('[data-testid="subtitles-history-row"][data-state="skipped"]');
	await expect(skippedRow).toContainText('Skipped Episode');
	await expect(skippedRow.getByTestId('subtitles-history-retry')).toHaveText('Search again');

	await noSidewaysScroll(qaPage);
	await axe(qaPage, 'subtitles history desktop dark');

	const qaFailReq = qaPage.waitForResponse((r) => r.url().includes('/api/v1/subtitles/failed') && r.request().method() === 'GET');
	await qaPage.getByTestId('subtitles-tab-failed').click();
	expect((await qaFailReq).status()).toBe(200);
	await expect(qaPage).toHaveURL(/\/subtitles\?tab=failed$/);
	const failedRow = qaPage.getByTestId('subtitles-failed-row').first();
	await expect(failedRow).toBeVisible();
	await expect(failedRow).toContainText('Search · English');
	await expect(failedRow).toContainText('provider returned HTTP 503: rate limit exceeded');
	await expect(failedRow.getByTestId('subtitles-job-retry')).toBeVisible();
	await expect(failedRow.getByTestId('subtitles-job-retry')).toBeEnabled();
	await expect(qaPage.getByTestId('subtitles-retry-all')).toBeVisible();
	await expect(qaPage.getByTestId('subtitles-retry-all')).toBeEnabled();

	await noSidewaysScroll(qaPage);
	await axe(qaPage, 'subtitles failed desktop dark');

	expect(qaConsoleErrors).toEqual([]);
	expect(qaPageErrors).toEqual([]);
	await qaPage.context().close();
});

async function verifyDesktopTheme(browser: Browser, theme: 'dark' | 'light', dir: string | undefined, includeHistoryAndFailed: boolean) {
	const desk = await openContext(browser, { width: 1440, height: 900 }, theme);
	const { ce, pe } = trackErrors(desk);
	await installQaRoutes(desk);

	await desk.goto('/subtitles');
	await expect(desk.getByTestId('subtitles-stats')).toBeVisible();
	await expect(desk.getByTestId('subtitles-job')).toHaveCount(2);
	await expect(desk.getByRole('navigation', { name: 'Main' }).locator('a[href="/subtitles"]').getByTestId('nav-ring')).toBeVisible();
	if (dir) {
		await settleShot(desk);
		await desk.screenshot({ path: join(dir, `subtitles-active-desktop-${theme}.png`), fullPage: true, animations: 'disabled' });
	}
	await noSidewaysScroll(desk);
	await axe(desk, `subtitles active desktop ${theme}`);

	await desk.goto('/subtitles?tab=waiting');
	await expect(desk.getByTestId('subtitles-waiting-tab')).toBeVisible();
	await expect(desk.getByTestId('subtitles-waiting-row')).toHaveCount(3);
	if (dir) {
		await settleShot(desk);
		await desk.screenshot({ path: join(dir, `subtitles-waiting-desktop-${theme}.png`), fullPage: true, animations: 'disabled' });
	}
	await noSidewaysScroll(desk);
	await axe(desk, `subtitles waiting desktop ${theme}`);

	if (includeHistoryAndFailed) {
		await desk.goto('/subtitles?tab=history');
		await expect(desk.getByTestId('subtitles-history-tab')).toBeVisible();
		const histAllReq = desk.waitForResponse((r) => r.url().includes('/api/v1/subtitles/history') && r.url().includes('state=all') && r.request().method() === 'GET');
		await desk.getByTestId('subtitles-history-filter').getByRole('radio', { name: 'All', exact: true }).click();
		expect((await histAllReq).status()).toBe(200);
		await expect(desk.getByTestId('subtitles-history-row')).toHaveCount(2);
		if (dir) {
			await settleShot(desk);
			await desk.screenshot({ path: join(dir, `subtitles-history-desktop-${theme}.png`), fullPage: true, animations: 'disabled' });
		}
		await noSidewaysScroll(desk);
		await axe(desk, `subtitles history desktop ${theme}`);

		await desk.goto('/subtitles?tab=failed');
		await expect(desk.getByTestId('subtitles-failed-tab')).toBeVisible();
		await expect(desk.getByTestId('subtitles-failed-row').first()).toBeVisible();
		if (dir) {
			await settleShot(desk);
			await desk.screenshot({ path: join(dir, `subtitles-failed-desktop-${theme}.png`), fullPage: true, animations: 'disabled' });
		}
		await noSidewaysScroll(desk);
		await axe(desk, `subtitles failed desktop ${theme}`);
	}

	await desk.goto('/settings/subtitles');
	await expect(desk.locator('#lang-card')).toBeVisible();
	await expect(desk.locator('#translator-card')).toBeVisible();
	await expect(desk.locator('#tr-gpu')).toBeVisible();
	await expect(desk.locator('#tr-threads')).toBeVisible();
	if (dir) {
		await settleShot(desk);
		await desk.screenshot({ path: join(dir, `subtitles-settings-desktop-${theme}.png`), fullPage: true, animations: 'disabled' });
	}
	await noSidewaysScroll(desk);
	await axe(desk, `subtitles settings desktop ${theme}`);
	await assertAlignedGridRows(desk, false);

	await openSettingsMoreAndAdvanced(desk);
	await assertAlignedGridRows(desk, true);
	expect(ce).toEqual([]);
	expect(pe).toEqual([]);
	await desk.context().close();
}

test('Desktop responsive & accessibility: 1440x900 dark & light Settings alignment, Active, Waiting, History, Failed, and axe', async ({ browser }) => {
	const dir = process.env.SUBTITLES_QA_SHOTS;
	if (dir) mkdirSync(dir, { recursive: true });
	await verifyDesktopTheme(browser, 'dark', dir, true);
	await verifyDesktopTheme(browser, 'light', dir, false);
});

test('Phone responsive & accessibility: 390x844 dark Active (1-col), Waiting, History, Settings, and axe', async ({ browser }) => {
	const dir = process.env.SUBTITLES_QA_SHOTS;
	if (dir) mkdirSync(dir, { recursive: true });

	const phoneDark = await openContext(browser, { width: 390, height: 844 }, 'dark');
	const { ce: phoneCe, pe: phonePe } = trackErrors(phoneDark);
	await installQaRoutes(phoneDark);

	await phoneDark.goto('/subtitles');
	await expect(phoneDark.getByTestId('subtitles-stats')).toBeVisible();
	const phoneJobs = phoneDark.getByTestId('subtitles-job');
	await expect(phoneJobs).toHaveCount(2);
	const pBox0 = await phoneJobs.nth(0).boundingBox();
	const pBox1 = await phoneJobs.nth(1).boundingBox();
	expect(pBox0).not.toBeNull();
	expect(pBox1).not.toBeNull();
	expect(Math.abs(pBox0!.x - pBox1!.x)).toBeLessThanOrEqual(2);
	expect(pBox1!.y).toBeGreaterThan(pBox0!.y);
	if (dir) {
		await settleShot(phoneDark);
		await phoneDark.screenshot({ path: join(dir, 'subtitles-active-phone-dark.png'), fullPage: false, animations: 'disabled' });
		await phoneDark.getByTestId('subtitles-jobs').scrollIntoViewIfNeeded();
		await settleShot(phoneDark, false);
		await phoneDark.screenshot({ path: join(dir, 'subtitles-active-jobs-phone-dark.png'), fullPage: false, animations: 'disabled' });
		await phoneDark.evaluate(() => window.scrollTo(0, 0));
	}
	await noSidewaysScroll(phoneDark);
	await axe(phoneDark, 'subtitles active phone dark');

	await phoneDark.goto('/subtitles?tab=waiting');
	await expect(phoneDark.getByTestId('subtitles-waiting-tab')).toBeVisible();
	await expect(phoneDark.getByTestId('subtitles-waiting-row')).toHaveCount(3);
	if (dir) {
		await settleShot(phoneDark);
		await phoneDark.screenshot({ path: join(dir, 'subtitles-waiting-phone-dark.png'), fullPage: false, animations: 'disabled' });
	}
	await noSidewaysScroll(phoneDark);
	await axe(phoneDark, 'subtitles waiting phone dark');

	await phoneDark.goto('/subtitles?tab=history');
	await expect(phoneDark.getByTestId('subtitles-history-tab')).toBeVisible();
	const phoneHistAllReq = phoneDark.waitForResponse((r) => r.url().includes('/api/v1/subtitles/history') && r.url().includes('state=all') && r.request().method() === 'GET');
	await phoneDark.getByTestId('subtitles-history-filter').getByRole('radio', { name: 'All', exact: true }).click();
	expect((await phoneHistAllReq).status()).toBe(200);
	await expect(phoneDark.getByTestId('subtitles-history-row')).toHaveCount(2);
	await noSidewaysScroll(phoneDark);
	await axe(phoneDark, 'subtitles history phone dark');

	await phoneDark.goto('/settings/subtitles');
	await expect(phoneDark.locator('#lang-card')).toBeVisible();
	await expect(phoneDark.locator('#translator-card')).toBeVisible();
	if (dir) {
		await settleShot(phoneDark);
		await phoneDark.screenshot({ path: join(dir, 'subtitles-settings-phone-dark.png'), fullPage: false, animations: 'disabled' });
	}
	await noSidewaysScroll(phoneDark);
	await axe(phoneDark, 'subtitles settings phone dark');

	expect(phoneCe).toEqual([]);
	expect(phonePe).toEqual([]);
	await phoneDark.context().close();
});
