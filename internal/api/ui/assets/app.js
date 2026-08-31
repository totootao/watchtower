/*
 * Watchtower web dashboard.
 *
 * The dashboard is a thin client over the existing /v1/* HTTP API. It never
 * talks to Docker directly and never changes update policy: every button maps
 * onto one documented API call.
 */
(function () {
  'use strict';

  var CFG = window.__WATCHTOWER_UI__ || {};
  var EP = CFG.endpoints || {};
  var PATHS = CFG.endpointPaths || {};

  var AUTO_REFRESH_KEY = 'watchtower.ui.autoRefresh';
  var AUTO_INTERVAL_KEY = 'watchtower.ui.autoInterval';
  var CONFIRM_WINDOW_MS = 4000;

  var state = {
    containers: [],
    checks: {},
    busy: {},
    expanded: {},
    selected: {},
    filter: '',
    sortKey: 'name',
    sortAsc: true,
    lastLoaded: null,
    loading: false,
    autoTimer: null,
    armed: {}
  };

  /* ------------------------------------------------------------------ utils */

  function el(id) { return document.getElementById(id); }

  function escapeHTML(value) {
    return String(value == null ? '' : value).replace(/[&<>"']/g, function (ch) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[ch];
    });
  }

  /** Escape a container name so it is safe to use as an anchored regex pattern. */
  function escapeRegExp(value) {
    return String(value).replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  }

  function shortDigest(digest) {
    if (!digest) { return '—'; }
    return digest.length > 20 ? digest.slice(0, 19) + '…' : digest;
  }

  function relativeTime(date) {
    if (!date) { return '—'; }
    var seconds = Math.round((Date.now() - date.getTime()) / 1000);
    if (seconds < 5) { return 'just now'; }
    if (seconds < 60) { return seconds + 's ago'; }
    var minutes = Math.round(seconds / 60);
    if (minutes < 60) { return minutes + 'm ago'; }
    var hours = Math.round(minutes / 60);
    if (hours < 24) { return hours + 'h ago'; }
    return Math.round(hours / 24) + 'd ago';
  }

  function pluralize(count, singular, plural) {
    return count + ' ' + (count === 1 ? singular : (plural || singular + 's'));
  }

  /* ------------------------------------------------------------------- api */

  function ApiError(message, status, body) {
    this.name = 'ApiError';
    this.message = message || 'Request failed';
    this.status = status || 0;
    this.body = body || null;
  }
  ApiError.prototype = Object.create(Error.prototype);

  function sessionExpired() {
    stopAutoRefresh();
    toast('error', 'Session expired', 'Sign in again to continue.');
    setTimeout(function () { window.location.href = (CFG.basePath || '/ui'); }, 1200);
  }

  function apiRequest(path, options) {
    var opts = options || {};
    var url = new URL(path, window.location.origin);

    Object.keys(opts.params || {}).forEach(function (key) {
      var value = opts.params[key];
      if (value === undefined || value === null || value === '') { return; }
      if (Array.isArray(value)) {
        value.forEach(function (item) { url.searchParams.append(key, item); });
      } else {
        url.searchParams.append(key, value);
      }
    });

    return fetch(url.toString(), {
      method: opts.method || 'GET',
      credentials: 'same-origin',
      headers: {
        'Accept': 'application/json',
        'X-Requested-By': 'watchtower-ui'
      }
    }).then(function (res) {
      if (res.status === 401 || res.status === 403) {
        sessionExpired();
        throw new ApiError('Session expired', res.status);
      }

      return res.text().then(function (text) {
        var data = null;
        if (text) {
          try { data = JSON.parse(text); } catch (err) { data = null; }
        }
        if (!res.ok) {
          var message = (data && (data.error || data.message)) || text || ('HTTP ' + res.status);
          throw new ApiError(message, res.status, data);
        }
        return data;
      });
    });
  }

  /* ---------------------------------------------------------------- toasts */

  function toast(kind, title, body, ttl) {
    var host = el('toasts');
    if (!host) { return; }

    var node = document.createElement('div');
    node.className = 'toast toast-' + (kind || 'info');
    node.innerHTML =
      '<svg class="toast-mark" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" ' +
      'stroke-linecap="round" stroke-linejoin="round">' +
      (kind === 'error'
        ? '<circle cx="12" cy="12" r="9"/><path d="M12 8v4M12 16h.01"/>'
        : kind === 'success'
          ? '<circle cx="12" cy="12" r="9"/><path d="m8 12.5 2.5 2.5L16 9.5"/>'
          : '<circle cx="12" cy="12" r="9"/><path d="M12 11v5M12 8h.01"/>') +
      '</svg>' +
      '<div><div class="toast-title">' + escapeHTML(title) + '</div>' +
      (body ? '<div class="toast-body">' + escapeHTML(body) + '</div>' : '') +
      '</div>' +
      '<button type="button" class="toast-close" aria-label="Dismiss">&times;</button>';

    var timer = setTimeout(remove, ttl || (kind === 'error' ? 9000 : 5500));

    function remove() {
      clearTimeout(timer);
      node.classList.add('is-out');
      setTimeout(function () { if (node.parentNode) { node.parentNode.removeChild(node); } }, 200);
    }

    node.querySelector('.toast-close').addEventListener('click', remove);
    host.appendChild(node);
  }

  function alertBox(message) {
    var host = el('alerts');
    if (!host) { return; }
    var node = document.createElement('div');
    node.className = 'alert';
    node.textContent = message;
    host.appendChild(node);
  }

  /* ------------------------------------------------------------------ data */

  function loadContainers(quiet) {
    if (!EP.containers) {
      renderEmpty('The containers endpoint is not enabled. Add "containers" to http-api-endpoints to list containers here.');
      return Promise.resolve();
    }
    if (state.loading) { return Promise.resolve(); }

    state.loading = true;
    setRefreshing(true);

    var requests = [apiRequest(PATHS.containers || '/v1/containers')];
    if (EP.containers && PATHS.details) {
      requests.push(apiRequest(PATHS.details).catch(function () { return null; }));
    }

    return Promise.all(requests)
      .then(function (responses) {
        var statuses = (responses[0] && responses[0].containers) || [];
        var details = (responses[1] && responses[1].containers) || null;
        merge(statuses, details);
        state.lastLoaded = new Date();
        render();
      })
      .catch(function (err) {
        if (!quiet) { toast('error', 'Could not load containers', err.message); }
        renderEmpty(err.message, true);
      })
      .then(function () {
        state.loading = false;
        setRefreshing(false);
      });
  }

  /** Merge container statuses with the richer details payload (details win). */
  function merge(statuses, details) {
    var byName = {};

    statuses.forEach(function (item) {
      byName[item.name] = {
        name: item.name,
        image: item.image,
        imageID: item.image_id,
        digest: item.digest
      };
    });

    (details || []).forEach(function (item) {
      byName[item.name] = {
        name: item.name,
        image: item.image,
        imageID: item.image_id,
        digest: item.digest,
        running: !!item.running,
        watchtower: !!item.watchtower,
        monitorOnly: !!item.monitor_only,
        noPull: !!item.no_pull,
        enabled: item.enabled !== false,
        stale: !!item.stale,
        scope: item.scope || ''
      };
    });

    state.containers = Object.keys(byName).map(function (name) { return byName[name]; });
    state.containers.sort(function (a, b) { return a.name.localeCompare(b.name); });

    // Drop cached check results and selections for containers that went away.
    Object.keys(state.checks).forEach(function (name) {
      if (!byName[name]) { delete state.checks[name]; }
    });
    Object.keys(state.selected).forEach(function (name) {
      if (!byName[name]) { delete state.selected[name]; }
    });
  }

  /* ---------------------------------------------------------------- render */

  function visibleContainers() {
    var needle = state.filter.trim().toLowerCase();
    var rows = state.containers.filter(function (item) {
      if (!needle) { return true; }
      return item.name.toLowerCase().indexOf(needle) !== -1 ||
        String(item.image || '').toLowerCase().indexOf(needle) !== -1;
    });

    rows.sort(function (a, b) {
      var left = state.sortKey === 'image' ? String(a.image || '') : a.name;
      var right = state.sortKey === 'image' ? String(b.image || '') : b.name;
      var cmp = left.localeCompare(right);
      return state.sortAsc ? cmp : -cmp;
    });

    return rows;
  }

  function render() {
    var body = el('container-body');
    if (!body) { return; }

    var rows = visibleContainers();
    if (!rows.length) {
      renderEmpty(state.containers.length
        ? 'No containers match the current filter.'
        : 'No watched containers found. Check the container filter and scope configuration.');
    } else {
      body.innerHTML = rows.map(rowHTML).join('');
      bindRows(body);
    }

    renderStats();
    renderSelection();
  }

  function renderEmpty(message, isError) {
    var body = el('container-body');
    if (!body) { return; }
    body.innerHTML = '<tr class="empty-row"><td colspan="6">' +
      (isError ? '' : '') +
      '<p>' + escapeHTML(message) + '</p>' +
      (EP.containers && !isError ? '' : '') +
      '</td></tr>';
    renderStats();
    renderSelection();
  }

  function rowHTML(item) {
    var check = state.checks[item.name];
    var busy = state.busy[item.name];
    var expanded = !!state.expanded[item.name];
    var selected = !!state.selected[item.name];
    var canUpdate = EP.update && item.enabled !== false;

    var chips = [];
    if (item.enabled === false) {
      chips.push('<span class="chip">Disabled</span>');
    }
    chips.push(item.running === false
      ? '<span class="chip"><span class="dot"></span>Stopped</span>'
      : '<span class="chip chip-green"><span class="dot"></span>Running</span>');
    if (item.stale) { chips.push('<span class="chip chip-amber">Stale</span>'); }
    if (item.watchtower) { chips.push('<span class="chip chip-purple">Watchtower</span>'); }
    if (item.monitorOnly) { chips.push('<span class="chip chip-blue">Monitor only</span>'); }
    if (item.noPull) { chips.push('<span class="chip chip-quiet">No pull</span>'); }

    var updateChip;
    if (busy === 'check') {
      updateChip = '<span class="chip chip-blue">Checking…</span>';
    } else if (busy === 'update') {
      updateChip = '<span class="chip chip-blue">Updating…</span>';
    } else if (!check) {
      updateChip = '<span class="chip chip-quiet">Not checked</span>';
    } else if (check.error) {
      updateChip = '<span class="chip chip-red" title="' + escapeHTML(check.error) + '">Check failed</span>';
    } else if (check.update_available) {
      updateChip = '<span class="chip chip-amber"><span class="dot"></span>Update available</span>';
    } else {
      updateChip = '<span class="chip chip-green">Up to date</span>';
    }

    var armed = state.armed[item.name];
    var updateLabel = armed ? 'Confirm?' : 'Update';

    return '' +
      '<tr class="' + (busy ? 'row-busy ' : '') + (selected ? 'row-selected' : '') + '" data-name="' + escapeHTML(item.name) + '">' +
        '<td class="col-check"><input type="checkbox" class="row-select" data-name="' + escapeHTML(item.name) + '"' +
          (selected ? ' checked' : '') + ' aria-label="Select ' + escapeHTML(item.name) + '"></td>' +
        '<td class="name-cell">' +
          '<div class="name-main">' +
            '<button type="button" class="name-toggle" data-action="toggle" title="Show image details">' +
              (expanded ? '▾' : '▸') +
            '</button>' +
            '<span class="name-text">' + escapeHTML(item.name) + '</span>' +
          '</div>' +
          '<div class="name-sub">' + escapeHTML(shortDigest(item.imageID)) + '</div>' +
        '</td>' +
        '<td class="img-cell"><span class="img-text" title="' + escapeHTML(item.image) + '">' + escapeHTML(item.image) + '</span></td>' +
        '<td><div class="chips">' + chips.join('') + '</div></td>' +
        '<td>' + updateChip + '</td>' +
        '<td class="col-actions"><div class="actions">' +
          '<button type="button" class="btn btn-sm" data-action="check"' +
            (EP.check && !busy ? '' : ' disabled') + '>Check</button>' +
          '<button type="button" class="btn btn-sm' + (armed ? ' btn-danger-armed' : '') + '" data-action="update"' +
            (canUpdate && !busy ? '' : ' disabled') + '>' + updateLabel + '</button>' +
        '</div></td>' +
      '</tr>' +
      (expanded ? detailHTML(item, check) : '');
  }

  function detailHTML(item, check) {
    var fields = [
      ['Image', item.image],
      ['Image ID', item.imageID || '—'],
      ['Digest', item.digest || '—'],
      ['Scope', item.scope || 'default']
    ];

    if (check) {
      fields.push(['Latest image ID', check.latest_image_id || '—']);
      fields.push(['Latest digest', check.latest_digest || '—']);
      fields.push(['Checked', relativeTime(new Date(check.timestamp))]);
      if (check.error) { fields.push(['Check error', check.error]); }
    }

    return '<tr class="detail-row"><td colspan="6"><div class="detail-grid">' +
      fields.map(function (pair) {
        var value = pair[1] || '—';
        var muted = value === '—' ? ' muted' : '';
        return '<div class="detail-item"><span class="detail-key">' + escapeHTML(pair[0]) +
          '</span><span class="detail-val' + muted + '">' + escapeHTML(value) + '</span></div>';
      }).join('') +
      '</div></td></tr>';
  }

  function renderStats() {
    var total = state.containers.length;
    var running = state.containers.filter(function (c) { return c.running !== false; }).length;
    var updates = Object.keys(state.checks).filter(function (name) {
      var check = state.checks[name];
      return check && check.update_available;
    }).length;

    el('stat-total').textContent = total;
    el('stat-running').textContent = total ? running : '–';
    el('stat-updates').textContent = updates;
    el('stat-last').textContent = relativeTime(state.lastLoaded);
    el('stats').querySelector('.stat-accent').classList.toggle('has-updates', updates > 0);
  }

  function renderSelection() {
    var selected = selectedNames();
    var count = selected.length;
    var counter = el('selection-count');

    counter.hidden = count === 0;
    counter.textContent = count ? count + ' selected' : '';

    el('btn-check-selected').disabled = count === 0 || !EP.check;
    el('btn-update-selected').disabled = count === 0 || !EP.update;

    var all = el('select-all');
    var rows = visibleContainers();
    all.checked = rows.length > 0 && rows.every(function (c) { return state.selected[c.name]; });
    all.indeterminate = !all.checked && rows.some(function (c) { return state.selected[c.name]; });
  }

  function selectedNames() {
    return Object.keys(state.selected).filter(function (name) { return state.selected[name]; });
  }

  function setBusy(names, kind) {
    names.forEach(function (name) { state.busy[name] = kind; });
    render();
  }

  function clearBusy(names) {
    names.forEach(function (name) { delete state.busy[name]; });
    render();
  }

  function setRefreshing(on) {
    var button = el('btn-refresh');
    if (!button) { return; }
    button.disabled = !!on;
    var icon = button.querySelector('.icon');
    if (icon) { icon.classList.toggle('spin', !!on); }
  }

  /* ---------------------------------------------------------------- events */

  function bindRows(body) {
    Array.prototype.forEach.call(body.querySelectorAll('tr[data-name]'), function (row) {
      var name = row.getAttribute('data-name');

      Array.prototype.forEach.call(row.querySelectorAll('[data-action]'), function (button) {
        button.addEventListener('click', function () {
          var action = button.getAttribute('data-action');
          if (action === 'toggle') {
            state.expanded[name] = !state.expanded[name];
            render();
          } else if (action === 'check') {
            checkOne(name);
          } else if (action === 'update') {
            armOrUpdate(name);
          }
        });
      });

      var checkbox = row.querySelector('.row-select');
      if (checkbox) {
        checkbox.addEventListener('change', function () {
          state.selected[name] = checkbox.checked;
          row.classList.toggle('row-selected', checkbox.checked);
          renderSelection();
        });
      }
    });
  }

  /* ---------------------------------------------------------------- actions */

  function checkOne(name) {
    if (!EP.check) { return; }
    setBusy([name], 'check');

    apiRequest(PATHS.check || '/v1/check', { method: 'POST', params: { container: escapeRegExp(name) } })
      .then(function (data) {
        var results = (data && data.containers) || [];
        if (!results.length) {
          toast('info', 'Nothing to check', 'No watched container matches "' + name + '".');
          return;
        }
        results.forEach(function (result) { state.checks[result.name] = result; });

        var available = results.filter(function (r) { return r.update_available; });
        var failed = results.filter(function (r) { return r.error; });

        if (available.length) {
          toast('info', 'Update available', results.map(function (r) { return r.name; }).join(', '));
        } else if (failed.length) {
          toast('error', 'Check failed', failed.map(function (r) { return r.name + ': ' + r.error; }).join('; '));
        } else {
          toast('success', 'Up to date', results.map(function (r) { return r.name; }).join(', '));
        }
      })
      .catch(function (err) { toast('error', 'Check failed', err.message); })
      .then(function () { clearBusy([name]); });
  }

  function checkMany(names) {
    if (!EP.check || !names.length) { return; }
    setBusy(names, 'check');

    apiRequest(PATHS.check || '/v1/check', { method: 'POST', params: { container: names.map(escapeRegExp) } })
      .then(function (data) {
        var results = (data && data.containers) || [];
        results.forEach(function (result) { state.checks[result.name] = result; });

        var available = results.filter(function (r) { return r.update_available; }).length;
        var failed = results.filter(function (r) { return r.error; }).length;

        toast(failed ? 'error' : available ? 'info' : 'success', 'Check finished',
          pluralize(available, 'update') + ' available' + (failed ? ', ' + pluralize(failed, 'failure') : '') +
          ' across ' + pluralize(results.length, 'container') + '.');
      })
      .catch(function (err) { toast('error', 'Check failed', err.message); })
      .then(function () { clearBusy(names); });
  }

  function checkAll() {
    if (!EP.check) { return; }
    setBusy(state.containers.map(function (c) { return c.name; }), 'check');

    apiRequest(PATHS.check || '/v1/check', { method: 'POST' })
      .then(function (data) {
        var results = (data && data.containers) || [];
        results.forEach(function (result) { state.checks[result.name] = result; });

        var available = results.filter(function (r) { return r.update_available; }).length;
        var failed = results.filter(function (r) { return r.error; }).length;

        toast(failed ? 'error' : available ? 'info' : 'success', 'Check finished',
          pluralize(available, 'update') + ' available' + (failed ? ', ' + pluralize(failed, 'failure') : '') +
          ' across ' + pluralize(results.length, 'container') + '.');
      })
      .catch(function (err) { toast('error', 'Check failed', err.message); })
      .then(function () { clearBusy(Object.keys(state.busy)); });
  }

  /** Updates need a second click so a misclick cannot restart a container. */
  function armOrUpdate(name) {
    if (!EP.update) { return; }

    if (state.armed[name]) {
      delete state.armed[name];
      updateOne(name);
      return;
    }

    state.armed[name] = Date.now();
    render();

    setTimeout(function () {
      if (!state.armed[name]) { return; }
      if (Date.now() - state.armed[name] < CONFIRM_WINDOW_MS) { return; }
      delete state.armed[name];
      render();
    }, CONFIRM_WINDOW_MS + 50);
  }

  function updateOne(name) {
    setBusy([name], 'update');

    apiRequest(PATHS.update || '/v1/update', { method: 'POST', params: { container: escapeRegExp(name) } })
      .then(function (data) {
        var summary = (data && data.summary) || {};
        var updated = summary.updated || 0;
        var failed = summary.failed || 0;

        toast(failed ? 'error' : 'success', 'Update finished',
          pluralize(updated, 'container') + ' updated, ' + pluralize(summary.scanned || 0, 'container') + ' scanned' +
          (failed ? ', ' + pluralize(failed, 'failure') : '') + '.');

        delete state.checks[name];
        return loadContainers(true);
      })
      .catch(function (err) {
        toast('error', 'Update failed', err.status === 429
          ? 'Another update is already running — try again shortly.'
          : err.message);
      })
      .then(function () { clearBusy([name]); });
  }

  function updateMany(names) {
    if (!names.length) { return; }
    setBusy(names, 'update');

    // Targeted updates queue behind the update lock, so run them in the
    // background and let the operator poll instead of holding the request open.
    apiRequest(PATHS.update || '/v1/update', {
      method: 'POST',
      params: { container: names.map(escapeRegExp), async: 'true' }
    })
      .then(function () {
        toast('success', 'Update started',
          pluralize(names.length, 'container') + ' queued. Refresh to see the result.');
        setTimeout(function () { loadContainers(true); }, 4000);
      })
      .catch(function (err) {
        toast('error', 'Update failed', err.status === 429
          ? 'Another update is already running — try again shortly.'
          : err.message);
      })
      .then(function () { clearBusy(names); });
  }

  function updateAll() {
    if (!EP.update) { return; }
    setBusy(state.containers.map(function (c) { return c.name; }), 'update');

    apiRequest(PATHS.update || '/v1/update', { method: 'POST', params: { async: 'true' } })
      .then(function () {
        toast('success', 'Update started', 'A full scan is running in the background.');
        setTimeout(function () { loadContainers(true); }, 4000);
      })
      .catch(function (err) {
        toast('error', 'Update failed', err.status === 429
          ? 'Another update is already running — try again shortly.'
          : err.message);
      })
      .then(function () { clearBusy(Object.keys(state.busy)); });
  }

  /* ------------------------------------------------------------ auto refresh */

  function startAutoRefresh() {
    stopAutoRefresh();
    var interval = parseInt(el('auto-refresh-interval').value, 10) || 30000;
    state.autoTimer = setInterval(function () { loadContainers(true); }, interval);
  }

  function stopAutoRefresh() {
    if (state.autoTimer) {
      clearInterval(state.autoTimer);
      state.autoTimer = null;
    }
  }

  /* -------------------------------------------------------------------- init */

  function init() {
    if (CFG.version) {
      var version = el('meta-version');
      if (version) { version.textContent = 'v' + CFG.version; }
    }
    if (CFG.scope) {
      var scope = el('meta-scope');
      if (scope) {
        scope.textContent = 'scope: ' + CFG.scope;
        scope.classList.remove('hidden');
      }
    }

    if (!EP.containers) {
      alertBox('The containers endpoint is disabled, so the dashboard cannot list containers. ' +
        'Add "containers" to http-api-endpoints.');
    } else if (!EP.check && !EP.update) {
      alertBox('Neither the check nor the update endpoint is enabled, so this dashboard is read-only. ' +
        'Add "check" and "update" to http-api-endpoints to enable the actions.');
    }

    var checkSelected = el('btn-check-selected');
    var updateSelected = el('btn-update-selected');
    var checkAllBtn = el('btn-check-all');
    var updateAllBtn = el('btn-update-all');

    checkSelected.disabled = !EP.check;
    updateSelected.disabled = !EP.update;
    checkAllBtn.disabled = !EP.check;
    updateAllBtn.disabled = !EP.update;
    if (!EP.check) { checkAllBtn.title = 'Enable the "check" endpoint to use this'; }
    if (!EP.update) { updateAllBtn.title = 'Enable the "update" endpoint to use this'; }

    el('btn-refresh').addEventListener('click', function () { loadContainers(); });

    checkAllBtn.addEventListener('click', checkAll);
    updateAllBtn.addEventListener('click', updateAll);
    checkSelected.addEventListener('click', function () { checkMany(selectedNames()); });
    updateSelected.addEventListener('click', function () { updateMany(selectedNames()); });

    el('filter').addEventListener('input', function (event) {
      state.filter = event.target.value;
      render();
    });

    el('select-all').addEventListener('change', function (event) {
      var checked = event.target.checked;
      visibleContainers().forEach(function (item) { state.selected[item.name] = checked; });
      render();
    });

    Array.prototype.forEach.call(document.querySelectorAll('th.sortable'), function (header) {
      header.addEventListener('click', function () {
        var key = header.getAttribute('data-sort');
        if (state.sortKey === key) {
          state.sortAsc = !state.sortAsc;
        } else {
          state.sortKey = key;
          state.sortAsc = true;
        }

        Array.prototype.forEach.call(document.querySelectorAll('th.sortable'), function (other) {
          other.classList.remove('sorted', 'asc');
        });
        header.classList.add('sorted');
        if (state.sortAsc) { header.classList.add('asc'); }

        render();
      });
    });

    var auto = el('auto-refresh');
    var autoInterval = el('auto-refresh-interval');

    try {
      auto.checked = window.localStorage.getItem(AUTO_REFRESH_KEY) === '1';
      var saved = parseInt(window.localStorage.getItem(AUTO_INTERVAL_KEY), 10);
      if (saved) { autoInterval.value = String(saved); }
    } catch (err) { /* storage unavailable — keep defaults */ }

    auto.addEventListener('change', function () {
      try { window.localStorage.setItem(AUTO_REFRESH_KEY, auto.checked ? '1' : '0'); } catch (err) { /* ignore */ }
      if (auto.checked) { startAutoRefresh(); } else { stopAutoRefresh(); }
    });

    autoInterval.addEventListener('change', function () {
      try { window.localStorage.setItem(AUTO_INTERVAL_KEY, autoInterval.value); } catch (err) { /* ignore */ }
      if (auto.checked) { startAutoRefresh(); }
    });

    document.addEventListener('keydown', function (event) {
      if (event.key === 'r' && !event.metaKey && !event.ctrlKey && !event.altKey) {
        var tag = (event.target.tagName || '').toLowerCase();
        if (tag === 'input' || tag === 'textarea' || tag === 'select') { return; }
        loadContainers();
      }
    });

    // Keep relative timestamps honest between refreshes.
    setInterval(function () {
      if (!state.loading) { el('stat-last').textContent = relativeTime(state.lastLoaded); }
    }, 15000);

    if (auto.checked) { startAutoRefresh(); }
    loadContainers();
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
})();
