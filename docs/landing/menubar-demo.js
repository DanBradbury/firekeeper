// Menu bar demo: drives the live mock in #menubar-demo. Sample data only, no
// network. Without this script the markup is a complete static panel.
(function () {
  var root = document.querySelector('[data-mb]');
  if (!root) return;

  var tray = root.querySelector('[data-mb-tray]');
  var pauseBtn = root.querySelector('[data-mb-pause]');
  var filter = root.querySelector('[data-mb-filter]');
  var empty = root.querySelector('[data-mb-empty]');
  var count = root.querySelector('[data-mb-count]');
  var toast = root.querySelector('[data-mb-toast]');
  var clock = root.querySelector('[data-mb-clock]');
  var live = document.getElementById('live');

  var LABELS = { working: 'Working', waiting: 'Waiting', needs: 'Needs input' };
  var DAYS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];
  var MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];

  var now = 0;          // seconds of simulated time
  var paused = false;
  var visible = !('IntersectionObserver' in window);
  var toastTimer = 0;

  // Seconds until each session's first change, so the demo starts with one of
  // every state and the first change comes soon after it scrolls into view.
  var FIRST = { landing: 11, api: 18, docs: 5 };
  var sessions = [];
  root.querySelectorAll('[data-mb-session]').forEach(function (row) {
    var id = row.dataset.mbSession;
    sessions.push({
      id: id,
      row: row,
      win: root.querySelector('[data-mb-win="' + id + '"]'),
      provider: row.dataset.provider,
      state: row.dataset.state,
      since: row.dataset.state === 'waiting' ? -120 : 0,
      next: FIRST[id] || 10,
      asks: false
    });
  });

  // Percent used per limit. Working sessions push their provider's numbers up.
  var usage = { 'codex-5h': 0, 'codex-week': 14, 'claude-5h': 15, 'claude-week': 29, 'copilot': 5 };
  var RATES = {
    codex: { 'codex-5h': 0.3, 'codex-week': 0.05 },
    claude: { 'claude-5h': 0.3, 'claude-week': 0.05 },
    copilot: { 'copilot': 0.04 }
  };

  function byId(id) {
    for (var i = 0; i < sessions.length; i++) if (sessions[i].id === id) return sessions[i];
    return null;
  }

  function between(lo, hi) { return lo + Math.floor(Math.random() * (hi - lo + 1)); }

  function ago(seconds) {
    if (seconds < 5) return 'just now';
    if (seconds < 60) return seconds + 's ago';
    return Math.floor(seconds / 60) + 'm ago';
  }

  function setState(s, state) {
    s.state = state;
    s.since = now;
    if (state === 'working') s.next = now + between(8, 14);
    else if (state === 'waiting') s.next = now + between(5, 9);
    else s.next = now + 16;
    s.row.dataset.state = state;
    if (s.win) s.win.dataset.state = state;
    s.row.querySelector('[data-mb-state]').textContent = LABELS[state];
    renderSession(s);
  }

  function renderSession(s) {
    var elapsed = now - s.since;
    s.row.querySelector('[data-mb-ago]').textContent = ago(elapsed);
    var el = s.win && s.win.querySelector('[data-mb-elapsed]');
    if (el && s.state === 'working') el.textContent = elapsed + 's';
  }

  function advance(s) {
    if (s.state !== 'working') return setState(s, 'working');
    // A finished turn alternates between asking for approval and going idle,
    // with at most one session asking at a time.
    s.asks = !s.asks;
    var someoneAsks = sessions.some(function (o) { return o.state === 'needs'; });
    setState(s, s.asks && !someoneAsks ? 'needs' : 'waiting');
  }

  function renderUsage() {
    Object.keys(usage).forEach(function (key) {
      var el = root.querySelector('[data-mb-limit="' + key + '"]');
      if (!el) return;
      var pct = Math.round(usage[key]) + '%';
      el.style.setProperty('--v', pct);
      el.querySelector('[data-mb-pct]').textContent = pct;
    });
  }

  function renderClock() {
    var d = new Date();
    var mm = d.getMinutes();
    clock.textContent = DAYS[d.getDay()] + ' ' + d.getDate() + ' ' + MONTHS[d.getMonth()] + ' ' +
      d.getHours() + ':' + (mm < 10 ? '0' : '') + mm;
  }

  function tick() {
    if (clock) renderClock();
    if (paused || !visible || document.hidden) return;
    now++;
    sessions.forEach(function (s) {
      if (now >= s.next) advance(s);
      else renderSession(s);
      if (s.state !== 'working') return;
      var rates = RATES[s.provider] || {};
      Object.keys(rates).forEach(function (key) {
        usage[key] = Math.min(96, usage[key] + rates[key]);
      });
    });
    renderUsage();
  }

  function say(text) {
    toast.textContent = text;
    toast.classList.add('show');
    if (live) live.textContent = text;
    clearTimeout(toastTimer);
    toastTimer = setTimeout(function () {
      toast.classList.remove('show');
      if (live) live.textContent = '';
    }, 2200);
  }

  function focusWin(id) {
    root.querySelectorAll('[data-mb-win]').forEach(function (w) {
      w.classList.toggle('front', w.dataset.mbWin === id);
    });
  }

  function setOpen(open) {
    root.toggleAttribute('data-open', open);
    tray.setAttribute('aria-expanded', open ? 'true' : 'false');
  }

  function applyFilter() {
    var q = filter.value.trim().toLowerCase();
    var shown = 0;
    sessions.forEach(function (s) {
      var hit = !q || s.row.dataset.search.indexOf(q) !== -1;
      s.row.hidden = !hit;
      if (hit) shown++;
    });
    empty.hidden = shown > 0;
    count.textContent = shown;
  }

  tray.addEventListener('click', function () { setOpen(!root.hasAttribute('data-open')); });

  pauseBtn.addEventListener('click', function () {
    paused = !paused;
    root.toggleAttribute('data-paused', paused);
    pauseBtn.setAttribute('aria-pressed', paused ? 'true' : 'false');
    pauseBtn.setAttribute('aria-label', paused ? 'Resume the live demo' : 'Pause the live demo');
  });

  root.addEventListener('click', function (e) {
    var go = e.target.closest('[data-mb-go]');
    if (go) {
      var s = byId(go.dataset.mbGo);
      focusWin(s.id);
      say('Switched to Ghostty · ' + s.id);
      return;
    }
    var approve = e.target.closest('[data-mb-approve]');
    if (approve) {
      var asked = byId(approve.dataset.mbApprove);
      if (asked.state === 'needs') {
        setState(asked, 'working');
        say('Approved in the terminal. Back to work.');
      }
      return;
    }
    var win = e.target.closest('[data-mb-win]');
    if (win) focusWin(win.dataset.mbWin);
  });

  filter.addEventListener('input', applyFilter);

  document.addEventListener('keydown', function (e) {
    var inside = root.matches(':hover') || root.contains(document.activeElement);
    if (!inside) return;
    if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
      e.preventDefault();
      setOpen(true);
      filter.focus();
    } else if (e.key === 'Escape' && root.hasAttribute('data-open')) {
      if (filter.value) { filter.value = ''; applyFilter(); return; }
      setOpen(false);
      tray.focus();
    }
  });

  // Open the panel as the demo scrolls into view, and only run while on screen.
  if (!visible) {
    var opened = false;
    setOpen(false);
    new IntersectionObserver(function (entries) {
      visible = entries[0].isIntersecting;
      if (visible && !opened) { opened = true; setOpen(true); }
    }, { threshold: 0.3 }).observe(root);
  }

  sessions.forEach(renderSession);
  renderUsage();
  if (clock) renderClock();
  setInterval(tick, 1000);
})();
