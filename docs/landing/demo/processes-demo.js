// Local browser TUI spike. Assets and WASM load only after visitor presses Start.
(() => {
  const frame = document.getElementById('processes-live');
  if (!frame) return;
  const button = frame.querySelector('[data-process-demo-start]');
  const status = frame.querySelector('[data-process-demo-status]');
  const container = frame.querySelector('[data-process-demo-terminal]');
  const video = frame.querySelector('video');
  const decoder = new TextDecoder();
  let term;
  let fit;
  let resizeObserver;
  let running = false;
  let firstOutput = false;
  let startedAt = 0;

  function script(src) {
    return new Promise((resolve, reject) => {
      const element = document.createElement('script');
      element.src = src;
      element.onload = resolve;
      element.onerror = () => reject(new Error(`Could not load ${src}`));
      document.head.append(element);
    });
  }

  async function ensureRuntime() {
    if (window.Terminal && window.FitAddon && window.Go) return;
    const css = document.createElement('link');
    css.rel = 'stylesheet';
    css.href = 'demo/vendor/xterm.css';
    document.head.append(css);
    await script('demo/vendor/xterm.js');
    await script('demo/vendor/addon-fit.js');
    await script('demo/wasm_exec.js');
  }

  function resize() {
    if (!term || !fit) return;
    fit.fit();
    if (window.firekeeperResize) window.firekeeperResize(term.cols, term.rows);
  }

  button.addEventListener('click', async () => {
    if (running) {
      term.focus();
      return;
    }
    button.disabled = true;
    status.textContent = 'Loading terminal…';
    try {
      await ensureRuntime();
      if (term) term.dispose();
      container.replaceChildren();
      term = new window.Terminal({
        fontFamily: 'JetBrains Mono, Menlo, monospace', fontSize: 13,
        lineHeight: 1.18, cursorBlink: false, scrollback: 0,
        theme: { background: '#110f0d', foreground: '#e7e0d8', cursor: '#f97316' }
      });
      fit = new window.FitAddon.FitAddon();
      term.loadAddon(fit);
      container.hidden = false;
      video.hidden = true;
      video.pause();
      term.open(container);
      resize();
      if (resizeObserver) resizeObserver.disconnect();
      resizeObserver = new ResizeObserver(resize);
      resizeObserver.observe(container);
      term.attachCustomKeyEventHandler(event => {
        if (event.key === 'Tab') event.preventDefault();
        return true;
      });
      term.onData(data => window.firekeeperInput?.(data));
      const response = await fetch('demo/firekeeper-demo.wasm?usage-demo=1');
      if (!response.ok) throw new Error('WASM not built; run cmd/demo-wasm/build.sh');
      const go = new window.Go();
      const bytes = await response.arrayBuffer();
      const result = await WebAssembly.instantiate(bytes, go.importObject);
      firstOutput = false;
      startedAt = performance.now();
      window.firekeeperOutput = data => {
        term.write(decoder.decode(data, { stream: true }));
        if (!firstOutput) {
          firstOutput = true;
          status.textContent = `Live demo · first frame ${Math.round(performance.now() - startedAt)} ms`;
        }
      };
      running = true;
      button.disabled = false;
      button.textContent = 'Focus terminal';
      const done = go.run(result.instance);
      resize();
      term.focus();
      await done;
      running = false;
      button.textContent = 'Restart demo';
      status.textContent = 'Demo ended. Restart to try again.';
    } catch (error) {
      running = false;
      button.textContent = 'Retry demo';
      status.textContent = error.message;
      container.hidden = true;
      video.hidden = false;
      video.play().catch(() => {});
      button.disabled = false;
    }
  });
})();
