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
  const s = job.schedule || {};
  if (s.type === 'cron') return `<code>${esc(s.cron_expr)}</code>`;
  if (s.type === 'interval') {
    const m = s.interval_min;
    if (m % 1440 === 0) return m === 1440 ? t('schedule.everyDay') : t('schedule.everyDays', { n: m / 1440 });
    if (m % 60 === 0) return m === 60 ? t('schedule.everyHour') : t('schedule.everyHours', { n: m / 60 });
    return t('schedule.everyMin', { n: m });
  }
  return t('schedule.manual');
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
  if (!job.bytes_done && !job.rate) return '';
  const parts = [];
  if (job.rate) parts.push(`${fmtBytes(job.rate)}/s`);
  if (job.bytes_done) parts.push(job.bytes_total ? `${fmtBytes(job.bytes_done)} / ${fmtBytes(job.bytes_total)}` : fmtBytes(job.bytes_done));
  if (job.rate && job.bytes_total > job.bytes_done) parts.push(t('jobs.left', { time: fmtDuration(((job.bytes_total - job.bytes_done) / job.rate) * 1000) }));
  return ` · ${parts.join(' · ')}`;
}

function resultPill(job) {
  if (job.running) return `<span class="pill accent"><span class="dot pulse"></span>${t('code.running')}</span>`;
  const r = job.last_result;
  if (!r) return `<span class="pill">${t('code.never')}</span>`;
  const label = LANGS.en[`code.${r.code}`] ? t(`code.${r.code}`) : r.code;
  const cls = r.success ? 'ok' : (r.code === 'partial' || r.code === 'skipped_running' ? 'warn' : 'bad');
  return `<span class="pill ${cls}">${label}</span>`;
}

function renderJobs() {
  const list = $('#jobList');
  if (!state.jobs.length) {
    list.innerHTML = `<div class="empty">${t('jobs.empty')}</div>`;
    return;
  }
  list.innerHTML = state.jobs.map((job) => {
    const r = job.last_result;
    const kindPill = `<span class="pill ${job.kind === 'backup' ? 'accent' : ''}">${t(`kind.${job.kind}`)}</span>`;
    const statePill = job.enabled ? '' : `<span class="pill warn">${t('jobs.disabled')}</span>`;
    const actions = [
      job.running
        ? `<button class="sm" data-act="cancel">${t('act.cancel')}</button>`
        : `<button class="sm primary" data-act="run">${t('act.run')}</button>`,
      job.kind === 'sync' ? `<button class="sm" data-act="preview">${t('act.preview')}</button>` : '',
      job.kind === 'backup' ? `<button class="sm" data-act="restore">${t('act.restore')}</button>` : '',
      `<button class="sm" data-act="history">${t('act.history')}</button>`,
      `<button class="sm" data-act="output">${t('act.output')}</button>`,
      `<button class="sm" data-act="edit">${t('act.edit')}</button>`,
      `<button class="sm ghost" data-act="toggle">${job.enabled ? t('act.disable') : t('act.enable')}</button>`,
      `<button class="sm ghost" data-act="delete">${t('act.delete')}</button>`,
    ].join('');
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
          <div class="head"><strong>${esc(job.name)}</strong>${kindPill}${statePill}${resultPill(job)}</div>
          <div class="meta">
            <span class="k">${t('jobs.sources')}</span><span>${job.sources.map((s) => `<code>${esc(s)}</code>`).join(' · ')}</span>
            <span class="k">${t('jobs.target')}</span><span>${targetLabel(job.target)}</span>
            <span class="k">${t('jobs.schedule')}</span><span>${scheduleLabel(job)}${job.enabled && job.next_run_at ? ` · <span class="muted">${t('jobs.next')} ${fmtRel(job.next_run_at)}</span>` : ''}</span>
          </div>
        </div>
        <div class="actions">${actions}</div>
        ${progress}
        ${lastLine ? `<div class="result">${lastLine}</div>` : ''}
      </article>`;
  }).join('');
}

async function onJobAction(ev) {
  const btn = ev.target.closest('button[data-act]');
  if (!btn) return;
  const card = btn.closest('.job-card');
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

/* ---------- wizard ---------- */

const wiz = { step: 1, editing: null, sources: [], remote: '' };

function openWizard(job) {
  wiz.editing = job || null;
  wiz.sources = job ? [...job.sources] : [];
  $('#jobModalTitle').textContent = job ? t('wizard.editTitle', { name: job.name }) : t('wizard.newTitle');
  $('#jobNotice').hidden = true;
  fillWizard(job);
  showStep(1);
  loadSSHKey();
  loadVolumes();
  $('#jobModal').hidden = false;
  $('#nameInput').focus();
}

function fillWizard(job) {
  const j = job || {
    kind: 'backup', name: '', excludes: [], target: { type: 'local', path: '' },
    schedule: { type: 'cron', cron_expr: '0 3 * * *' }, retention: { keep_last: 7, keep_daily: 7, keep_weekly: 4, keep_monthly: 6 },
    delete_extraneous: false, enabled: true, timeout_min: 0, notifications: [],
  };
  $(`input[name="kind"][value="${j.kind}"]`).checked = true;
  $$('input[name="kind"]').forEach((r) => { r.disabled = !!job; }); // the kind of an existing job is fixed
  $('#nameInput').value = j.name;
  $('#excludesInput').value = (j.excludes || []).join('\n');
  renderSources();

  const tg = j.target;
  $('#targetType').value = tg.type;
  $('#targetPath').value = tg.type === 'local' ? tg.path : '';
  $('#cloudPath').value = tg.type === 'cloud' ? tg.path : '';
  wiz.remote = tg.type === 'cloud' ? tg.remote : '';
  $('#targetRemotePath').value = tg.type === 'local' ? '' : (tg.path || '');
  $('#targetHost').value = tg.host || '';
  $('#targetPort').value = tg.port || '';
  $('#targetUser').value = tg.user || '';
  $('#targetSecret').value = '';
  $('#targetSecret').placeholder = job && tg.type !== 'local' && tg.type !== 'ssh' ? t('field.unchanged') : '';
  $('#targetShare').value = tg.share || '';
  $('#targetBucket').value = tg.bucket || '';
  $('#targetRegion').value = tg.region || '';
  $('#targetInsecure').checked = !!tg.insecure;
  $('#passphraseInput').value = '';
  $('#passphraseConfirm').value = '';
  $('#passphraseInput').placeholder = job ? t('field.unchanged') : '';
  $('#passphraseConfirmField').hidden = !!job;
  $('#deleteExtraneous').checked = !!j.delete_extraneous;

  const s = j.schedule || { type: 'manual' };
  $('#scheduleType').value = s.type;
  $('#intervalHours').value = s.type === 'interval' ? Math.max(1, Math.round(s.interval_min / 60)) : 24;
  $('#cronInput').value = s.type === 'cron' ? s.cron_expr : '0 3 * * *';
  $('#cronPreset').value = '';
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
  validateCron();
}

function currentKind() { return $('input[name="kind"]:checked').value; }

function updateKindFields() {
  const kind = currentKind();
  $$('.kind-fields').forEach((el) => { el.hidden = el.dataset.kind !== kind; });
  markVolume();
}

function updateTargetFields() {
  const type = $('#targetType').value;
  $$('.target-fields').forEach((el) => { el.hidden = !el.dataset.for.split(' ').includes(type); });
  $('#discoverList').hidden = true;
  if (volumes.list.length) markVolume();
  $('#targetSecretField').hidden = type === 'ssh';
  $('#targetHostLabel').textContent = type === 's3' ? t('field.endpoint') : t('field.host');
  $('#targetUserLabel').textContent = type === 's3' ? t('field.accessKey') : t('field.user');
  $('#targetSecretLabel').textContent = type === 's3' ? t('field.secretKey') : t('field.password');
  $('#targetRemotePathLabel').textContent = type === 's3' ? t('field.prefix') : (type === 'smb' ? t('field.shareFolder') : t('field.remotePath'));
  $('#sftpPasswordHint').hidden = type !== 'sftp';
}

function updateScheduleFields() {
  const type = $('#scheduleType').value;
  $$('[data-schedule]').forEach((el) => { el.hidden = el.dataset.schedule !== type; });
}

function renderSources() {
  const list = $('#sourceList');
  list.innerHTML = wiz.sources.map((s, i) => `<span class="chip"><code title="${esc(s)}">${esc(s)}</code><button type="button" data-remove="${i}" aria-label="remove">&times;</button></span>`).join('');
  if (!wiz.sources.length) list.innerHTML = `<span class="muted">${t('field.noSources')}</span>`;
}

async function loadSSHKey() {
  try {
    const k = await api('/sshkey');
    $('#sshKeyText').textContent = k.public_key;
  } catch (err) {
    $('#sshKeyText').textContent = describeError(err);
  }
}

function showStep(n) {
  wiz.step = n;
  $$('#wizardSteps li').forEach((li) => {
    const s = Number(li.dataset.step);
    li.className = s === n ? 'active' : (s < n ? 'done' : '');
  });
  $$('.step').forEach((sec) => { sec.hidden = Number(sec.dataset.step) !== n; });
  $('#wizardBackBtn').hidden = n === 1;
  $('#wizardNextBtn').hidden = n === 3;
  $('#jobSaveBtn').hidden = n !== 3;
  $('#jobNotice').hidden = true;
}

// Client-side checks only catch what the user can fix before leaving the
// step; the server remains the authority and its codes map to error.* keys.
function checkStep(n) {
  if (n === 1) {
    if (!$('#nameInput').value.trim()) return t('error.name_required');
    if (!wiz.sources.length) return t('error.sources_required');
  }
  if (n === 2) {
    const type = $('#targetType').value;
    if (type === 'local' && !$('#targetPath').value.trim()) return t('error.target_path_invalid');
    if (type === 'cloud' && (!$('#cloudRemote').value || !$('#cloudPath').value.trim())) return t('error.target_incomplete');
    if (type !== 'local' && type !== 'cloud' && (!$('#targetHost').value.trim() || !$('#targetUser').value.trim())) return t('error.target_incomplete');
    if ((type === 'ssh' || type === 'sftp') && !$('#targetRemotePath').value.trim()) return t('error.target_incomplete');
    if (type === 'smb' && !$('#targetShare').value.trim()) return t('error.target_incomplete');
    if (type === 's3' && !$('#targetBucket').value.trim()) return t('error.target_incomplete');
    if (currentKind() === 'backup' && !wiz.editing) {
      const p = $('#passphraseInput').value;
      if (!p) return t('error.passphrase_required');
      if (p !== $('#passphraseConfirm').value) return t('error.passphrase_mismatch');
    }
  }
  return '';
}

function wizardNext() {
  const problem = checkStep(wiz.step);
  if (problem) { showJobNotice(problem); return; }
  showStep(wiz.step + 1);
}

function showJobNotice(text) {
  const n = $('#jobNotice');
  n.textContent = text;
  n.hidden = false;
}

function readWizard() {
  const type = $('#targetType').value;
  const target = { type };
  if (type === 'local') target.path = $('#targetPath').value.trim();
  else if (type === 'cloud') {
    target.remote = $('#cloudRemote').value;
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
  const st = $('#scheduleType').value;
  const schedule = { type: st };
  if (st === 'interval') schedule.interval_min = Math.max(1, Number($('#intervalHours').value)) * 60;
  if (st === 'cron') schedule.cron_expr = $('#cronInput').value.trim();
  const notifications = [];
  const onS = $('#notifyOnSuccess').checked;
  const onF = $('#notifyOnFailure').checked;
  if ($('#notifyTelegram').checked) notifications.push({ enabled: true, type: 'telegram', target: '', on_success: onS, on_failure: onF });
  if ($('#webhookUrl').value.trim()) notifications.push({ enabled: true, type: 'webhook', target: $('#webhookUrl').value.trim(), webhook_format: $('#webhookFormat').value, on_success: onS, on_failure: onF });
  const job = {
    name: $('#nameInput').value.trim(),
    kind: currentKind(),
    sources: [...wiz.sources],
    excludes: $('#excludesInput').value.split('\n').map((s) => s.trim()).filter(Boolean),
    target, schedule, notifications,
    enabled: $('#enabledInput').checked,
    timeout_min: Number($('#timeoutInput').value) || 0,
  };
  if (job.kind === 'backup') {
    job.retention = { keep_last: Number($('#keepLast').value) || 0, keep_daily: Number($('#keepDaily').value) || 0, keep_weekly: Number($('#keepWeekly').value) || 0, keep_monthly: Number($('#keepMonthly').value) || 0 };
    if ($('#passphraseInput').value) job.passphrase = $('#passphraseInput').value;
  } else {
    job.delete_extraneous = $('#deleteExtraneous').checked;
  }
  return job;
}

async function saveJob() {
  for (const n of [1, 2]) {
    const problem = checkStep(n);
    if (problem) { showStep(n); showJobNotice(problem); return; }
  }
  const job = readWizard();
  const btn = $('#jobSaveBtn');
  btn.disabled = true;
  try {
    const saved = wiz.editing
      ? await api(`/jobs/${wiz.editing.id}`, { method: 'PUT', body: job })
      : await api('/jobs', { method: 'POST', body: job });
    $('#jobModal').hidden = true;
    const isNew = !wiz.editing;
    await loadJobs();
    // a fresh sync job gets its preview before anything is copied
    if (isNew && saved.kind === 'sync') openPreview(saved);
  } catch (err) {
    showJobNotice(describeError(err));
  } finally {
    btn.disabled = false;
  }
}

let cronTimer = null;
function validateCron() {
  clearTimeout(cronTimer);
  const fb = $('#cronFeedback');
  const expr = $('#cronInput').value.trim();
  if ($('#scheduleType').value !== 'cron' || !expr) { fb.textContent = ''; return; }
  cronTimer = setTimeout(async () => {
    try {
      const v = await api('/schedule/validate', { method: 'POST', body: { type: 'cron', cron_expr: expr } });
      if (!v.valid) {
        fb.className = 'cron-feedback bad';
        fb.textContent = v.errors.map((e) => e.message).join(' · ') || t('error.cron_invalid');
        return;
      }
      fb.className = 'cron-feedback ok';
      fb.innerHTML = `${t('cron.valid')} <span class="next">${t('cron.next')}: ${v.next_runs.slice(0, 3).map(fmtTime).join(' · ')}</span>`;
    } catch (err) {
      fb.className = 'cron-feedback bad';
      fb.textContent = describeError(err);
    }
  }, 300);
}

/* ---------- drives ---------- */

const volumes = { list: [] };

// GET /api/mounts: system disk, pools, USB disks, cloud drives — one click
// puts "<drive>/Backups" into the target folder.
async function loadVolumes() {
  const el = $('#volumeList');
  el.innerHTML = `<span class="muted">${t('common.loading')}</span>`;
  try {
    const res = await api('/mounts');
    volumes.list = res.volumes;
    $('#cloudRemote').innerHTML = (res.remotes || []).map((r) => `<option value="${esc(r.remote)}">${esc(r.name)}</option>`).join('');
    if (wiz.remote) $('#cloudRemote').value = wiz.remote;
    if (!volumes.list.length) { el.innerHTML = `<span class="muted">${t('volumes.none')}</span>`; return; }
    el.innerHTML = volumes.list.map((v) => `
      <button type="button" class="volume" data-path="${esc(v.path)}" data-kind="${v.kind}" data-remote="${esc(v.remote || '')}">
        <span class="name" title="${esc(v.path)}">${esc(v.name)}</span>
        <span class="sub"><span class="pill ${v.kind === 'cloud' ? 'warn' : (v.kind === 'system' ? 'accent' : '')}">${t(`vol.${v.kind}`)}</span>${v.size ? esc(t('volumes.free', { free: fmtBytes(v.free), size: fmtBytes(v.size) })) : ''}</span>
      </button>`).join('');
    markVolume();
  } catch (err) {
    el.innerHTML = `<span class="muted">${esc(describeError(err))}</span>`;
  }
}

// markVolume highlights the drive the current path lies on and shows the
// cloud warning for backups.
function markVolume() {
  const type = $('#targetType').value;
  const path = $('#targetPath').value.trim();
  const remote = $('#cloudRemote').value;
  $$('#volumeList .volume').forEach((b) => {
    const on = type === 'cloud'
      ? b.dataset.kind === 'cloud' && b.dataset.remote === remote
      : type === 'local' && b.dataset.kind !== 'cloud' && (path === b.dataset.path || path.startsWith(b.dataset.path + '/'));
    b.classList.toggle('selected', on);
  });
}

/* ---------- network discovery ---------- */

// GET /api/discover listens for a couple of seconds; the list fills in
// once, a click copies the host into the field.
async function discoverHosts() {
  const list = $('#discoverList');
  const btn = $('#discoverBtn');
  list.hidden = false;
  list.innerHTML = `<div class="empty muted">${t('discover.searching')}</div>`;
  btn.disabled = true;
  try {
    const res = await api('/discover');
    if (!res.hosts.length) {
      list.innerHTML = `<div class="empty">${t('discover.none')}</div>`;
      return;
    }
    list.innerHTML = res.hosts.map((h) => `
      <div class="row" data-host="${esc(h.host)}">
        <span class="icon">${h.os === 'ZimaOS' ? '&#9673;' : '&#9675;'}</span>
        <span>${esc(h.name)}</span>
        <span class="pill ${h.os === 'ZimaOS' ? 'accent' : ''}">${esc(h.os || '?')}</span>
        <span class="pill">${t(`net.${h.network}`)}</span>
        <span class="addr size">${esc(h.host)}</span>
      </div>`).join('');
  } catch (err) {
    list.innerHTML = `<div class="empty">${esc(describeError(err))}</div>`;
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
  $('#wizardNextBtn').addEventListener('click', wizardNext);
  $('#wizardBackBtn').addEventListener('click', () => showStep(wiz.step - 1));
  $('#jobSaveBtn').addEventListener('click', saveJob);
  $$('input[name="kind"]').forEach((r) => r.addEventListener('change', updateKindFields));
  $('#targetType').addEventListener('change', updateTargetFields);
  $('#scheduleType').addEventListener('change', () => { updateScheduleFields(); validateCron(); });
  $('#cronInput').addEventListener('input', validateCron);
  $('#cronPreset').addEventListener('change', (ev) => { if (ev.target.value) { $('#cronInput').value = ev.target.value; validateCron(); } });
  $('#addSourceBtn').addEventListener('click', () => openPicker('', (p) => { if (!wiz.sources.includes(p)) wiz.sources.push(p); renderSources(); }));
  $('#sourceList').addEventListener('click', (ev) => { const b = ev.target.closest('button[data-remove]'); if (b) { wiz.sources.splice(Number(b.dataset.remove), 1); renderSources(); } });
  $('#volumeList').addEventListener('click', (ev) => {
    const b = ev.target.closest('.volume');
    if (!b) return;
    if (b.dataset.kind === 'cloud') {
      $('#targetType').value = 'cloud';
      updateTargetFields();
      $('#cloudRemote').value = b.dataset.remote;
      if (!$('#cloudPath').value.trim()) $('#cloudPath').value = 'Backups';
      markVolume();
      $('#cloudPath').focus();
      return;
    }
    $('#targetType').value = 'local';
    updateTargetFields();
    $('#targetPath').value = `${b.dataset.path}/Backups`;
    markVolume();
    $('#targetPath').focus();
  });
  $('#cloudRemote').addEventListener('change', markVolume);
  $('#targetPath').addEventListener('input', markVolume);
  $('#discoverBtn').addEventListener('click', discoverHosts);
  $('#discoverList').addEventListener('click', (ev) => {
    const row = ev.target.closest('.row[data-host]');
    if (!row) return;
    $('#targetHost').value = row.dataset.host;
    $('#discoverList').hidden = true;
    $('#targetUser').focus();
  });
  $('#pickTargetBtn').addEventListener('click', () => openPicker($('#targetPath').value, (p) => { $('#targetPath').value = p; }));
  $('#copyKeyBtn').addEventListener('click', async () => {
    try { await navigator.clipboard.writeText($('#sshKeyText').textContent); $('#copyKeyBtn').textContent = t('field.copied'); setTimeout(() => { $('#copyKeyBtn').textContent = t('field.copy'); }, 1500); } catch { /* clipboard blocked on http origins */ }
  });

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

  $$('.modal-overlay').forEach((ov) => ov.addEventListener('click', (ev) => { if (ev.target === ov && ov.id !== 'jobModal') ov.hidden = true; }));
  document.addEventListener('keydown', (ev) => { if (ev.key === 'Escape') $$('.modal-overlay').forEach((ov) => { ov.hidden = true; }); });

  loadHealth();
  loadJobs();
}

document.addEventListener('DOMContentLoaded', init);
