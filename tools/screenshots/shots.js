// README screenshots of Sync & Backup, English, device scale 2, through a real
// ZimaOS login. Needs Playwright with Chromium (npm i playwright; npx playwright install chromium).
//
//   ZB_URL=http://<box> ZB_USER=<user> ZB_PASSFILE=<file> node shots.js module <out-dir>
//   ZB_URL=…            ZB_USER=…      ZB_PASSFILE=…      node shots.js log    <out-dir>
//   ZB_URL=…            ZB_USER=…      ZB_PASSFILE=…      node shots.js card   <out-dir>
//
// "module" needs the demo jobs (demo-data.sh, then demo-jobs.py setup) and writes
// overview, overview-dark, new-backup, passphrase, advanced, folder-picker,
// and restore; nothing is created or changed on the box. "log" wants a second
// run of the backup job first (demo-jobs.py rerun). "card" needs a
// failing scheduled job (demo-jobs.py card) and writes dashboard-card.
// Set PLAYWRIGHT to the module path when playwright is not resolvable from here.
const { chromium } = require(process.env.PLAYWRIGHT || 'playwright');
const fs = require('fs');

const URL = process.env.ZB_URL.replace(/\/$/, '');
const [mode, out] = process.argv.slice(2);
const log = (...a) => console.log(...a);

async function login(ctx) {
  const p = await ctx.newPage();
  await p.goto(URL + '/', { waitUntil: 'networkidle' });
  await p.locator('input:not([type="password"])').first().fill(process.env.ZB_USER);
  await p.locator('input[type="password"]').first().fill(fs.readFileSync(process.env.ZB_PASSFILE, 'utf8').trim());
  await p.keyboard.press('Enter');
  await p.waitForLoadState('networkidle');
  await p.waitForTimeout(2500); // the shell stores its session token after the redirect
  return p;
}

async function moduleShots(p) {
  const errs = [];
  p.on('pageerror', (e) => errs.push(String(e).slice(0, 160)));
  await p.goto(URL + '/modules/zbackup/index.html', { waitUntil: 'networkidle' });
  await p.waitForSelector('#jobList > *', { timeout: 15000 });
  await p.waitForTimeout(1500);
  await p.screenshot({ path: `${out}/overview.png` });
  await p.click('#themeToggle');
  await p.waitForTimeout(600);
  await p.screenshot({ path: `${out}/overview-dark.png` });
  await p.click('#themeToggle');
  await p.waitForTimeout(400);

  const bk = (await p.evaluate(() => api('/jobs'))).find((j) => j.name === 'Photos, Documents');
  if (!bk) throw new Error('demo job "Photos, Documents" missing: run demo-jobs.py setup');

  await p.setViewportSize({ width: 1100, height: 1000 });
  await p.click('#newJobBtn');
  await p.evaluate(() => { wiz.sources = ['/DATA/Demo/Photos', '/DATA/Demo/Documents']; renderSources(); });
  await p.waitForTimeout(2500);
  await p.locator('#volumeList .volume').first().click();
  await p.waitForTimeout(800);
  await p.locator('#jobModal .modal').screenshot({ path: `${out}/new-backup.png` });
  await p.click('#jobSaveBtn'); // opens the passphrase step; "Back" leaves without creating a job
  await p.waitForTimeout(1500);
  await p.locator('#passModal .modal').screenshot({ path: `${out}/passphrase.png` });
  await p.click('#passBackBtn');
  await p.waitForTimeout(500);
  await p.click('#jobCancelBtn');
  await p.waitForTimeout(500);

  await p.evaluate((j) => openWizard(j), bk);
  await p.waitForTimeout(1500);
  await p.evaluate(() => { $('#advancedBox').open = true; $('#advancedBox').scrollIntoView({ block: 'start' }); });
  await p.waitForTimeout(800);
  const ab = await p.locator('#advancedBox').boundingBox();
  await p.screenshot({ path: `${out}/advanced.png`, clip: { x: ab.x, y: ab.y, width: ab.width, height: Math.min(ab.height, 452) } });
  await p.click('#jobCancelBtn');
  await p.waitForTimeout(500);

  await p.evaluate(() => openPicker('', () => {}));
  await p.waitForTimeout(2500);
  await p.locator('#pickerModal .modal').screenshot({ path: `${out}/folder-picker.png` });
  await p.click('#pickerCancelBtn');
  await p.waitForTimeout(400);

  await p.setViewportSize({ width: 1100, height: 640 });
  await p.evaluate((j) => openRestore(j), bk);
  await p.waitForTimeout(4000);
  await p.evaluate(() => browseSnapshot('/DATA/Demo/Photos'));
  await p.waitForTimeout(2500);
  const rows = p.locator('#restoreTree .row');
  for (const i of [0, 2, 3]) await rows.nth(i).locator('span').last().click();
  await p.waitForTimeout(600);
  await p.locator('#restoreModal .modal').screenshot({ path: `${out}/restore.png` });
  await p.click('#restoreCancelBtn');
  await p.waitForTimeout(400);

  log('page errors on the module:', errs.length, errs.join(' || '));
}

// The log of a second run: the first one starts with restic's red "repository
// does not exist" before the module creates it, which reads like a failure.
async function logShot(p) {
  await p.goto(URL + '/modules/zbackup/index.html', { waitUntil: 'networkidle' });
  await p.waitForTimeout(1500);
  await p.setViewportSize({ width: 1100, height: 560 });
  const bk = (await p.evaluate(() => api('/jobs'))).find((j) => j.name === 'Photos, Documents');
  await p.evaluate((j) => openOutput(j), bk);
  await p.waitForTimeout(2500);
  await p.locator('#outputModal .modal').screenshot({ path: `${out}/log.png` });
}

async function cardShot(p) {
  const t = p.getByText(/needs your attention/).first();
  await t.waitFor({ timeout: 150000 }); // the daemon re-sends the card once a minute
  log('card:', (await t.textContent()).replace(/\s+/g, ' ').trim());
  // the card is the smallest ancestor that holds the title, the body and the button
  const card = await t.evaluateHandle((el) => {
    let e = el;
    while (e && !(e.querySelector('button') && e.getBoundingClientRect().height > 150)) e = e.parentElement;
    return e;
  });
  await card.asElement().screenshot({ path: `${out}/dashboard-card.png` });
}

(async () => {
  if (!['module', 'log', 'card'].includes(mode) || !out) {
    console.error('usage: node shots.js module|log|card <out-dir>');
    process.exit(2);
  }
  fs.mkdirSync(out, { recursive: true });
  const b = await chromium.launch();
  const ctx = await b.newContext({ viewport: { width: 1100, height: 640 }, deviceScaleFactor: 2, locale: 'en-US', colorScheme: 'light' });
  await ctx.addInitScript(() => { try { localStorage.setItem('lang', 'en_US'); } catch { /* no storage */ } });
  const p = await login(ctx);
  await { module: moduleShots, log: logShot, card: cardShot }[mode](p);
  await b.close();
})().catch((e) => { console.error(e); process.exit(1); });
