/* Sync & Backup UI — plain JavaScript, no build step.
 *
 * Sections: i18n · api · state · render · wizard · folder picker · history ·
 * restore · preview · settings · init. All strings come from i18n.js via
 * t(); all backend calls go through api(), which attaches the ZimaOS
 * session token. Same structure as the Cron module.
 */
'use strict';

const API_BASE = '/v2/zbackup/api';
const $ = (sel, root = document) => root.querySelector(sel);
const $$ = (sel, root = document) => Array.from(root.querySelectorAll(sel));

/* ---------- i18n ---------- */

const LANGS = window.ZBACKUP_I18N || {};
const SHELL_LANG_MAP = { en: 'en', de: 'de', fr: 'fr', zh: 'zh' };
let lang = 'en';

// The ZimaOS shell keeps the UI language in localStorage.lang as "fr_FR",
// "de_DE", … (measured on v1.7.1); this module lives on the same origin and
// follows it unless the user picked a language here (zbackup_lang).
function resolveLanguage() {
  const own = safeGet('zbackup_lang');
  if (own && LANGS[own]) return own;
  const shell = (safeGet('lang') || navigator.language || 'en').slice(0, 2).toLowerCase();
  return LANGS[SHELL_LANG_MAP[shell]] ? SHELL_LANG_MAP[shell] : 'en';
}

function t(key, params) {
  let s = (LANGS[lang] && LANGS[lang][key]) || (LANGS.en && LANGS.en[key]) || key;
  if (params) for (const [k, v] of Object.entries(params)) s = s.replace(`{${k}}`, v);
  return s;
}

function applyI18n() {
  document.documentElement.lang = lang;
  document.title = t('app.title');
  $$('[data-i18n]').forEach((el) => { el.textContent = t(el.dataset.i18n); });
  $$('[data-i18n-ph]').forEach((el) => { el.placeholder = t(el.dataset.i18nPh); });
  $$('[data-i18n-title]').forEach((el) => { el.title = t(el.dataset.i18nTitle); });
  $('#langSelect').value = lang;
}

function setLanguage(next) {
  lang = LANGS[next] ? next : 'en';
  safeSet('zbackup_lang', lang);
  applyI18n();
  render();
}

const locale = () => (lang === 'zh' ? 'zh-CN' : lang);
const fmtTime = (ms) => (ms ? new Intl.DateTimeFormat(locale(), { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(ms)) : '–');
const fmtRel = (ms) => {
  if (!ms) return '–';
  const diff = ms - Date.now();
  const rtf = new Intl.RelativeTimeFormat(locale(), { numeric: 'auto' });
  const abs = Math.abs(diff);
  if (abs < 3600e3) return rtf.format(Math.round(diff / 60e3), 'minute');
  if (abs < 86400e3) return rtf.format(Math.round(diff / 3600e3), 'hour');
  return rtf.format(Math.round(diff / 86400e3), 'day');
};
function fmtBytes(n) {
  if (n == null) return '';
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
  let i = 0;
  let v = n;
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i += 1; }
  return `${i ? v.toFixed(1) : v} ${units[i]}`;
}
function fmtDuration(ms) {
  if (ms < 1000) return `${ms} ms`;
  const s = Math.round(ms / 1000);
  if (s < 90) return `${s} s`;
  const m = Math.round(s / 60);
  if (m < 90) return `${m} min`;
  return `${(m / 60).toFixed(1)} h`;
}

function safeGet(k) { try { return localStorage.getItem(k); } catch { return null; } }
function safeSet(k, v) { try { localStorage.setItem(k, v); } catch { /* private mode */ } }

function esc(s) {
  return String(s ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}

/* ---------- api ---------- */

class ApiError extends Error {
  constructor(status, code, message) { super(message); this.status = status; this.code = code; }
}

// The gateway forwards module calls without the session token, so it is
// attached here from the shell's localStorage. The access token lives
// three hours; on 401 the module renews it once through the shell's own
// refresh endpoint (measured on v1.7.1: POST /v1/users/refresh with
// {refresh_token} → data.{access_token,refresh_token,expires_at}, the same
// three keys the shell keeps in localStorage) and retries. Only when that
// fails does the banner ask for a reload.
async function api(path, opts = {}, retried = false) {
  const headers = { Accept: 'application/json', ...(opts.headers || {}) };
  if (opts.body !== undefined) headers['Content-Type'] = 'application/json';
  const token = safeGet('access_token');
  if (token) headers.Authorization = `Bearer ${token}`;
  const res = await fetch(API_BASE + path, { ...opts, headers, body: opts.body !== undefined ? JSON.stringify(opts.body) : undefined });
  if (res.status === 401 && !retried && await refreshSession()) return api(path, opts, true);
  if (res.status === 204) return null;
  const isJson = (res.headers.get('content-type') || '').includes('application/json');
  const data = isJson ? await res.json().catch(() => ({})) : await res.text();
  if (!res.ok) throw new ApiError(res.status, (data && data.code) || 'http', (data && data.error) || `HTTP ${res.status}`);
  return data;
}

let refreshing = null;
function refreshSession() {
  if (refreshing) return refreshing; // parallel calls share one renewal
  refreshing = (async () => {
    const rt = safeGet('refresh_token');
    if (!rt) return false;
    try {
      const res = await fetch('/v1/users/refresh', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ refresh_token: rt }) });
      const body = res.ok ? await res.json() : null;
      const d = body && body.data;
      if (!d || !d.access_token) return false;
      safeSet('access_token', d.access_token);
      if (d.refresh_token) safeSet('refresh_token', d.refresh_token);
      if (d.expires_at !== undefined) safeSet('expires_at', String(d.expires_at));
      return true;
    } catch {
      return false;
    } finally {
      setTimeout(() => { refreshing = null; }, 0);
    }
  })();
  return refreshing;
}

function describeError(err) {
  if (err instanceof ApiError && LANGS.en[`error.${err.code}`]) return t(`error.${err.code}`);
  return t('error.generic', { msg: err.message || String(err) });
}

/* ---------- state ---------- */

const state = {
  jobs: [],
  pollTimer: null,
  version: '',
};

async function loadJobs() {
  try {
    state.jobs = await api('/jobs');
    hideBanner();
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) showBanner(t('error.session'), 'bad');
    else showBanner(describeError(err), 'bad');
  }
  render();
  schedulePoll();
}

function schedulePoll() {
  clearTimeout(state.pollTimer);
  const busy = state.jobs.some((j) => j.running);
  state.pollTimer = setTimeout(loadJobs, busy ? 2000 : 15000);
}

async function loadHealth() {
  try {
    const h = await api('/health');
    state.version = h.version;
    $('#versionLabel').textContent = `Sync & Backup ${h.version}`;
  } catch { /* the banner from loadJobs covers it */ }
}

/* ---------- render ---------- */

function render() {
  renderStats();
  renderJobs();
}

function renderStats() {
  const jobs = state.jobs;
  $('#statJobs').textContent = jobs.length;
  $('#statRunning').textContent = jobs.filter((j) => j.running).length;
  $('#statFailed').textContent = jobs.filter((j) => j.last_result && !j.last_result.success).length;
  const next = jobs.filter((j) => j.enabled && j.next_run_at).map((j) => j.next_run_at).sort()[0];
  $('#statNext').textContent = next ? `${fmtRel(next)} · ${fmtTime(next)}` : t('stats.none');
}

function scheduleLabel(job) {
  return scheduleWords(job.schedule || { type: 'manual' });
}

function targetLabel(tg) {
  switch (tg.type) {
    case 'local': return `<code>${esc(tg.path)}</code>`;
    case 'cloud': return `${t('vol.cloud')} <code>${esc(tg.remote.replace(/_[0-9a-f]{6,}$/, ''))}:${esc(tg.path)}</code>`;
    case 'ssh': return `${t('target.sshShort')} <code>${esc(tg.user)}@${esc(tg.host)}${tg.port ? ':' + tg.port : ''}:${esc(tg.path)}</code>`;
    case 'sftp': return `SFTP <code>${esc(tg.user)}@${esc(tg.host)}${tg.port ? ':' + tg.port : ''}:${esc(tg.path)}</code>`;
    case 'smb': return `SMB <code>\\\\${esc(tg.host)}\\${esc(tg.share)}${tg.path ? '\\' + esc(tg.path.replace(/\//g, '\\')) : ''}</code>`;
    case 's3': return `S3 <code>${esc(tg.host)}/${esc(tg.bucket)}${tg.path ? '/' + esc(tg.path) : ''}</code>`;
    default: return esc(tg.type);
  }
}

// transferLine: "12.3 MiB/s · 120 MiB / 1.2 GiB · 2 min left" from the
// runner's figures; nothing while the tool has not reported any.
function transferLine(job) {
  if (!job.bytes_done && !job.rate && !job.files_done) return '';
  const parts = [];
  // files first when the tool counts them: on a cloud drive every file is
  // a round trip, so "12 / 62 files · 1.2 files/s" is the honest figure
  // where "60 KiB/s" reads like a broken link
  if (job.files_total) parts.push(t('jobs.filesOf', { done: job.files_done || 0, total: job.files_total }));
  if (job.file_rate) parts.push(t('jobs.filesPerSec', { n: (job.file_rate / 100).toFixed(1) }));
  if (job.rate) parts.push(`${fmtBytes(job.rate)}/s`);
  if (job.bytes_done) parts.push(job.bytes_total ? `${fmtBytes(job.bytes_done)} / ${fmtBytes(job.bytes_total)}` : fmtBytes(job.bytes_done));
  if (job.file_rate && job.files_total > job.files_done) parts.push(t('jobs.left', { time: fmtDuration(((job.files_total - job.files_done) / (job.file_rate / 100)) * 1000) }));
  else if (job.rate && job.bytes_total > job.bytes_done) parts.push(t('jobs.left', { time: fmtDuration(((job.bytes_total - job.bytes_done) / job.rate) * 1000) }));
  return ` · ${parts.join(' · ')}`;
}

function resultPill(job) {
  if (job.running) return `<span class="pill accent"><span class="dot pulse"></span>${t('code.running')}</span>`;
  const r = job.last_result;
  if (!r) return `<span class="pill">${t('code.never')}</span>`;
  const label = LANGS.en[`code.${r.code}`] ? t(`code.${r.code}`) : r.code;
  const cls = r.code === 'empty' ? 'warn' : (r.success ? 'ok' : (r.code === 'partial' || r.code === 'skipped_running' ? 'warn' : 'bad'));
  return `<span class="pill ${cls}">${label}</span>`;
}

// renderJobs: one line per job — from → to, kind, schedule, result — and
// two buttons; everything else sits under "···". The ZimaOS backup list
// reads the same way (measured 1.7.1: "Von Camera → Bis /media/…").
function renderJobs() {
  const list = $('#jobList');
  if (!state.jobs.length) {
    list.innerHTML = `<div class="empty">${t('jobs.empty')}</div>`;
    return;
  }
  list.innerHTML = state.jobs.map((job) => {
    const r = job.last_result;
    const from = job.sources.length === 1
      ? `<code title="${esc(job.sources[0])}">${esc(baseName(job.sources[0]))}</code>`
      : `<code title="${esc(job.sources.join('\n'))}">${esc(t('name.folders', { n: job.sources.length }))}</code>`;
    const primary = job.running
      ? `<button class="sm" data-act="cancel">${t('act.cancel')}</button>`
      : `<button class="sm primary" data-act="run">${t(job.kind === 'backup' ? 'act.backupNow' : 'act.syncNow')}</button>`;
    const secondary = job.kind === 'backup'
      ? `<button class="sm" data-act="restore">${t('act.restore')}</button>`
      : `<button class="sm" data-act="preview">${t('act.preview')}</button>`;
    const more = `
      <details class="menu">
        <summary class="sm" aria-label="${t('act.more')}">&middot;&middot;&middot;</summary>
        <div class="menu-items">
          <button data-act="history">${t('act.history')}</button>
          <button data-act="output">${t('act.output')}</button>
          <button data-act="edit">${t('act.edit')}</button>
          <button data-act="toggle">${job.enabled ? t('act.disable') : t('act.enable')}</button>
          <button data-act="delete" class="danger">${t('act.delete')}</button>
        </div>
      </details>`;
    const lastLine = r
      ? `<span class="muted">${fmtTime(job.last_run_at)}</span><span class="msg" title="${esc(r.message)}">${esc(r.message)}</span>`
      : '';
    const phaseKey = job.phase && LANGS.en[`phase.${job.phase}`] ? `phase.${job.phase}` : '';
    const progress = job.running
      ? `<div class="progress"><i style="width:${Math.round((job.progress || 0) * 100)}%"></i></div><div class="phase">${phaseKey ? t(phaseKey) : esc(job.phase || '')}${job.progress ? ` · ${Math.round(job.progress * 100)} %` : ''}${transferLine(job)}</div>`
      : '';
    return `
      <article class="job-card${job.enabled ? '' : ' disabled'}" data-id="${job.id}">
        <div>
          <div class="head"><strong>${esc(job.name)}</strong>${job.enabled ? '' : `<span class="pill warn">${t('jobs.disabled')}</span>`}${resultPill(job)}</div>
          <div class="route"><span class="k">${t('jobs.from')}</span> ${from} <span class="arrow">&rarr;</span> <span class="k">${t('jobs.to')}</span> ${targetLabel(job.target)}</div>
          <div class="meta-line">${t(`kind.${job.kind}`)} · ${scheduleLabel(job)}${job.enabled && job.next_run_at ? ` · <span class="muted">${t('jobs.next')} ${fmtRel(job.next_run_at)}</span>` : ''}</div>
        </div>
        <div class="actions">${primary}${secondary}${more}</div>
        ${progress}
        ${lastLine ? `<div class="result">${lastLine}</div>` : ''}
      </article>`;
  }).join('');
}

async function onJobAction(ev) {
  const btn = ev.target.closest('button[data-act]');
  if (!btn) return;
  const card = btn.closest('.job-card');
  const menu = btn.closest('details.menu');
  if (menu) menu.open = false;
  const job = state.jobs.find((j) => j.id === card.dataset.id);
  if (!job) return;
  try {
    switch (btn.dataset.act) {
      case 'run': await api(`/jobs/${job.id}/run`, { method: 'POST' }); break;
      case 'cancel': await api(`/jobs/${job.id}/cancel`, { method: 'POST' }); break;
      case 'toggle': await api(`/jobs/${job.id}/${job.enabled ? 'disable' : 'enable'}`, { method: 'POST' }); break;
      case 'edit': openWizard(job); return;
      case 'history': openHistory(job); return;
      case 'output': openOutput(job); return;
      case 'restore': openRestore(job); return;
      case 'preview': openPreview(job); return;
      case 'delete':
        confirmDialog(t('confirm.deleteTitle'), t('confirm.deleteText', { name: job.name }), t('act.delete'), async () => {
          await api(`/jobs/${job.id}`, { method: 'DELETE' });
          await loadJobs();
        });
        return;
      default: return;
    }
    await loadJobs();
  } catch (err) {
    showBanner(describeError(err), 'bad');
  }
}

/* ---------- new backup: what → where → start ---------- */

// One screen. Picking a folder and a drive is all a new backup needs; the
// name, the schedule (daily 03:00), the retention and the passphrase have
// defaults that the summary line spells out. Everything else waits under
// "Advanced", the server targets under "experts" — measured against the
// ZimaOS 1.7.1 backup dialog, which asks for a source, a target and Start.
const wiz = { editing: null, sources: [], remote: '', drive: null, generated: '', autoPath: '', target: 'local', stats: {} };

const DEFAULT_JOB = {
  kind: 'backup', name: '', excludes: [], target: { type: 'local', path: '' },
  schedule: { type: 'cron', cron_expr: '0 3 * * *' }, retention: { keep_last: 7, keep_daily: 7, keep_weekly: 4, keep_monthly: 6 },
  delete_extraneous: false, enabled: true, timeout_min: 0, notifications: [],
};

function openWizard(job) {
  wiz.editing = job || null;
  wiz.sources = job ? [...job.sources] : [];
  wiz.drive = null;
  wiz.generated = '';
  wiz.autoPath = '';
  wiz.stats = {};
  wiz.emptyConfirmed = false;
  $('#jobModalTitle').textContent = job ? t('wizard.editTitle', { name: job.name }) : t('wizard.newTitle');
  $('#jobSaveBtn').textContent = job ? t('common.save') : t('wizard.start');
  $('#jobNotice').hidden = true;
  $('#advancedBox').open = false;
  fillWizard(job);
  loadSSHKey();
  loadVolumes();
  $('#jobModal').hidden = false;
}

function fillWizard(job) {
  const j = job || DEFAULT_JOB;
  $(`input[name="kind"][value="${j.kind}"]`).checked = true;
  $$('input[name="kind"]').forEach((r) => { r.disabled = !!job; }); // the kind of an existing job is fixed
  $('#nameInput').value = j.name;
  $('#excludesInput').value = (j.excludes || []).join('\n');
  renderSources();

  const tg = j.target;
  const expert = !['local', 'cloud'].includes(tg.type);
  $('#targetType').value = expert ? tg.type : 'local';
  $('#expertBox').open = expert;
  $('#targetPath').value = tg.type === 'local' ? tg.path : '';
  $('#cloudPath').value = tg.type === 'cloud' ? tg.path : '';
  wiz.remote = tg.type === 'cloud' ? tg.remote : '';
  wiz.target = tg.type; // local | cloud | ssh | sftp | smb | s3
  $('#targetRemotePath').value = expert ? (tg.path || '') : '';
  $('#targetHost').value = tg.host || '';
  $('#targetPort').value = tg.port || '';
  $('#targetUser').value = tg.user || '';
  $('#targetSecret').value = '';
  $('#targetSecret').placeholder = job && expert && tg.type !== 'ssh' ? t('field.unchanged') : '';
  $('#targetShare').value = tg.share || '';
  $('#targetBucket').value = tg.bucket || '';
  $('#targetRegion').value = tg.region || '';
  $('#targetInsecure').checked = !!tg.insecure;
  $('#passphraseInput').value = '';
  $('#passphraseInput').placeholder = job ? t('field.unchanged') : '';
  $('#deleteExtraneous').checked = !!j.delete_extraneous;

  fillSchedule(j.schedule || { type: 'manual' });
  const r = j.retention || {};
  $('#keepLast').value = r.keep_last || 0;
  $('#keepDaily').value = r.keep_daily || 0;
  $('#keepWeekly').value = r.keep_weekly || 0;
  $('#keepMonthly').value = r.keep_monthly || 0;
  $('#timeoutInput').value = j.timeout_min || 0;
  const tg1 = (j.notifications || []).find((n) => n.type === 'telegram');
  const wh = (j.notifications || []).find((n) => n.type === 'webhook');
  $('#notifyTelegram').checked = !!tg1;
  $('#webhookUrl').value = wh ? wh.target : '';
  $('#webhookFormat').value = (wh && wh.webhook_format) || 'generic';
  const any = tg1 || wh;
  $('#notifyOnSuccess').checked = any ? !!any.on_success : false;
  $('#notifyOnFailure').checked = any ? any.on_failure !== false : true;
  $('#enabledInput').checked = j.enabled !== false;

  updateKindFields();
  updateTargetFields();
  updateScheduleFields();
  validateSchedule();
}

function currentKind() { return $('input[name="kind"]:checked').value; }

function updateKindFields() {
  const kind = currentKind();
  $$('.kind-fields').forEach((el) => { el.hidden = el.dataset.kind !== kind; });
  markVolume();
  renderSummary();
}

// updateTargetFields shows the rows the chosen target needs: the folder
// row for a drive, the cloud folder for a cloud drive, the expert fields
// for a server. wiz.target is the source of truth, the expert select
// follows it.
function updateTargetFields() {
  const type = wiz.target || 'local';
  const expert = !['local', 'cloud'].includes(type);
  $$('.target-fields').forEach((el) => { el.hidden = !el.dataset.for.split(' ').includes(type); });
  $('#discoverList').hidden = true;
  $('#targetFolderRow').hidden = type !== 'local' || !$('#targetPath').value;
  $('#cloudFolderRow').hidden = type !== 'cloud';
  $('#expertTargetRow').hidden = !expert;
  if (expert) $('#expertTargetSummary').textContent = t('target.expertChosen', { type: t(`target.${type}Short`) });
  $('#targetSecretField').hidden = type === 'ssh';
  $('#targetHostLabel').textContent = type === 's3' ? t('field.endpoint') : t('field.host');
  $('#targetUserLabel').textContent = type === 's3' ? t('field.accessKey') : t('field.user');
  $('#targetSecretLabel').textContent = type === 's3' ? t('field.secretKey') : t('field.password');
  $('#targetRemotePathLabel').textContent = type === 's3' ? t('field.prefix') : (type === 'smb' ? t('field.shareFolder') : t('field.remotePath'));
  $('#sftpPasswordHint').hidden = type !== 'sftp';
  if (volumes.list.length) markVolume();
  renderSummary();
}

// renderSources draws one chip per folder and fills in what it holds
// ("6 files · 346 kB", or "empty" in yellow) from GET /api/folders/stat —
// the tester backed up an empty twin of the folder he meant and got a
// green "0 files".
function renderSources() {
  const list = $('#sourceList');
  list.innerHTML = wiz.sources.map((s, i) => `<span class="chip" data-src="${esc(s)}"><code title="${esc(s)}">${esc(s)}</code><span class="stat muted">…</span><button type="button" data-remove="${i}" aria-label="remove">&times;</button></span>`).join('');
  if (!wiz.sources.length) list.innerHTML = `<span class="muted">${t('field.noSources')}</span>`;
  wiz.sources.forEach(async (src) => {
    const chip = list.querySelector(`.chip[data-src="${CSS.escape(src)}"] .stat`);
    if (!chip) return;
    try {
      const st = await api(`/folders/stat?path=${encodeURIComponent(src)}`);
      wiz.stats[src] = st;
      if (st.files === 0 && !st.truncated) {
        chip.className = 'stat pill warn';
        chip.textContent = t('source.empty');
      } else {
        chip.className = 'stat muted';
        chip.textContent = `${st.truncated ? '≥ ' : ''}${t('source.files', { n: st.files })} · ${fmtBytes(st.bytes)}`;
      }
    } catch {
      chip.textContent = '';
    }
  });
  $('#flowWhat').classList.toggle('done', wiz.sources.length > 0);
  refreshAutoPath();
  renderSummary();
}

async function loadSSHKey() {
  try {
    const k = await api('/sshkey');
    $('#sshKeyText').textContent = k.public_key;
  } catch (err) {
    $('#sshKeyText').textContent = describeError(err);
  }
}

/* ----- schedule as words ----- */

// The list shapes mirror schedule.Form on the server (lintux-modkit):
// daily, weekly, monthly, hourly, minutes render to a cron expression and
// only exactly those expressions read back as words — anything else stays
// a cron expression in the list and in the editor. Never paraphrase what
// the form cannot reproduce.
function scheduleFromForm() {
  const kind = $('#scheduleKind').value;
  const [hh, mm] = ($('#schedTime').value || '03:00').split(':').map(Number);
  switch (kind) {
    case 'daily': return { type: 'cron', cron_expr: `${mm} ${hh} * * *` };
    case 'weekly': return { type: 'cron', cron_expr: `${mm} ${hh} * * ${Number($('#schedWeekday').value)}` };
    case 'monthly': return { type: 'cron', cron_expr: `${mm} ${hh} ${Math.min(28, Math.max(1, Number($('#schedDay').value) || 1))} * *` };
    case 'hourly': return { type: 'cron', cron_expr: `${Math.min(59, Math.max(0, Number($('#schedMinute').value) || 0))} * * * *` };
    case 'minutes': return { type: 'cron', cron_expr: `*/${Number($('#schedEvery').value)} * * * *` };
    case 'interval': return { type: 'interval', interval_min: Math.max(1, Number($('#intervalHours').value) || 24) * 60 };
    case 'cron': return { type: 'cron', cron_expr: $('#cronInput').value.trim() };
    default: return { type: 'manual' };
  }
}

// formOf reads a saved schedule back into the words the editor and the
// list show. Strict on purpose: a bare number in each field, nothing else.
function formOf(s) {
  if (!s || s.type === 'manual') return { kind: 'manual' };
  if (s.type === 'interval') return { kind: 'interval', hours: Math.max(1, Math.round((s.interval_min || 60) / 60)) };
  const f = (s.cron_expr || '').trim().split(/\s+/);
  const num = (x, lo, hi) => (/^\d+$/.test(x) && Number(x) >= lo && Number(x) <= hi ? Number(x) : null);
  if (f.length === 5) {
    const [mi, ho, dom, mon, dow] = [num(f[0], 0, 59), num(f[1], 0, 23), num(f[2], 1, 28), f[3], num(f[4], 0, 7)];
    const star = (x) => x === '*';
    if (mi !== null && ho !== null && star(f[2]) && star(mon) && star(f[4])) return { kind: 'daily', hour: ho, minute: mi };
    if (mi !== null && ho !== null && star(f[2]) && star(mon) && dow !== null) return { kind: 'weekly', hour: ho, minute: mi, weekday: dow % 7 };
    if (mi !== null && ho !== null && dom !== null && star(mon) && star(f[4])) return { kind: 'monthly', hour: ho, minute: mi, day: dom };
    if (mi !== null && star(f[1]) && star(f[2]) && star(mon) && star(f[4])) return { kind: 'hourly', minute: mi };
    const m = /^\*\/(\d+)$/.exec(f[0]);
    if (m && star(f[1]) && star(f[2]) && star(mon) && star(f[4]) && [5, 10, 15, 20, 30].includes(Number(m[1]))) return { kind: 'minutes', every: Number(m[1]) };
  }
  return { kind: 'cron', expr: s.cron_expr || '' };
}

const pad2 = (n) => String(n).padStart(2, '0');

// scheduleWords: the sentence for the list and the summary line.
function scheduleWords(s) {
  const f = formOf(s);
  switch (f.kind) {
    case 'daily': return t('sched.words.daily', { time: `${pad2(f.hour)}:${pad2(f.minute)}` });
    case 'weekly': return t('sched.words.weekly', { day: t(`day.${f.weekday}`), time: `${pad2(f.hour)}:${pad2(f.minute)}` });
    case 'monthly': return t('sched.words.monthly', { day: f.day, time: `${pad2(f.hour)}:${pad2(f.minute)}` });
    case 'hourly': return f.minute ? t('sched.words.hourlyAt', { minute: pad2(f.minute) }) : t('sched.words.hourly');
    case 'minutes': return t('sched.words.minutes', { n: f.every });
    case 'interval': return f.hours === 24 ? t('schedule.everyDay') : (f.hours % 24 === 0 ? t('schedule.everyDays', { n: f.hours / 24 }) : (f.hours === 1 ? t('schedule.everyHour') : t('schedule.everyHours', { n: f.hours })));
    case 'cron': return `<code>${esc(f.expr)}</code>`;
    default: return t('sched.manual');
  }
}

function fillSchedule(s) {
  const f = formOf(s);
  $('#scheduleKind').value = f.kind;
  $('#schedTime').value = `${pad2(f.hour ?? 3)}:${pad2(f.minute ?? 0)}`;
  $('#schedWeekday').value = String(f.weekday ?? 0);
  $('#schedDay').value = f.day ?? 1;
  $('#schedMinute').value = f.kind === 'hourly' ? f.minute : 0;
  $('#schedEvery').value = String(f.every ?? 15);
  $('#intervalHours').value = f.hours ?? 24;
  $('#cronInput').value = f.kind === 'cron' ? f.expr : (s.cron_expr || '0 3 * * *');
}

function updateScheduleFields() {
  const kind = $('#scheduleKind').value;
  $$('[data-sched]').forEach((el) => { el.hidden = !el.dataset.sched.split(' ').includes(kind); });
  renderSummary();
}

let schedTimer = null;
function validateSchedule() {
  clearTimeout(schedTimer);
  const fb = $('#cronFeedback');
  const s = scheduleFromForm();
  if (s.type === 'manual' || (s.type === 'cron' && !s.cron_expr)) { fb.textContent = ''; return; }
  schedTimer = setTimeout(async () => {
    try {
      const v = await api('/schedule/validate', { method: 'POST', body: s });
      if (!v.valid) {
        fb.className = 'cron-feedback bad';
        fb.textContent = v.errors.map((e) => e.message).join(' · ') || t('error.cron_invalid');
        return;
      }
      fb.className = 'cron-feedback ok';
      fb.innerHTML = `<span class="next">${t('cron.next')}: ${v.next_runs.slice(0, 3).map(fmtTime).join(' · ')}</span>`;
    } catch (err) {
      fb.className = 'cron-feedback bad';
      fb.textContent = describeError(err);
    }
  }, 300);
}

/* ----- summary, name, checks ----- */

// renderSummary is the one grey line under "Where": what a Start does
// with nothing changed under Advanced.
function renderSummary() {
  const kind = currentKind();
  const parts = [t(`kind.${kind}`)];
  parts.push(scheduleWords(scheduleFromForm()));
  if (kind === 'backup') {
    const keep = [['keepDaily', 'summary.days'], ['keepWeekly', 'summary.weeks'], ['keepMonthly', 'summary.months']]
      .map(([id, key]) => [Number($(`#${id}`).value) || 0, key]).filter(([n]) => n > 0)
      .map(([n, key]) => t(key, { n })).join(', ');
    parts.push(keep ? t('summary.keeps', { list: keep }) : t('summary.keepsAll'));
    parts.push(t('summary.encrypted'));
  } else {
    parts.push($('#deleteExtraneous').checked ? t('summary.mirrorDeletes') : t('summary.mirrorGrows'));
  }
  $('#planSummary').innerHTML = parts.join(' · ');
  // a sync to a cloud drive uploads every file on its own — Google Drive
  // manages about one file per second, whatever its size (measured);
  // a backup packs files and runs at line speed
  const slow = kind === 'sync' && wiz.target === 'cloud';
  $('#cloudSyncHint').hidden = !slow;
}

function baseName(p) { return (p || '').replace(/\/+$/, '').split('/').pop() || p; }

// autoName: "<folder> → <drive>" unless the user typed one.
function autoName(job) {
  const typed = $('#nameInput').value.trim();
  if (typed) return typed;
  const what = job.sources.length === 1 ? baseName(job.sources[0]) : t('name.folders', { n: job.sources.length });
  let where = '';
  if (job.target.type === 'local') where = wiz.drive ? wiz.drive.name : baseName(job.target.path);
  else if (job.target.type === 'cloud') where = (volumes.remotes.find((r) => r.remote === job.target.remote) || {}).name || t('vol.cloud');
  else where = job.target.host || t(`target.${job.target.type}Short`);
  return `${what} → ${where}`.slice(0, 80);
}

// sameTarget says whether two targets name the same repository folder.
function sameTarget(a, b) {
  const norm = (p) => (p || '').replace(/^\/+|\/+$/g, '');
  return a.type === b.type && norm(a.path) === norm(b.path) && (a.remote || '') === (b.remote || '')
    && (a.host || '') === (b.host || '') && (a.share || '') === (b.share || '') && (a.bucket || '') === (b.bucket || '');
}

function checkWizard(job) {
  if (!job.sources.length) return t('error.sources_required');
  const tg = job.target;
  if (tg.type === 'local' && !tg.path) return t('error.target_required');
  if (tg.type === 'cloud' && (!tg.remote || !tg.path)) return t('error.target_incomplete');
  if (!['local', 'cloud'].includes(tg.type) && (!tg.host || !tg.user)) return t('error.target_incomplete');
  if ((tg.type === 'ssh' || tg.type === 'sftp') && !tg.path) return t('error.target_incomplete');
  if (tg.type === 'smb' && !tg.share) return t('error.target_incomplete');
  if (tg.type === 's3' && !tg.bucket) return t('error.target_incomplete');
  if (job.schedule.type === 'cron' && !job.schedule.cron_expr) return t('error.cron_invalid');
  return '';
}

function showJobNotice(text) {
  const n = $('#jobNotice');
  n.textContent = text;
  n.hidden = false;
  n.scrollIntoView({ block: 'nearest' });
}

function readWizard() {
  const type = wiz.target || 'local';
  const target = { type };
  if (type === 'local') target.path = $('#targetPath').value.trim();
  else if (type === 'cloud') {
    target.remote = wiz.remote;
    target.path = $('#cloudPath').value.trim();
  } else {
    target.path = $('#targetRemotePath').value.trim();
    target.host = $('#targetHost').value.trim();
    target.port = Number($('#targetPort').value) || 0;
    target.user = $('#targetUser').value.trim();
    if (type !== 'ssh' && $('#targetSecret').value) target.secret = $('#targetSecret').value;
    if (type === 'smb') target.share = $('#targetShare').value.trim();
    if (type === 's3') {
      target.bucket = $('#targetBucket').value.trim();
      target.region = $('#targetRegion').value.trim();
      target.insecure = $('#targetInsecure').checked;
    }
  }
  const notifications = [];
  const onS = $('#notifyOnSuccess').checked;
  const onF = $('#notifyOnFailure').checked;
  if ($('#notifyTelegram').checked) notifications.push({ enabled: true, type: 'telegram', target: '', on_success: onS, on_failure: onF });
  if ($('#webhookUrl').value.trim()) notifications.push({ enabled: true, type: 'webhook', target: $('#webhookUrl').value.trim(), webhook_format: $('#webhookFormat').value, on_success: onS, on_failure: onF });
  const job = {
    kind: currentKind(),
    sources: [...wiz.sources],
    excludes: $('#excludesInput').value.split('\n').map((s) => s.trim()).filter(Boolean),
    target, schedule: scheduleFromForm(), notifications,
    enabled: $('#enabledInput').checked,
    timeout_min: Number($('#timeoutInput').value) || 0,
  };
  job.name = autoName(job);
  if (job.kind === 'backup') {
    job.retention = { keep_last: Number($('#keepLast').value) || 0, keep_daily: Number($('#keepDaily').value) || 0, keep_weekly: Number($('#keepWeekly').value) || 0, keep_monthly: Number($('#keepMonthly').value) || 0 };
    if ($('#passphraseInput').value) job.passphrase = $('#passphraseInput').value;
  } else {
    job.delete_extraneous = $('#deleteExtraneous').checked;
  }
  return job;
}

/* ----- passphrase, generated and shown once ----- */

// Seven words from the EFF short list (1296 words → about 72 bits), drawn
// with crypto.getRandomValues and rejection sampling so no word is
// favoured. Shown once; the server never returns a passphrase.
function generatePassphrase() {
  const words = window.ZBACKUP_WORDS || [];
  if (words.length < 1000 || !window.crypto || !crypto.getRandomValues) throw new Error('wordlist or crypto missing');
  const out = [];
  const buf = new Uint16Array(1);
  while (out.length < 7) {
    crypto.getRandomValues(buf);
    if (buf[0] < 65535 - (65535 % words.length)) out.push(words[buf[0] % words.length]);
  }
  return out.join(' ');
}

function openPassModal(words) {
  $('#passWords').innerHTML = words.split(' ').map((w, i) => `<span class="word"><small>${i + 1}</small>${esc(w)}</span>`).join('');
  $('#passSavedCheck').checked = false;
  $('#passOkBtn').disabled = true;
  $('#passCopyBtn').textContent = t('field.copy');
  $('#passModal').hidden = false;
}

// printPassphrase opens the print dialog with the words on one sheet —
// paper, or "Save as PDF" from the dialog. Not a download: ZimaOS is
// served over plain http in the LAN, and Chrome flags every download
// from such a page as insecure (seen by the tester with the .txt file).
function printPassphrase(name, words) {
  const frame = document.createElement('iframe');
  frame.style.cssText = 'position:fixed;right:0;bottom:0;width:0;height:0;border:0;';
  document.body.appendChild(frame);
  const doc = frame.contentDocument;
  doc.open();
  doc.write(`<!doctype html><html><head><meta charset="utf-8"><title>${esc(t('pass.title'))}</title>
    <style>body{font-family:system-ui,sans-serif;margin:40px;color:#111}h1{font-size:20px;margin:0 0 4px}p{margin:6px 0;font-size:14px}
    .words{display:grid;grid-template-columns:1fr 1fr;gap:10px 24px;margin:24px 0}.w{font:600 22px ui-monospace,monospace;padding:8px 0;border-bottom:1px solid #ccc}
    .w small{font:400 12px system-ui;color:#666;margin-right:10px}.foot{font-size:12px;color:#555;margin-top:24px}</style></head><body>
    <h1>${esc(t('pass.fileHead', { name }))}</h1><p>${esc(new Date().toLocaleString())}</p>
    <div class="words">${words.split(' ').map((w, i) => `<div class="w"><small>${i + 1}</small>${esc(w)}</div>`).join('')}</div>
    <p class="foot">${esc(t('pass.fileFoot'))}</p></body></html>`);
  doc.close();
  const done = () => setTimeout(() => frame.remove(), 1000);
  frame.contentWindow.onafterprint = done;
  frame.contentWindow.focus();
  frame.contentWindow.print();
  setTimeout(done, 60000); // browsers without onafterprint
}

/* ----- save / start ----- */

async function saveJob() {
  const job = readWizard();
  const problem = checkWizard(job);
  if (problem) { showJobNotice(problem); return; }
  const empty = job.sources.filter((src) => wiz.stats[src] && wiz.stats[src].files === 0 && !wiz.stats[src].truncated);
  if (empty.length && !wiz.emptyConfirmed) {
    // say it once; a second Start goes ahead — the user may know better
    wiz.emptyConfirmed = true;
    showJobNotice(t('error.source_empty', { name: empty.map(baseName).join(', ') }));
    return;
  }
  if (!wiz.editing && job.kind === 'backup' && !job.passphrase) {
    // another backup job already writes here: its repository has its
    // passphrase, a fresh one would only produce "wrong password"
    const twin = state.jobs.find((j) => j.kind === 'backup' && sameTarget(j.target, job.target));
    if (twin) { showJobNotice(t('error.target_shared', { name: twin.name })); return; }
    // no passphrase typed under Advanced: generate one and show it once
    try {
      wiz.generated = generatePassphrase();
    } catch (err) {
      showJobNotice(t('error.generic', { msg: err.message }));
      return;
    }
    openPassModal(wiz.generated);
    return;
  }
  await submitJob(job);
}

// submitJob creates or updates the job. A new job runs right away —
// that is what Start promises; the schedule takes over afterwards. A new
// sync job opens its preview first, as before.
async function submitJob(job) {
  const btn = $('#jobSaveBtn');
  btn.disabled = true;
  $('#passOkBtn').disabled = true;
  try {
    const saved = wiz.editing
      ? await api(`/jobs/${wiz.editing.id}`, { method: 'PUT', body: job })
      : await api('/jobs', { method: 'POST', body: job });
    $('#passModal').hidden = true;
    $('#jobModal').hidden = true;
    wiz.generated = '';
    const isNew = !wiz.editing;
    if (isNew && saved.kind === 'backup' && saved.enabled) {
      try { await api(`/jobs/${saved.id}/run`, { method: 'POST' }); } catch (err) { showBanner(describeError(err), 'bad'); }
    }
    await loadJobs();
    if (isNew && saved.kind === 'sync') openPreview(saved);
  } catch (err) {
    $('#passModal').hidden = true;
    showJobNotice(describeError(err));
  } finally {
    btn.disabled = false;
  }
}

/* ---------- drives ---------- */

const volumes = { list: [], remotes: [] };

// GET /api/mounts: system disk, pools, USB disks, LAN shares connected in
// Files, cloud drives — one click makes "<drive>/Backups" the target.
async function loadVolumes() {
  const el = $('#volumeList');
  el.innerHTML = `<span class="muted">${t('common.loading')}</span>`;
  try {
    const res = await api('/mounts');
    volumes.list = res.volumes;
    volumes.remotes = res.remotes || [];
    // cloud drives ZimaOS is signed in to but Files has not mounted still count
    for (const r of volumes.remotes) {
      if (!volumes.list.some((v) => v.kind === 'cloud' && v.remote === r.remote)) volumes.list.push({ name: r.name, kind: 'cloud', remote: r.remote, path: '' });
    }
    if (!volumes.list.length) { el.innerHTML = `<span class="muted">${t('volumes.none')}</span>`; return; }
    // three groups — drives in the box, shares on the network, cloud —
    // with a heading each once more than one group is present (a box with
    // two network servers connected shows eight shares)
    const groupOf = (v) => (v.kind === 'lan' ? 'lan' : (v.kind === 'cloud' ? 'cloud' : 'drives'));
    const groups = ['drives', 'lan', 'cloud'].map((g) => [g, volumes.list.filter((v) => groupOf(v) === g)]).filter(([, vs]) => vs.length);
    const card = (v) => `
      <button type="button" class="volume" data-path="${esc(v.path)}" data-kind="${v.kind}" data-remote="${esc(v.remote || '')}" data-name="${esc(v.name)}">
        <span class="name" title="${esc(v.path)}">${esc(v.name)}${v.host ? ` <span class="muted">@ ${esc(shortHost(v.host))}</span>` : ''}</span>
        <span class="sub"><span class="pill ${v.kind === 'cloud' ? 'warn' : (v.kind === 'system' ? 'accent' : '')}">${t(`vol.${v.kind}`)}</span>${v.size ? esc(t('volumes.free', { free: fmtBytes(v.free), size: fmtBytes(v.size) })) : ''}</span>
      </button>`;
    el.innerHTML = groups.map(([g, vs]) => `${groups.length > 1 ? `<div class="volume-group">${t(`vol.group.${g}`)}</div>` : ''}${vs.map(card).join('')}`).join('');
    markVolume();
  } catch (err) {
    el.innerHTML = `<span class="muted">${esc(describeError(err))}</span>`;
  }
}

// shortHost trims a Tailscale MagicDNS name to its first label.
function shortHost(h) { return /\.ts\.net$/.test(h) ? h.split('.')[0] : h; }

// markVolume highlights the drive the current target lies on.
function markVolume() {
  const type = wiz.target || 'local';
  const path = $('#targetPath').value.trim();
  wiz.drive = null;
  $$('#volumeList .volume').forEach((b) => {
    const on = type === 'cloud'
      ? b.dataset.kind === 'cloud' && b.dataset.remote === wiz.remote
      : type === 'local' && b.dataset.kind !== 'cloud' && (path === b.dataset.path || path.startsWith(b.dataset.path + '/'));
    b.classList.toggle('selected', on);
    if (on) wiz.drive = { name: b.dataset.name, path: b.dataset.path, kind: b.dataset.kind };
  });
  $('#flowWhere').classList.toggle('done', !!wiz.drive || (!['local', 'cloud'].includes(type) && !!$('#targetHost').value.trim()));
}

// slug makes a folder name out of the first source: "/DATA/Fotos 2024" →
// "Fotos_2024".
function slug(p) {
  return baseName(p).replace(/[^\w.-]+/g, '_').replace(/^_+|_+$/g, '') || 'Backup';
}

// defaultFolder is "Backups/<first source>" — one repository per job. Two
// backup jobs in the same folder would have to share one passphrase; a
// generated passphrase never matches, and restic then answers "wrong
// password" (seen on the tester's box on 2026-09-20 with two jobs on
// "Backups").
function defaultFolder() {
  return `Backups/${wiz.sources.length ? slug(wiz.sources[0]) : 'Backup'}`;
}

// chooseVolume is the click on a drive card.
function chooseVolume(b) {
  if (b.dataset.kind === 'cloud') {
    wiz.target = 'cloud';
    wiz.remote = b.dataset.remote;
    wiz.autoPath = defaultFolder();
    $('#cloudPath').value = wiz.autoPath;
  } else {
    wiz.target = 'local';
    wiz.autoPath = `${b.dataset.path}/${defaultFolder()}`;
    $('#targetPath').value = wiz.autoPath;
  }
  $('#targetType').value = 'local';
  updateTargetFields();
}

// refreshAutoPath follows a later source pick while the folder is still
// the suggested one; a folder the user changed is left alone.
function refreshAutoPath() {
  if (!wiz.autoPath || !wiz.drive && wiz.target !== 'cloud') return;
  const field = wiz.target === 'cloud' ? $('#cloudPath') : $('#targetPath');
  if (field.value.trim() !== wiz.autoPath) return;
  wiz.autoPath = wiz.target === 'cloud' ? defaultFolder() : `${wiz.drive.path}/${defaultFolder()}`;
  field.value = wiz.autoPath;
}

/* ---------- connect a network share (through Files) ---------- */

// Files mounts every share of a server as CIFS under /media/<host>/<share>
// once POST /v2_1/files/connect succeeds (measured on ZimaOS 1.7.1); the
// same session token our API uses is accepted there. The mount then shows
// up as a "lan" drive in GET /api/mounts.
async function filesApi(path, opts = {}) {
  const headers = { Accept: 'application/json' };
  if (opts.body !== undefined) headers['Content-Type'] = 'application/json';
  const token = safeGet('access_token');
  if (token) headers.Authorization = `Bearer ${token}`;
  const res = await fetch('/v2_1/files' + path, { ...opts, headers, body: opts.body !== undefined ? JSON.stringify(opts.body) : undefined });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new ApiError(res.status, 'files', (data && data.message) || `HTTP ${res.status}`);
  return data;
}

function openConnect() {
  $('#connectNotice').hidden = true;
  $('#connectHost').value = '';
  $('#connectUser').value = '';
  $('#connectPassword').value = '';
  $('#connectGuest').checked = false;
  $('#connectCredentials').hidden = false;
  $('#connectDiscoverList').hidden = true;
  $('#connectModal').hidden = false;
  $('#connectHost').focus();
}

async function connectShare() {
  const host = $('#connectHost').value.trim();
  const guest = $('#connectGuest').checked;
  const user = $('#connectUser').value.trim();
  if (!host || (!guest && !user)) { $('#connectNotice').textContent = t('lan.incomplete'); $('#connectNotice').hidden = false; return; }
  const btn = $('#connectOkBtn');
  btn.disabled = true;
  try {
    await filesApi('/connect', { method: 'POST', body: guest ? { host, username: 'guest', password: '' } : { host, username: user, password: $('#connectPassword').value } });
    $('#connectModal').hidden = true;
    await loadVolumes();
  } catch (err) {
    $('#connectNotice').textContent = t('lan.failed', { msg: err.message });
    $('#connectNotice').hidden = false;
  } finally {
    btn.disabled = false;
  }
}

/* ---------- network discovery ---------- */

// GET /api/discover listens for a couple of seconds; the list fills in
// once, a click copies the host into the field that asked.
async function discoverHosts(listEl, btn, onPick) {
  listEl.hidden = false;
  listEl.innerHTML = `<div class="empty muted">${t('discover.searching')}</div>`;
  btn.disabled = true;
  try {
    const res = await api('/discover');
    if (!res.hosts.length) {
      listEl.innerHTML = `<div class="empty">${t('discover.none')}</div>`;
      return;
    }
    listEl.innerHTML = res.hosts.map((h) => `
      <div class="row" data-host="${esc(h.host)}">
        <span class="icon">${h.os === 'ZimaOS' ? '&#9673;' : '&#9675;'}</span>
        <span>${esc(h.name)}</span>
        <span class="pill ${h.os === 'ZimaOS' ? 'accent' : ''}">${esc(h.os || '?')}</span>
        <span class="pill">${t(`net.${h.network}`)}</span>
        <span class="addr size">${esc(h.host)}</span>
      </div>`).join('');
    listEl.onclick = (ev) => {
      const row = ev.target.closest('.row[data-host]');
      if (!row) return;
      listEl.hidden = true;
      onPick(row.dataset.host);
    };
  } catch (err) {
    listEl.innerHTML = `<div class="empty">${esc(describeError(err))}</div>`;
  } finally {
    btn.disabled = false;
  }
}

/* ---------- folder picker ---------- */

const picker = { path: '', onChoose: null };

function openPicker(start, onChoose) {
  picker.onChoose = onChoose;
  $('#pickerModal').hidden = false;
  browsePicker(start || '');
}

async function browsePicker(path) {
  picker.path = path;
  $('#pickerCurrent').textContent = path || '/';
  $('#pickerOkBtn').disabled = !path;
  renderCrumbs($('#pickerCrumbs'), path, browsePicker);
  const list = $('#pickerList');
  list.innerHTML = `<div class="empty muted">${t('common.loading')}</div>`;
  try {
    const entries = await api(`/folders?path=${encodeURIComponent(path || '/')}`);
    list.innerHTML = entries.length
      ? entries.map((e) => `<div class="row" data-path="${esc(e.path)}"><span class="icon">${e.kind ? '&#128190;' : '&#128193;'}</span><span>${esc(e.name)}</span>${e.kind ? `<span class="pill ${e.kind === 'cloud' ? 'warn' : ''}">${t(`vol.${e.kind}`)}</span><span class="size mono">${esc(e.path)}</span>` : ''}</div>`).join('')
      : `<div class="empty">${t('picker.empty')}</div>`;
  } catch (err) {
    list.innerHTML = `<div class="empty">${esc(describeError(err))}</div>`;
  }
}

function renderCrumbs(el, path, onNav) {
  const parts = path.split('/').filter(Boolean);
  let html = `<button type="button" data-path="">${t('picker.root')}</button>`;
  let acc = '';
  for (const p of parts) {
    acc += '/' + p;
    html += `<span class="sep">/</span><button type="button" data-path="${esc(acc)}">${esc(p)}</button>`;
  }
  el.innerHTML = html;
  $$('button', el).forEach((b) => b.addEventListener('click', () => onNav(b.dataset.path)));
}

/* ---------- history ---------- */

const hist = { job: null };

async function openHistory(job) {
  hist.job = job;
  $('#historyTitle').textContent = t('history.titleFor', { name: job.name });
  $('#historyModal').hidden = false;
  $('#historyList').innerHTML = `<div class="empty muted">${t('common.loading')}</div>`;
  $('#historySpark').innerHTML = '';
  try {
    const entries = await api(`/jobs/${job.id}/logs`);
    renderHistory(entries);
  } catch (err) {
    $('#historyList').innerHTML = `<div class="empty">${esc(describeError(err))}</div>`;
  }
}

function renderHistory(entries) {
  $('#historySpark').innerHTML = entries.slice(0, 40).reverse().map((e) => `<i class="${e.success ? '' : (e.code === 'partial' ? 'warn' : 'bad')}" title="${esc(fmtTime(e.time))}"></i>`).join('');
  if (!entries.length) { $('#historyList').innerHTML = `<div class="empty">${t('history.empty')}</div>`; return; }
  $('#historyList').innerHTML = entries.map((e) => {
    const label = LANGS.en[`code.${e.code}`] ? t(`code.${e.code}`) : e.code;
    const cls = e.success ? 'ok' : (e.code === 'partial' ? 'warn' : 'bad');
    return `<div class="log-item"><span class="time">${fmtTime(e.time)}</span><span class="pill ${cls}">${label}</span><span class="msg">${esc(e.message)}</span><span class="dur">${fmtDuration(e.duration_ms)}</span></div>`;
  }).join('');
}

function clearHistory() {
  const job = hist.job;
  confirmDialog(t('history.clear'), t('history.clearText', { name: job.name }), t('history.clear'), async () => {
    await api(`/jobs/${job.id}/logs/clear`, { method: 'POST' });
    renderHistory([]);
  });
}

/* ---------- run output ---------- */

const out = { job: null, timer: null };

// The log window shows the kept output of the current or last run and
// refreshes every two seconds while the job runs.
function openOutput(job) {
  out.job = job;
  $('#outputTitle').textContent = t('output.titleFor', { name: job.name });
  $('#outputLines').textContent = '';
  $('#outputModal').hidden = false;
  refreshOutput();
}

async function refreshOutput() {
  clearTimeout(out.timer);
  if (!out.job || $('#outputModal').hidden) return;
  try {
    const res = await api(`/jobs/${out.job.id}/output`);
    const phase = res.phase && LANGS.en[`phase.${res.phase}`] ? t(`phase.${res.phase}`) : res.phase;
    $('#outputState').textContent = res.running ? `${t('output.running')}${phase ? ` · ${phase}` : ''}` : (res.lines.length ? t('output.finished') : t('output.empty'));
    const pre = $('#outputLines');
    pre.innerHTML = res.lines.map((l) => `<span class="ts">${new Intl.DateTimeFormat(locale(), { timeStyle: 'medium' }).format(new Date(l.time))}</span>  <span class="${/error|Fatal|denied|failed/i.test(l.text) ? 'err' : ''}">${esc(l.text)}</span>`).join('\n');
    if ($('#outputFollow').checked) pre.scrollTop = pre.scrollHeight;
    if (res.running) out.timer = setTimeout(refreshOutput, 2000);
  } catch (err) {
    $('#outputState').textContent = describeError(err);
  }
}

/* ---------- restore ---------- */

const rst = { job: null, snapshot: '', path: '', selected: new Map() };

async function openRestore(job) {
  rst.job = job;
  rst.selected = new Map();
  rst.path = '';
  $('#restoreTitle').textContent = t('restore.titleFor', { name: job.name });
  $('#restoreNotice').hidden = true;
  $('#restoreTarget').value = '';
  $('#restoreTree').innerHTML = `<div class="empty muted">${t('common.loading')}</div>`;
  $('#restoreCrumbs').innerHTML = '';
  renderSelection();
  $('#restoreModal').hidden = false;
  const sel = $('#snapshotSelect');
  sel.innerHTML = `<option>${t('common.loading')}</option>`;
  try {
    const snaps = await api(`/jobs/${job.id}/snapshots`);
    if (!snaps.length) {
      sel.innerHTML = `<option value="">${t('restore.noSnapshots')}</option>`;
      $('#restoreTree').innerHTML = `<div class="empty">${t('restore.noSnapshots')}</div>`;
      return;
    }
    sel.innerHTML = snaps.map((s) => `<option value="${s.id}">${esc(fmtTime(Date.parse(s.time)))} · ${s.files} ${t('restore.files')} · ${fmtBytes(s.bytes)}</option>`).join('');
    rst.snapshot = snaps[0].id;
    browseSnapshot('');
  } catch (err) {
    showRestoreNotice(describeError(err));
  }
}

function showRestoreNotice(text) {
  const n = $('#restoreNotice');
  n.textContent = text;
  n.hidden = false;
}

async function browseSnapshot(path) {
  rst.path = path;
  renderCrumbs($('#restoreCrumbs'), path, browseSnapshot);
  const tree = $('#restoreTree');
  tree.innerHTML = `<div class="empty muted">${t('common.loading')}</div>`;
  try {
    const nodes = await api(`/jobs/${rst.job.id}/snapshots/${rst.snapshot}/ls?path=${encodeURIComponent(path)}`);
    if (!nodes.length) { tree.innerHTML = `<div class="empty">${t('picker.empty')}</div>`; return; }
    tree.innerHTML = nodes.map((n) => `
      <div class="row" data-path="${esc(n.path)}" data-type="${n.type}">
        <input type="checkbox" ${rst.selected.has(n.path) ? 'checked' : ''} aria-label="select">
        <span class="icon">${n.type === 'dir' ? '&#128193;' : '&#128196;'}</span>
        <span>${esc(n.name)}</span>
        <span class="size">${n.type === 'dir' ? '' : fmtBytes(n.size)}</span>
      </div>`).join('');
  } catch (err) {
    tree.innerHTML = `<div class="empty">${esc(describeError(err))}</div>`;
  }
}

function onTreeClick(ev) {
  const row = ev.target.closest('.row');
  if (!row) return;
  const box = $('input', row);
  if (ev.target === box) {
    if (box.checked) rst.selected.set(row.dataset.path, row.dataset.type); else rst.selected.delete(row.dataset.path);
    renderSelection();
    return;
  }
  if (row.dataset.type === 'dir') { browseSnapshot(row.dataset.path); return; }
  box.checked = !box.checked; // a click on a file row toggles its box
  if (box.checked) rst.selected.set(row.dataset.path, 'file'); else rst.selected.delete(row.dataset.path);
  renderSelection();
}

function renderSelection() {
  const el = $('#restoreSelection');
  el.innerHTML = [...rst.selected.keys()].map((p) => `<span class="chip file"><code title="${esc(p)}">${esc(p)}</code><button type="button" data-unselect="${esc(p)}" aria-label="remove">&times;</button></span>`).join('');
  $('#restoreOkBtn').disabled = rst.selected.size === 0;
}

async function startRestore() {
  const paths = [...rst.selected.keys()];
  const target = $('#restoreTarget').value.trim();
  const go = async () => {
    await api(`/jobs/${rst.job.id}/restore`, { method: 'POST', body: { snapshot: rst.snapshot, paths, target } });
    $('#restoreModal').hidden = true;
    showBanner(t('restore.started'), 'ok');
    loadJobs();
  };
  if (!target) {
    confirmDialog(t('restore.overwriteTitle'), t('restore.overwriteText', { n: paths.length }), t('restore.go'), go);
    return;
  }
  try { await go(); } catch (err) { showRestoreNotice(describeError(err)); }
}

async function startCheck() {
  try {
    await api(`/jobs/${rst.job.id}/check`, { method: 'POST' });
    $('#restoreModal').hidden = true;
    showBanner(t('restore.checkStarted'), 'ok');
    loadJobs();
  } catch (err) { showRestoreNotice(describeError(err)); }
}

/* ---------- preview ---------- */

const prev = { job: null };

async function openPreview(job) {
  prev.job = job;
  $('#previewModal').hidden = false;
  $('#previewText').textContent = t('preview.running');
  $('#previewText').hidden = false;
  $('#previewGrid').hidden = true;
  $('#previewWarn').hidden = true;
  $('#previewRunBtn').hidden = true;
  try {
    const p = await api(`/jobs/${job.id}/preview`, { method: 'POST' });
    const cells = [
      [t('preview.copy'), p.files_copy],
      [t('preview.delete'), p.files_delete],
      [t('preview.size'), fmtBytes(p.bytes)],
    ];
    if (p.detailed) cells.splice(1, 0, [t('preview.new'), p.files_new], [t('preview.changed'), p.files_changed]);
    $('#previewGrid').innerHTML = cells.map(([k, v]) => `<div class="stat"><div class="label">${k}</div><div class="value">${v}</div></div>`).join('');
    $('#previewGrid').hidden = false;
    $('#previewText').textContent = p.files_copy === 0 && p.files_delete === 0 ? t('preview.nothing') : t('preview.summary');
    if (p.files_delete > 0) {
      $('#previewWarn').textContent = t('preview.deleteWarn', { n: p.files_delete });
      $('#previewWarn').hidden = false;
    }
    $('#previewRunBtn').hidden = false;
  } catch (err) {
    $('#previewText').textContent = describeError(err);
  }
}

async function runFromPreview() {
  try {
    await api(`/jobs/${prev.job.id}/run`, { method: 'POST' });
    $('#previewModal').hidden = true;
    loadJobs();
  } catch (err) { showBanner(describeError(err), 'bad'); }
}

/* ---------- dialogs ---------- */

function confirmDialog(title, text, okLabel, onOk) {
  $('#confirmTitle').textContent = title;
  $('#confirmText').textContent = text;
  const ok = $('#confirmOkBtn');
  ok.textContent = okLabel;
  ok.onclick = async () => {
    try { await onOk(); } catch (err) { showBanner(describeError(err), 'bad'); }
    $('#confirmModal').hidden = true;
  };
  $('#confirmModal').hidden = false;
}

function showBanner(text, kind) {
  const b = $('#banner');
  b.className = `notice banner ${kind}`;
  b.textContent = text;
  b.hidden = false;
}
function hideBanner() { $('#banner').hidden = true; }

/* ---------- settings ---------- */

async function openSettings() {
  $('#settingsNotice').hidden = true;
  try {
    const s = await api('/settings');
    $('#tgTokenInput').value = s.telegram_bot_token || '';
    $('#tgChatInput').value = s.telegram_chat_id || '';
    $('#tgOnSuccess').checked = !!s.telegram_on_success;
    $('#tgOnFailure').checked = s.telegram_on_failure !== false;
    $('#settingsModal').hidden = false;
  } catch (err) {
    showBanner(describeError(err), 'bad');
  }
}

async function saveSettings() {
  try {
    await api('/settings', { method: 'PUT', body: {
      telegram_bot_token: $('#tgTokenInput').value.trim(), telegram_chat_id: $('#tgChatInput').value.trim(),
      telegram_on_success: $('#tgOnSuccess').checked, telegram_on_failure: $('#tgOnFailure').checked,
    } });
    $('#settingsModal').hidden = true;
    showBanner(t('settings.saved'), 'ok');
  } catch (err) {
    const n = $('#settingsNotice');
    n.className = 'notice bad';
    n.textContent = describeError(err);
    n.hidden = false;
  }
}

/* ---------- theme ---------- */

function initTheme() {
  if (safeGet('zbackup_theme') === 'dark') document.documentElement.dataset.theme = 'dark';
  updateThemeIcon();
}
function toggleTheme() {
  const dark = document.documentElement.dataset.theme === 'dark';
  if (dark) delete document.documentElement.dataset.theme; else document.documentElement.dataset.theme = 'dark';
  safeSet('zbackup_theme', dark ? 'light' : 'dark');
  updateThemeIcon();
}
function updateThemeIcon() { $('#themeToggle').innerHTML = document.documentElement.dataset.theme === 'dark' ? '&#9728;' : '&#9790;'; }

/* ---------- init ---------- */

function init() {
  lang = resolveLanguage();
  initTheme();
  applyI18n();

  $('#langSelect').addEventListener('change', (ev) => setLanguage(ev.target.value));
  $('#themeToggle').addEventListener('click', toggleTheme);
  $('#settingsBtn').addEventListener('click', openSettings);
  $('#settingsCancelBtn').addEventListener('click', () => { $('#settingsModal').hidden = true; });
  $('#settingsSaveBtn').addEventListener('click', saveSettings);

  $('#newJobBtn').addEventListener('click', () => openWizard(null));
  $('#jobList').addEventListener('click', onJobAction);
  $('#jobCancelBtn').addEventListener('click', () => { $('#jobModal').hidden = true; });
  $('#jobSaveBtn').addEventListener('click', saveJob);
  $$('input[name="kind"]').forEach((r) => r.addEventListener('change', updateKindFields));
  $('#targetType').addEventListener('change', (ev) => { wiz.target = ev.target.value === 'local' ? (wiz.remote ? 'cloud' : 'local') : ev.target.value; updateTargetFields(); });
  $('#scheduleKind').addEventListener('change', () => { updateScheduleFields(); validateSchedule(); });
  ['#schedTime', '#schedWeekday', '#schedDay', '#schedMinute', '#schedEvery', '#intervalHours', '#cronInput'].forEach((sel) => {
    $(sel).addEventListener('input', () => { renderSummary(); validateSchedule(); });
    $(sel).addEventListener('change', () => { renderSummary(); validateSchedule(); });
  });
  ['#keepDaily', '#keepWeekly', '#keepMonthly', '#deleteExtraneous'].forEach((sel) => $(sel).addEventListener('input', renderSummary));
  $('#addSourceBtn').addEventListener('click', () => openPicker('', (p) => { if (!wiz.sources.includes(p)) wiz.sources.push(p); renderSources(); }));
  $('#sourceList').addEventListener('click', (ev) => { const b = ev.target.closest('button[data-remove]'); if (b) { wiz.sources.splice(Number(b.dataset.remove), 1); renderSources(); } });
  $('#volumeList').addEventListener('click', (ev) => { const b = ev.target.closest('.volume'); if (b) chooseVolume(b); });
  $('#targetPath').addEventListener('input', () => { markVolume(); renderSummary(); });
  $('#targetHost').addEventListener('input', markVolume);
  $('#discoverBtn').addEventListener('click', () => discoverHosts($('#discoverList'), $('#discoverBtn'), (h) => { $('#targetHost').value = h; markVolume(); $('#targetUser').focus(); }));
  $('#pickTargetBtn').addEventListener('click', () => openPicker($('#targetPath').value, (p) => { $('#targetPath').value = p; markVolume(); }));
  $('#copyKeyBtn').addEventListener('click', async () => {
    try { await navigator.clipboard.writeText($('#sshKeyText').textContent); $('#copyKeyBtn').textContent = t('field.copied'); setTimeout(() => { $('#copyKeyBtn').textContent = t('field.copy'); }, 1500); } catch { /* clipboard blocked on http origins */ }
  });

  // generated passphrase
  $('#passBackBtn').addEventListener('click', () => { $('#passModal').hidden = true; wiz.generated = ''; });
  $('#passSavedCheck').addEventListener('change', (ev) => { $('#passOkBtn').disabled = !ev.target.checked; });
  $('#passCopyBtn').addEventListener('click', async () => {
    try { await navigator.clipboard.writeText(wiz.generated); $('#passCopyBtn').textContent = t('field.copied'); setTimeout(() => { $('#passCopyBtn').textContent = t('field.copy'); }, 1500); } catch { /* clipboard blocked on http origins */ }
  });
  $('#passPrintBtn').addEventListener('click', () => printPassphrase(readWizard().name, wiz.generated));
  $('#passOkBtn').addEventListener('click', () => { const job = readWizard(); job.passphrase = wiz.generated; submitJob(job); });

  // network share through Files
  $('#connectLanBtn').addEventListener('click', openConnect);
  $('#connectCancelBtn').addEventListener('click', () => { $('#connectModal').hidden = true; });
  $('#connectOkBtn').addEventListener('click', connectShare);
  $('#connectGuest').addEventListener('change', (ev) => { $('#connectCredentials').hidden = ev.target.checked; });
  $('#connectDiscoverBtn').addEventListener('click', () => discoverHosts($('#connectDiscoverList'), $('#connectDiscoverBtn'), (h) => { $('#connectHost').value = h; $('#connectUser').focus(); }));

  $('#pickerCancelBtn').addEventListener('click', () => { $('#pickerModal').hidden = true; });
  $('#pickerOkBtn').addEventListener('click', () => { $('#pickerModal').hidden = true; if (picker.onChoose && picker.path) picker.onChoose(picker.path); });
  $('#pickerList').addEventListener('click', (ev) => { const row = ev.target.closest('.row'); if (row) browsePicker(row.dataset.path); });

  $('#historyCloseBtn').addEventListener('click', () => { $('#historyModal').hidden = true; });
  $('#historyClearBtn').addEventListener('click', clearHistory);
  $('#outputCloseBtn').addEventListener('click', () => { $('#outputModal').hidden = true; clearTimeout(out.timer); });

  $('#snapshotSelect').addEventListener('change', (ev) => { rst.snapshot = ev.target.value; rst.selected = new Map(); renderSelection(); browseSnapshot(''); });
  $('#restoreTree').addEventListener('click', onTreeClick);
  $('#restoreSelection').addEventListener('click', (ev) => { const b = ev.target.closest('button[data-unselect]'); if (b) { rst.selected.delete(b.dataset.unselect); renderSelection(); browseSnapshot(rst.path); } });
  $('#pickRestoreBtn').addEventListener('click', () => openPicker($('#restoreTarget').value, (p) => { $('#restoreTarget').value = p; }));
  $('#restoreCancelBtn').addEventListener('click', () => { $('#restoreModal').hidden = true; });
  $('#restoreOkBtn').addEventListener('click', startRestore);
  $('#checkBtn').addEventListener('click', startCheck);

  $('#previewCloseBtn').addEventListener('click', () => { $('#previewModal').hidden = true; });
  $('#previewRunBtn').addEventListener('click', runFromPreview);
  $('#confirmCancelBtn').addEventListener('click', () => { $('#confirmModal').hidden = true; });

  $$('.modal-overlay').forEach((ov) => ov.addEventListener('click', (ev) => { if (ev.target === ov && ov.id !== 'jobModal' && ov.id !== 'passModal') ov.hidden = true; }));
  document.addEventListener('keydown', (ev) => { if (ev.key === 'Escape') $$('.modal-overlay').forEach((ov) => { if (ov.id !== 'passModal') ov.hidden = true; }); });

  loadHealth();
  loadJobs();
}

document.addEventListener('DOMContentLoaded', init);
