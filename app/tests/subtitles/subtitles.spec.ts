import { expect, test, type Browser, type Page } from '@playwright/test';
import { mkdirSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { subsPort } from '../fakes/index.mjs';
import { moviesDir, noSidewaysScroll, owner, signIn, libraryTool, openSeasonNamed, episodeAction, openAdvanced, openLibraryTab, openSubtitles } from '../helpers/helpers.ts';
import { axe as scan } from '../helpers/axe.ts';

// Subtitles (4.16): the owner sets English and Spanish as the default
// languages, reads what the Pass mark means, adds the lab's fake Subtitle
// site (and one that needs an account, which waits for its key), sees every
// movie missing Spanish and searches them all, then on a movie page picks
// the movie's own languages, chooses a subtitle by hand, fixes its timing
// and turns one down; an episode gets its own languages from the show page.
// Phone width, accessibility, and members see none of it. Every subtitle
// comes from cmd/holyarrs-fakesubs; no real site is reached.
test.describe.configure({ mode: 'serial' });

let page: Page;
const showsDir = join(moviesDir, '..', 'shows');
const files = {
	movie: join(moviesDir, 'Quill Atlas (2011)', 'Quill.Atlas.2011.1080p.BluRay.x264-LAB.mkv'),
	episode: join(showsDir, 'Fennel Clock (2020)', 'Season 1', 'Fennel.Clock.S01E01.1080p.WEB-DL.H264-LAB.mkv')
};
const siteURL = `http://127.0.0.1:${subsPort}`;

async function fresh(browser: Browser, width = 1280) {
	const ctx = await browser.newContext({ viewport: { width, height: 900 } });
	return ctx.newPage();
}

const axe = (p: Page, where: string, include?: string) => scan(p, where, { waitAll: false, include: include ?? '#main' });
// Opens More options and every card's advanced part.
async function openAll(p: Page) {
	await p.getByTestId('more-options').evaluate((d) => ((d as HTMLDetailsElement).open = true));
	await openAdvanced(p);
}
async function adopt(folderLabel: string, name: string, button: string, doneText: string) {
	await page.goto('/library/import');
	const lf = page.getByLabel('Library folder');
	await lf.selectOption((await lf.locator('option', { hasText: folderLabel }).first().getAttribute('value')) ?? '');
	await page.getByRole('button', { name: 'Scan', exact: true }).click();
	const plan = page.getByTestId('adopt-plan');
	await expect(plan).toBeVisible({ timeout: 30_000 });
	await expect(plan.getByRole('checkbox', { name: `Adopt ${name}` })).toBeChecked();
	// Last to first: unchecking a box shifts the later "checked" matches, not the earlier ones.
	for (const box of (await plan.getByRole('checkbox', { checked: true }).all()).reverse()) {
		if (!(await box.getAttribute('aria-label'))?.includes(name)) await box.uncheck();
	}
	await page.getByRole('button', { name: button }).click();
	await expect(page.getByTestId('adopt-results')).toContainText(doneText, { timeout: 30_000 });
}

test.beforeAll(async ({ browser }) => {
	for (const f of Object.values(files)) {
		mkdirSync(dirname(f), { recursive: true });
		writeFileSync(f, `video ${f}`);
	}
	page = await fresh(browser);
	await signIn(page, owner());
});

test('Settings > Subtitles: English and Spanish become the default languages; the Pass marks say what they mean', async () => {
	await page.goto('/settings');
	// Open the Library card first.
	await page.getByTestId('settings-group').filter({ hasText: 'Library' }).click();
	await page.getByRole('navigation', { name: 'Settings sections' }).getByRole('link', { name: 'Subtitles' }).click();
	await expect(page.getByRole('heading', { level: 1, name: 'Subtitles' })).toBeVisible();
	const editor = page.getByTestId('wish-editor').first();
	await expect(editor).toContainText('No languages yet. Add one to get subtitles.');
	await page.getByRole('button', { name: 'Add a language' }).click();
	await page.getByRole('button', { name: 'Add a language' }).click();
	await expect(editor.getByTestId('wish-row')).toHaveCount(2);
	await page.locator('#def-lang-0').selectOption('en');
	await page.locator('#def-lang-1').selectOption('es');
	await openAdvanced(page);
	await expect(page.getByLabel('Pass mark for episodes')).toHaveValue('90');
	await expect(page.getByLabel('Pass mark for movies')).toHaveValue('70');
	await page.getByTestId('pass-mark-help').locator('summary').click();
	await expect(page.getByTestId('pass-mark-help')).toContainText('match score from 0 to 100');
	await expect(page.getByLabel('Frame rate from')).toHaveValue('25');
	await expect(page.getByLabel('Frame rate to')).toHaveValue('23.976');
	await page.getByTestId('savebar').getByRole('button', { name: 'Save' }).click();
	await expect(page.getByText('Subtitle settings saved')).toBeVisible();
	await page.reload();
	await expect(page.locator('#def-lang-0')).toHaveValue('en');
	await expect(page.locator('#def-lang-1')).toHaveValue('es');
	await axe(page, 'settings subtitles');
});

test('Subtitle sites: the lab site is added and tested; a site that needs your account waits for its key; pause and remove', async () => {
	await page.getByRole('button', { name: 'Add a Subtitle site' }).click();
	await page.getByRole('radio', { name: /^Lab subtitle site \(/ }).check();
	await page.getByRole('textbox', { name: 'Address' }).fill(siteURL);
	await page.getByText('Limits', { exact: true }).click();
	await page.getByLabel('Wait between calls (ms)').fill('10');
	await page.getByLabel('Calls per window, at most').fill('5000');
	await page.getByLabel(/Reach the local network/).check();
	await page.locator('form[aria-labelledby="h-site-form"]').getByRole('button', { name: 'Save', exact: true }).click();
	const lab = page.getByTestId('subtitle-site').filter({ hasText: 'Lab subtitle site' }).first();
	await expect(lab.getByTestId('subtitle-site-state')).toHaveText('On');
	await expect(lab.getByTestId('subtitle-site-test')).toHaveText(/· \d+ subtitles?$/, { timeout: 15_000 });

	// A site that needs your account: refused until the key is there; the key field is masked.
	await page.getByRole('button', { name: 'Add a Subtitle site' }).click();
	const acctRadio = page.getByRole('radio', { name: /with an account/ });
	await acctRadio.check();
	await expect(page.locator('form').getByText('Needs your account').first()).toBeVisible();
	const key = page.getByTestId('site-account').getByLabel('API key');
	await expect(key).toHaveAttribute('type', 'password');
	await page.getByRole('textbox', { name: 'Address' }).fill(siteURL);
	await page.getByLabel(/Reach the local network/).check();
	await page.locator('form[aria-labelledby="h-site-form"]').getByRole('button', { name: 'Save', exact: true }).click();
	await expect(page.getByRole('alert')).toContainText('needs your account');
	await key.fill('xxxx-xxxx');
	await page.locator('form[aria-labelledby="h-site-form"]').getByRole('button', { name: 'Save', exact: true }).click();
	const acct = page.getByTestId('subtitle-site').filter({ hasText: 'with an account' });
	await expect(acct).toContainText('account saved');
	await expect(acct.getByTestId('subtitle-site-test')).toHaveText(/· \d+ subtitles?$/, { timeout: 15_000 });
	await expect(page.locator('body')).not.toContainText('xxxx-xxxx');
	await acct.getByRole('button', { name: /^Pause / }).click();
	await expect(acct.getByTestId('subtitle-site-state')).toHaveText('Paused');
	await expect(acct.getByTestId('subtitle-site-text')).toContainText('Paused by you');
	await acct.getByRole('button', { name: /^Remove / }).click();
	await page.getByTestId('ask-yes').click();
	await expect(page.getByTestId('subtitle-site')).toHaveCount(1);
});

test('Missing subtitles: every movie missing Spanish, by show too, and Search all with its progress', async () => {
	await adopt(moviesDir, 'Quill Atlas', 'Adopt 1 movie', '1 movie is now in the Library');
	await adopt(showsDir, 'Fennel Clock', 'Adopt 1 show', '1 show is now in the Library');
	await page.goto('/library');
	await libraryTool(page, 'Missing subtitles');
	await expect(page.getByRole('heading', { level: 1, name: 'Missing subtitles' })).toBeVisible();
	await page.getByLabel('Language').selectOption('es');
	await page.getByLabel('Kind').selectOption('movie');
	await expect(page).toHaveURL(/language=es&kind=movie/);
	const rows = page.getByTestId('missing-subtitle');
	await expect(async () => {
		await page.reload();
		await expect(rows.filter({ hasText: 'Quill Atlas' })).toHaveCount(1, { timeout: 2000 });
	}).toPass({ timeout: 30_000 });
	for (const r of await rows.all()) await expect(r.getByTestId('missing-language')).toHaveText('Spanish');
	await expect(rows.filter({ hasText: /S\d\dE\d\d/ })).toHaveCount(0);

	// One show: its episode misses English and Spanish.
	await page.getByLabel('Kind').selectOption('');
	await page.getByLabel('Language').selectOption('');
	await page.getByLabel('Movie or show').selectOption({ label: 'Fennel Clock (2)' });
	await expect(rows).toHaveCount(2);
	await expect(rows.first()).toContainText('Fennel Clock S01E01');
	await axe(page, 'missing subtitles');

	// Back to every movie missing Spanish; Search all follows its Jobs.
	await page.getByLabel('Movie or show').selectOption({ label: 'All of them' });
	await page.getByLabel('Language').selectOption('es');
	await page.getByLabel('Kind').selectOption('movie');
	await page.getByTestId('search-all-subtitles').click();
	const progress = page.getByTestId('search-all-progress');
	await expect(progress).toContainText(/Done: searched \d+ files?\./, { timeout: 60_000 });
	await expect(async () => {
		await page.reload();
		await expect(rows.filter({ hasText: 'Quill Atlas' })).toHaveCount(0, { timeout: 2000 });
	}).toPass({ timeout: 30_000 });
});

test('Subtitles page: what is missing and why, the sites, the jobs, and the list narrowed by what each file needs', async () => {
	await page.goto('/');
	await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Subtitles' }).click();
	await expect(page).toHaveURL(/\/subtitles$/);
	await expect(page.getByRole('heading', { level: 1, name: /Subtitles/ })).toBeVisible();
	const tiles = page.getByTestId('subtitles-tiles');
	await expect(tiles).toBeVisible();
	// The lab's videos are not real: nothing can be read from them, so every missing subtitle needs speech to text.
	const missing = Number((await page.getByTestId('tile-missing').locator('.n').textContent())?.replace(/\D/g, ''));
	expect(missing).toBeGreaterThan(0);
	await expect(page.getByTestId('tile-speech').locator('.n')).not.toHaveText('0');
	await expect(page.getByTestId('tile-translate').locator('.n')).toHaveText('0');
	// This build has no speech program, and the tile says so.
	await expect(page.getByTestId('tile-speech')).toContainText('no speech program');
	await expect(page.getByTestId('subtitles-site').first()).toBeVisible();
	// Search all, in the test before this one, saved the movie's Spanish subtitle from the lab site: it is listed in History as done.
	await page.getByTestId('subtitles-tab-history').click();
	await expect(page).toHaveURL(/\/subtitles\?tab=history$/);
	const histTab = page.getByTestId('subtitles-history-tab');
	await expect(histTab).toBeVisible();
	await expect(histTab.getByTestId('subtitles-done-count').filter({ hasText: /^Downloaded [1-9]\d*$/ })).toBeVisible();
	const quillHistoryRow = histTab.getByTestId('subtitles-history-row').filter({ hasText: 'Quill Atlas' }).first();
	await expect(quillHistoryRow).toHaveAttribute('data-state', 'done');
	await expect(quillHistoryRow).toHaveAttribute('data-how', 'downloaded');
	await page.getByTestId('subtitles-tab-overview').click();
	await expect(page).toHaveURL(/\/subtitles$/);
	const rows = page.getByTestId('subtitles-missing-row');
	await expect(rows.first()).toBeVisible();
	await expect(rows.first().getByTestId('subtitles-step')).toHaveAttribute('data-step', 'speechOff');
	await expect(page.getByTestId('subtitles-transcribe')).toHaveCount(0);
	// Narrowed to files that have another language: none here.
	await page.getByTestId('tile-translate').click();
	await expect(rows).toHaveCount(0);
	await expect(page.getByText('Nothing matches')).toBeVisible();
	await page.getByTestId('tile-speech').click();
	await expect(rows.first()).toBeVisible();
	await page.getByTestId('tile-missing').click();
	await page.waitForLoadState('networkidle');
	await noSidewaysScroll(page);
	await axe(page, 'subtitles page');
	if (process.env.HOLYARRS_E2E_SHOTS) {
		mkdirSync(process.env.HOLYARRS_E2E_SHOTS, { recursive: true });
		await page.screenshot({ path: join(process.env.HOLYARRS_E2E_SHOTS, 'subtitles-light.png'), fullPage: true });
		await page.screenshot({ path: join(process.env.HOLYARRS_E2E_SHOTS, 'subtitles-view.png') });
		await page.emulateMedia({ colorScheme: 'dark' });
		await page.screenshot({ path: join(process.env.HOLYARRS_E2E_SHOTS, 'subtitles-dark.png'), fullPage: true });
		await page.emulateMedia({ colorScheme: null });
	}
	// Pause holds subtitle work in the queue and says so; Resume lets it run again.
	const pause = page.getByTestId('subtitles-pause');
	await expect(pause).toHaveText('Pause');
	await pause.click();
	await expect(page.getByTestId('subtitles-paused')).toContainText('Paused');
	await expect(pause).toHaveText('Resume');
	await page.reload();
	await expect(page.getByTestId('subtitles-paused')).toBeVisible();
	await page.getByTestId('subtitles-pause').click();
	await expect(page.getByTestId('subtitles-paused')).toHaveCount(0);
	await expect(page.getByTestId('subtitles-pause')).toHaveText('Pause');
	// Nothing is searched here: the tests after this one count what is missing.
	await expect(rows.first().getByTestId('subtitles-search')).toBeEnabled();
});

test('Movie page: its own languages, every result with score and reasons, Choose a subtitle, Sync fix, Turn down, History', async () => {
	await page.goto('/library');
	await page.getByRole('link', { name: /Quill Atlas/ }).first().click();
	const card = await openSubtitles(page);
	await expect(card.getByTestId('subtitle-wish')).toContainText('English · Spanish · the default languages');
	await expect(card.getByTestId('subtitle-row').filter({ hasText: 'Spanish' })).toContainText('From Lab subtitle site');
	await expect(card.getByTestId('subtitle-row').filter({ hasText: 'Spanish' })).toContainText(/\d+\/100/);

	// Its own languages: English and French.
	await card.getByTestId('subtitle-wish-change').click();
	await card.getByRole('radio', { name: /Its own languages/ }).check();
	await card.getByRole('button', { name: 'Take Spanish off' }).click();
	await card.getByRole('button', { name: 'Add a language' }).click();
	await card.locator('select[id$="-lang-1"]').selectOption('fr');
	await card.getByRole('button', { name: 'Save languages' }).click();
	await expect(page.getByText('Subtitle languages saved')).toBeVisible();
	await expect(card.getByTestId('subtitle-wish')).toContainText('English · French · set for this movie');
	await expect(card.getByTestId('subtitle-missing')).toContainText('French');

	// Choose a subtitle by hand: every result with its score and reasons.
	await card.getByTestId('subtitle-choose').click();
	const choices = card.getByTestId('subtitle-choices');
	await expect(choices.getByRole('heading', { name: 'French' })).toBeVisible({ timeout: 30_000 });
	await expect(choices.getByTestId('subtitle-choice').first()).toContainText('matches');
	await expect(choices.getByTestId('subtitle-choice-score').first()).toHaveText(/\d+\/100/);
	await axe(page, 'choose a subtitle', '[data-testid="subtitles"]'); // the card; the Library tab's other tools have their own checks
	await choices.locator('ul[data-lang="fr"]').getByRole('button', { name: /^Choose / }).first().click();
	await expect(page.getByRole('status').filter({ hasText: 'Subtitle saved' })).toContainText(/\.fr\.srt/, { timeout: 30_000 });
	const fr = card.getByTestId('subtitle-row').filter({ hasText: 'French' });
	await expect(fr).toContainText('Chosen by hand from Lab subtitle site');

	// Sync fix: one second later.
	await fr.getByRole('button', { name: 'Sync fix for French' }).click();
	await fr.getByRole('button', { name: '500 ms later' }).click();
	await fr.getByRole('button', { name: '500 ms later' }).click();
	await expect(fr).toContainText('1 second later');
	await axe(page, 'sync fix', '[data-testid="subtitles"]'); // the card; the Library tab's other tools have their own checks
	await fr.getByRole('button', { name: 'Fix the timing' }).click();
	await expect(page.getByText('Timing fixed')).toBeVisible();

	// Turn down: it goes to the holding folder and is never chosen again.
	const fr2 = card.getByTestId('subtitle-row').filter({ hasText: 'French' });
	await fr2.getByRole('button', { name: 'Turn down French' }).click();
	await fr2.getByLabel('Why (optional)').fill('out of sync');
	await fr2.getByTestId('subtitle-turn-down-yes').click();
	await expect(page.getByText('Subtitle turned down')).toBeVisible();

	// History lists the subtitle Events.
	await page.reload();
	await openLibraryTab(page);
	// The title's history sits beside its files on the Library tab.
	const hist = page.getByTestId('lib-pane').locator('aside', { has: page.getByRole('heading', { name: 'History' }) });
	await expect(hist).toContainText('Subtitles');
	// Its rows are short; the full History page says what happened.
	await hist.getByText('Subtitles', { exact: false }).first().click();
	await expect(page).toHaveURL(/\/downloads\/history\?title=\d+/);
	const down = page.locator('[data-testid="history-row"][data-kind="subtitle_turned_down"]');
	await expect(down).toHaveCount(1, { timeout: 15_000 });
	await down.click();
	await expect(page.getByRole('main')).toContainText(/Turned down .*\.fr\.srt from Lab subtitle site/);
});

test('Cleanup: English gets its own steps, the other languages keep theirs; Clean again on a subtitle and a title', async () => {
	await page.goto('/settings/subtitles');
	await openAll(page);
	const card = page.locator('#clean-subs');
	await expect(card.getByLabel('Steps for')).toHaveValue('*');
	await expect(card.getByLabel('Remove ad and credit lines')).toBeChecked();
	await expect(card.getByLabel('Remove hearing-impaired text')).not.toBeChecked();
	await card.getByLabel('Steps for').selectOption('en');
	await card.getByLabel('Remove hearing-impaired text').check();
	await page.getByTestId('savebar').getByRole('button', { name: 'Save' }).click();
	await expect(page.getByText('Subtitle settings saved')).toBeVisible();
	await page.reload();
	await openAll(page);
	await expect(card.getByLabel('Remove hearing-impaired text')).not.toBeChecked(); // every other language
	await card.getByLabel('Steps for').selectOption('en');
	await expect(card.getByLabel('Remove hearing-impaired text')).toBeChecked();
	await axe(page, 'cleanup settings', '#clean-subs');

	await page.goto('/library');
	await page.getByRole('link', { name: /Quill Atlas/ }).first().click();
	const subs = await openSubtitles(page);
	await expect(subs.getByTestId('subtitle-clean').first()).toHaveAccessibleName(/^Clean the .+ subtitle$/);
	await subs.getByTestId('subtitle-clean').first().click();
	await expect(page.getByRole('status').filter({ hasText: /Subtitle cleaned|Nothing to clean/ })).toBeVisible();
	await subs.getByTestId('subtitle-clean-all').click();
	await expect(page.getByRole('status').filter({ hasText: /Subtitles cleaned|Nothing to clean/ }).first()).toBeVisible();
});

test('Show page: the show follows the default languages', async () => {
	await page.goto('/library?kind=show');
	await page.getByRole('link', { name: /Fennel Clock/ }).first().click();
	const showCard = await openSubtitles(page);
	await expect(showCard.getByTestId('subtitle-wish')).toContainText('English · Spanish · the default languages');
	await expect(showCard.getByTestId('subtitle-missing-link')).toContainText('2 subtitles missing');
});

test('Show page: one episode gets its own subtitle languages in its sheet', async () => {
	await page.goto('/library');
	await page.getByRole('link', { name: /Fennel Clock/ }).first().click();
	await openLibraryTab(page);
	await openSeasonNamed(page, 'Season 1');
	// The episode's sheet holds its own Subtitles card.
	await episodeAction(page, 'S01E01', 'History');
	await page.getByTestId('episode-subtitles-row').getByRole('button', { name: 'Subtitle options' }).click();
	await page.getByRole('menuitem', { name: 'Choose languages…' }).click();
	const sheet = page.getByTestId('episode-subtitles');
	await expect(sheet.getByRole('heading', { name: 'Subtitles of S01E01' })).toBeVisible();
	await expect(sheet.getByTestId('subtitle-missing')).toHaveCount(2);
	await sheet.getByTestId('subtitle-wish-change').click();
	await sheet.getByRole('radio', { name: /Its own languages/ }).check();
	await sheet.getByRole('button', { name: 'Take Spanish off' }).click();
	await sheet.getByRole('button', { name: 'Save languages' }).click();
	await expect(sheet.getByTestId('subtitle-wish')).toContainText('English · set for this episode');
	await expect(sheet.getByTestId('subtitle-missing')).toHaveCount(1);
	await axe(page, 'episode subtitles', '[data-testid="episode-subtitles"]');
	await sheet.getByTestId('subtitle-search').click();
	await expect(page.getByText('Searching for subtitles')).toBeVisible();
	await page.keyboard.press('Escape');
});

test('Subtitles page: coverage stats, sidebar progress ring, stop and retry jobs, Failed tab, and Integrity verification', async () => {
	await page.goto('/subtitles');
	await expect(page.getByTestId('subtitles-stats')).toBeVisible();
	await expect(page.getByTestId('subtitles-coverage')).toContainText('%');
	await expect(page.getByTestId('subtitles-speed-speech')).toBeVisible();
	await expect(page.getByTestId('subtitles-speed-translate')).toBeVisible();
	// Wait until any background search queued by the previous test settles.
	await expect(page.getByTestId('subtitles-jobs')).toHaveCount(0, { timeout: 15_000 });
	await expect(page.getByTestId('subtitles-waiting-callout')).toHaveCount(0, { timeout: 15_000 });
	await page.getByTestId('subtitles-pause').click();
	await expect(page.getByTestId('subtitles-paused')).toBeVisible();
	await page.getByTestId('subtitles-missing-row').first().getByTestId('subtitles-search').click();
	await expect(page.getByTestId('subtitles-jobs')).toHaveCount(0);
	const callout = page.getByTestId('subtitles-waiting-callout');
	await expect(callout).toBeVisible();
	const navSub = page.getByRole('navigation', { name: 'Main' }).locator('a[href="/subtitles"]');
	await expect(navSub.getByTestId('nav-ring')).toBeVisible();
	await expect(navSub.getByTestId('nav-subtitles')).toBeVisible();
	if (process.env.HOLYARRS_E2E_SHOTS) {
		mkdirSync(process.env.HOLYARRS_E2E_SHOTS, { recursive: true });
		await page.evaluate(() => window.scrollTo(0, 0));
		await page.screenshot({ path: join(process.env.HOLYARRS_E2E_SHOTS, 'subtitles-active-ring.png') });
	}
	await callout.getByTestId('subtitles-goto-waiting').click();
	await expect(page).toHaveURL(/\/subtitles\?tab=waiting$/);
	await expect(page.getByTestId('subtitles-waiting-tab')).toBeVisible();
	const waitingRows = page.getByTestId('subtitles-waiting-row');
	await expect(waitingRows.first()).toBeVisible();
	const waitingBefore = await waitingRows.count();
	await waitingRows.first().getByTestId('subtitles-skip-job').click();
	await expect(waitingRows).toHaveCount(waitingBefore - 1);

	await page.getByTestId('subtitles-tab-history').click();
	await expect(page).toHaveURL(/\/subtitles\?tab=history$/);
	await expect(page.getByTestId('subtitles-history-tab')).toBeVisible();
	await page.getByTestId('subtitles-history-filter').getByRole('radio', { name: 'Skipped', exact: true }).click();
	const skippedRows = page.locator('[data-testid="subtitles-history-row"][data-state="skipped"]');
	await expect(skippedRows.first()).toBeVisible();
	const skippedBefore = await skippedRows.count();
	const retrySearchRes = page.waitForResponse((r) => r.url().includes('/api/v1/subtitles/search') && r.request().method() === 'POST');
	await skippedRows.first().getByTestId('subtitles-history-retry').click();
	expect((await retrySearchRes).status()).toBe(202);
	await expect(skippedRows).toHaveCount(skippedBefore - 1);

	// Stop the newly queued job via API so it moves into Failed (cancelled).
	let queuedIds: number[] = [];
	await expect(async () => {
		const wRes = await page.request.get('/api/v1/subtitles/waiting');
		expect(wRes.status()).toBe(200);
		const wBody = (await wRes.json()) as { jobs?: { id: number }[] };
		queuedIds = (wBody.jobs ?? []).map((j) => j.id);
		expect(queuedIds.length).toBeGreaterThan(0);
	}).toPass({ timeout: 10_000 });
	const me = (await (await page.request.get('/api/v1/auth/me')).json()) as { csrf: string };
	for (const id of queuedIds) {
		const stopRes = await page.request.post(`/api/v1/subtitles/jobs/${id}/stop`, { headers: { 'X-CSRF-Token': me.csrf } });
		expect(stopRes.status()).toBe(200);
	}

	await page.getByTestId('subtitles-tab-failed').click();
	await expect(page).toHaveURL(/\/subtitles\?tab=failed$/);
	await expect(page.getByTestId('subtitles-failed-tab')).toBeVisible();
	const cancelledRows = page.locator('[data-testid="subtitles-failed-row"][data-state="cancelled"]');
	await expect(cancelledRows.first()).toBeVisible();
	const cancelledBefore = await cancelledRows.count();
	await noSidewaysScroll(page);
	await axe(page, 'subtitles failed tab');
	if (process.env.HOLYARRS_E2E_SHOTS) {
		await page.screenshot({ path: join(process.env.HOLYARRS_E2E_SHOTS, 'subtitles-failed-tab.png'), fullPage: true });
	}
	await cancelledRows.first().getByTestId('subtitles-job-retry').click();
	await expect(cancelledRows).toHaveCount(cancelledBefore - 1);

	await page.getByTestId('subtitles-tab-waiting').click();
	await expect(page).toHaveURL(/\/subtitles\?tab=waiting$/);
	await expect(waitingRows.first()).toBeVisible();
	const wCleanRes = await page.request.get('/api/v1/subtitles/waiting');
	expect(wCleanRes.status()).toBe(200);
	const wCleanBody = (await wCleanRes.json()) as { jobs?: { id: number }[] };
	for (const j of wCleanBody.jobs ?? []) {
		expect((await page.request.post(`/api/v1/subtitles/jobs/${j.id}/stop`, { headers: { 'X-CSRF-Token': me.csrf } })).status()).toBe(200);
	}

	await page.getByTestId('subtitles-tab-overview').click();
	await expect(page).toHaveURL(/\/subtitles$/);
	await expect(page.getByTestId('subtitles-waiting-callout')).toHaveCount(0, { timeout: 10_000 });
	await expect(page.getByTestId('subtitles-jobs')).toHaveCount(0, { timeout: 10_000 });
	await page.getByTestId('subtitles-pause').click();
	await expect(page.getByTestId('subtitles-paused')).toHaveCount(0);
	await page.getByTestId('subtitles-tab-integrity').click();
	await expect(page).toHaveURL(/\/subtitles\?tab=integrity$/);
	await expect(page.getByTestId('subtitles-integrity-tab')).toBeVisible();
	const integritySummary = page.getByTestId('subtitles-integrity-summary');
	await expect(integritySummary).toBeVisible();
	const verifyBtn = page.getByTestId('subtitles-verify-btn');
	await verifyBtn.click();
	await expect(async () => {
		await expect(verifyBtn).toBeEnabled();
		await expect(verifyBtn).toHaveText('Verify subtitles');
		await expect(integritySummary).not.toContainText('Not checked yet');
		await expect(integritySummary).toContainText('Last checked');
	}).toPass({ timeout: 20_000 });
	await noSidewaysScroll(page);
	await axe(page, 'subtitles integrity tab');
	if (process.env.HOLYARRS_E2E_SHOTS) {
		await page.screenshot({ path: join(process.env.HOLYARRS_E2E_SHOTS, 'subtitles-integrity-tab.png'), fullPage: true });
	}
	const issueRows = page.getByTestId('subtitles-integrity-row');
	const issueCount = await issueRows.count();
	if (issueCount > 0) {
		await issueRows.first().getByTestId('subtitles-integrity-keep').click();
		await expect(issueRows).toHaveCount(issueCount - 1);
	}
});

test('the subtitle screens fit a phone and pass the accessibility scan', async ({ browser }) => {
	const ph = await fresh(browser, 390);
	await signIn(ph, owner());
	for (const url of ['/settings/subtitles', '/library/missing-subtitles', '/subtitles', '/subtitles?tab=waiting', '/subtitles?tab=history', '/subtitles?tab=failed', '/subtitles?tab=integrity']) {
		await ph.goto(url);
		await expect(ph.getByRole('heading', { level: 1 })).toBeVisible();
		await ph.waitForLoadState('networkidle');
		if (url === '/settings/subtitles') await openAll(ph); // the Cleanup card's steps and language picker
		await noSidewaysScroll(ph);
		await axe(ph, `phone ${url}`);
	}
	await ph.goto('/library');
	await ph.getByRole('link', { name: /Quill Atlas/ }).first().click();
	const card = await openSubtitles(ph);
	await card.getByTestId('subtitle-choose').click();
	await expect(card.getByTestId('subtitle-choice').first()).toBeVisible({ timeout: 30_000 });
	await card.getByTestId('subtitle-wish-change').click();
	await card.getByRole('radio', { name: /Its own languages/ }).check();
	await noSidewaysScroll(ph);
	await axe(ph, 'phone movie subtitles');
	await ph.context().close();
});

test('a long translator error wraps inside its card on a small phone', async ({ browser }) => {
	const ph = await fresh(browser, 360);
	await signIn(ph, owner());
	const word = 'invalid_api_key_provided_please_check_your_settings_and_try_again_' + 'a'.repeat(40);
	await ph.route('**/api/v1/settings/subtitles/translator', (rt) =>
		rt.request().method() === 'GET' ? rt.fulfill({ json: { kind: 'openai', url: 'http://192.168.1.20:11434/v1', model: 'm', has_key: false, built_in_auto: false } }) : rt.continue()
	);
	await ph.route('**/api/v1/settings/subtitles/translator/test', (rt) => rt.fulfill({ json: { ok: false, message: word, models: [] } }));
	await ph.goto('/settings/subtitles');
	await ph.getByTestId('translator-test-button').click();
	const line = ph.getByTestId('translator-test');
	await expect(line).toContainText(word);
	const fits = await ph.locator('#translator-card').evaluate((card) => {
		const c = card.getBoundingClientRect();
		return [...card.querySelectorAll('[data-testid=translator-test] *')].every((e) => e.getBoundingClientRect().right <= c.right + 1);
	});
	expect(fits).toBe(true);
	await noSidewaysScroll(ph);
	await ph.context().close();
});

test('with a translator set up, each language row keeps its labels on one line at desktop width', async ({ browser }) => {
	const ph = await fresh(browser, 1440);
	await signIn(ph, owner());
	await ph.route('**/api/v1/settings/subtitles/translator', (rt) =>
		rt.request().method() === 'GET' ? rt.fulfill({ json: { kind: 'openai', url: 'http://192.168.1.20:11434/v1', model: 'm', has_key: false, built_in_auto: false } }) : rt.continue()
	);
	await ph.goto('/settings/subtitles');
	const row = ph.getByTestId('wish-row').first();
	await expect(row.getByTestId('wish-translate-from')).toBeVisible();
	const tall = await row.locator('.field > label').evaluateAll((ls) => ls.filter((l) => l.getBoundingClientRect().height > (parseFloat(getComputedStyle(l).lineHeight) || 20) * 1.8).map((l) => l.textContent));
	expect(tall).toEqual([]);
	await ph.context().close();
});

test('members see none of it', async ({ browser }) => {
	await page.goto('/settings/users');
	await page.getByRole('button', { name: 'Invite someone' }).click();
	await page.getByLabel('Note for yourself').fill('Uma');
	await page.getByRole('button', { name: 'Make an invite link' }).click();
	const link = (await page.locator('.fresh code').textContent())!.trim();
	const uma = await fresh(browser);
	const pw = `e2e-${Math.random().toString(36).slice(2)}-pw`;
	await uma.goto(new URL(link).pathname);
	await uma.getByLabel('Name').fill('uma');
	await uma.getByLabel('Password', { exact: true }).fill(pw);
	await uma.getByLabel('Confirm password').fill(pw);
	await uma.getByRole('button', { name: 'Join and sign in' }).click();
	await expect(uma).toHaveURL(/\/$/);
	await uma.goto('/library');
	await expect(uma.getByRole('link', { name: 'Missing subtitles' })).toHaveCount(0);
	await uma.getByRole('link', { name: /Quill Atlas/ }).first().click();
	await expect(uma.getByRole('heading', { level: 1 })).toContainText('Quill Atlas');
	await expect(uma.getByTestId('subtitles')).toHaveCount(0);
	for (const url of ['/library/missing-subtitles', '/subtitles']) {
		await uma.goto(url);
		await expect(uma).not.toHaveURL(new RegExp(url.replace(/\//g, '\\/') + '$'));
	}
	// A Settings page they may not open shows a note in place of the page.
	await uma.goto('/settings/subtitles');
	await expect(uma.getByRole('heading', { name: "You can't change this" })).toBeVisible();
	for (const path of ['/api/v1/subtitles/missing', '/api/v1/subtitles/overview', '/api/v1/subtitle-sites', '/api/v1/settings/subtitles']) {
		expect((await uma.request.get(path)).status(), path).toBe(403);
	}
	await uma.context().close();
});
