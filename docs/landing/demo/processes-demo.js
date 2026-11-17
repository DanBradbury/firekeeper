// The Processes demo: the real dashboard, compiled to WebAssembly
// (cmd/demo-wasm) and run on invented sample sessions. Nothing here is
// loaded until the section is on screen or the visitor asks for it, and
// when the demo cannot run the recording takes its place.
(() => {
  const frame = document.querySelector('[data-process-demo]');
  if (!frame) return;
  const slot = frame.querySelector('.process-demo-slot');
  const poster = frame.querySelector('.process-demo-poster');
  const container = frame.querySelector('[data-process-demo-terminal]');
  const bar = frame.querySelector('[data-process-demo-bar]');
  const startButton = frame.querySelector('[data-process-demo-start]');
  const pauseButton = frame.querySelector('[data-process-demo-pause]');
  const status = frame.querySelector('[data-process-demo-status]');
  const caption = frame.querySelector('[data-process-demo-caption]');
  const decoder = new TextDecoder();
  const reducedMotion = matchMedia('(prefers-reduced-motion: reduce)').matches;
  const FOCUS_RELEASE = 'Ctrl+]';

  let term;
  let fit;
  let resizeObserver;
  let moduleReady;
  let running = false;
  let paused = false;
  let fallbackShown = false;

  const connection = navigator.connection || {};
  const canRun =
    typeof WebAssembly === 'object' &&
    typeof WebAssembly.instantiate === 'function' &&
    !matchMedia('(pointer: coarse)').matches &&
    matchMedia('(min-width: 700px)').matches;
  const autostart = canRun && !reducedMotion && !connection.saveData &&
    !/^(slow-2g|2g|3g)$/.test(connection.effectiveType || '');

  function showFallback(message) {
    if (fallbackShown) return;
    fallbackShown = true;
    container.hidden = true;
    poster.hidden = true;
    const video = document.createElement('video');
    video.autoplay = true; video.muted = true; video.loop = true; video.playsInline = true;
    video.preload = 'metadata';
    video.poster = poster.src;
    video.width = 2112; video.height = 1216;
    video.setAttribute('aria-label', poster.alt);
    for (const [src, type] of [['media/processes-live.webm', 'video/webm'], ['media/processes-live.mp4', 'video/mp4']]) {
      const source = document.createElement('source');
      source.src = src; source.type = type;
      video.append(source);
    }
    slot.append(video);
    bar.hidden = !message;
    startButton.hidden = true;
    pauseButton.hidden = true;
    status.textContent = message || '';
    caption.textContent = 'The real app, recorded against demo projects.';
  }

  function loadScript(src) {
    return new Promise((resolve, reject) => {
      const element = document.createElement('script');
      element.src = src;
      element.onload = resolve;
      element.onerror = () => reject(new Error(`Could not load ${src}`));
      document.head.append(element);
    });
  }

  function loadStyle(href) {
    return new Promise((resolve, reject) => {
      const element = document.createElement('link');
      element.rel = 'stylesheet';
      element.href = href;
      element.onload = resolve;
      element.onerror = () => reject(new Error(`Could not load ${href}`));
      document.head.append(element);
    });
  }

  // Runtime, loader and compiled program, fetched once. A failure is not
  // remembered, so Retry starts clean.
  function loadModule() {
    if (!moduleReady) {
      moduleReady = (async () => {
        const manifestResponse = await fetch('demo/manifest.json', { cache: 'no-cache' });
        if (!manifestResponse.ok) throw new Error('The demo is not available right now.');
        const manifest = await manifestResponse.json();
        for (const name of [manifest.wasm, manifest.exec]) {
          if (!/^[\w.-]+$/.test(name)) throw new Error('The demo manifest is invalid.');
        }
        const wasmResponse = fetch(`demo/${manifest.wasm}`);
        await Promise.all([
          loadStyle('demo/vendor/xterm.css'),
          loadScript('demo/vendor/xterm.js'),
          loadScript('demo/vendor/addon-fit.js'),
          loadScript(`demo/${manifest.exec}`),
        ]);
        const response = await wasmResponse;
        if (!response.ok) throw new Error('The demo is not available right now.');
        const copy = response.clone();
        try {
          return await WebAssembly.compileStreaming(response);
        } catch (error) {
          // A server that does not label the file application/wasm.
          return WebAssembly.compile(await copy.arrayBuffer());
        }
      })();
      moduleReady.catch(() => { moduleReady = null; });
    }
    return moduleReady;
  }

  function fitTerminal() {
    if (!term || !fit || container.hidden) return;
    fit.fit();
    window.firekeeperResize?.(term.cols, term.rows);
  }

  function setPaused(value) {
    paused = value;
    pauseButton.setAttribute('aria-pressed', String(value));
    pauseButton.textContent = value ? 'Resume sample updates' : 'Pause sample updates';
    window.firekeeperPause?.(value);
  }

  function idleStatus() {
    return `Click the terminal to type. ${FOCUS_RELEASE} leaves it. Sample data only.`;
  }

  async function start() {
    if (running) {
      term.focus();
      return;
    }
    startButton.disabled = true;
    bar.hidden = false;
    status.textContent = 'Loading the demo…';
    try {
      const compiled = await loadModule();
      try { await document.fonts.load('13px "JetBrains Mono"'); } catch (_) { /* measure with the fallback font */ }
      resizeObserver?.disconnect();
      term?.dispose();
      container.replaceChildren();
      term = new window.Terminal({
        fontFamily: '"JetBrains Mono", "Geist Mono", ui-monospace, Menlo, Consolas, monospace',
        fontSize: 13, lineHeight: 1.15, cursorBlink: false, scrollback: 0, allowProposedApi: false,
        theme: { background: '#181a26', foreground: '#c6cde8', cursor: '#181a26', cursorAccent: '#181a26' },
      });
      fit = new window.FitAddon.FitAddon();
      term.loadAddon(fit);
      container.hidden = false;
      poster.hidden = true;
      term.open(container);
      // Keep Tab and the arrow keys for the dashboard, with one way out for
      // keyboard users.
      term.attachCustomKeyEventHandler(event => {
        if (event.type === 'keydown' && event.ctrlKey && event.key === ']') {
          term.blur();
          startButton.focus();
          return false;
        }
        return true;
      });
      term.onData(data => window.firekeeperInput?.(data));
      fit.fit();
      let first = true;
      const began = performance.now();
      window.firekeeperOutput = data => {
        term.write(decoder.decode(data, { stream: true }));
        if (first) {
          first = false;
          status.textContent = idleStatus();
        }
      };
      const go = new window.Go();
      const instance = await WebAssembly.instantiate(compiled, go.importObject);
      running = true;
      startButton.disabled = false;
      startButton.textContent = 'Focus terminal';
      pauseButton.hidden = false;
      caption.textContent = 'The real Firekeeper, compiled to WebAssembly and running on invented sample sessions. Use ↑ ↓, Tab, Enter, c, ? and q.';
      const done = go.run(instance);
      setPaused(reducedMotion || paused);
      resizeObserver = new ResizeObserver(() => requestAnimationFrame(fitTerminal));
      resizeObserver.observe(container);
      fitTerminal();
      if (!autostart || document.activeElement === startButton) term.focus();
      await done;
      running = false;
      pauseButton.hidden = true;
      startButton.textContent = 'Restart demo';
      status.textContent = 'Demo ended.';
      window.firekeeperOutput = undefined;
    } catch (error) {
      running = false;
      console.error('Firekeeper demo failed', error);
      showFallback('The live demo could not start, so this is a recording.');
    } finally {
      startButton.disabled = false;
    }
  }

  startButton.addEventListener('click', start);
  pauseButton.addEventListener('click', () => setPaused(!paused));

  if (!canRun) {
    showFallback('');
    return;
  }

  bar.hidden = false;
  if (!autostart) {
    status.textContent = reducedMotion
      ? 'Starts when you ask, because you prefer reduced motion.'
      : 'Starts when you ask, to save data.';
    return;
  }
  startButton.textContent = 'Start demo';
  status.textContent = 'Loads when you scroll here.';
  if (!('IntersectionObserver' in window)) {
    start();
    return;
  }
  const observer = new IntersectionObserver(entries => {
    if (entries.some(entry => entry.isIntersecting)) {
      observer.disconnect();
      start();
    }
  }, { rootMargin: '300px 0px' });
  observer.observe(frame);
})();
