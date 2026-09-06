// The token is read from the document rather than the URL, so it appears once in the
// address bar on the first navigation and never again in a later request or in history.
const TOKEN = document.body.dataset.token;

function api(path, options = {}) {
  const headers = new Headers(options.headers || {});
  headers.set('X-NoiseCrypt-Token', TOKEN);
  return fetch(path, { ...options, headers });
}

// The cell field ----------------------------------------------------------
//
// The signature element, and it is not decoration: it draws the same grid of macro
// cells the codec draws, so the interface shows you the shape your file takes. It
// idles slowly and speeds up while work is in flight, which makes the one piece of
// feedback a local tool cannot otherwise give, namely that something is happening.
//
// Nobody should be made to watch noise move, so under prefers-reduced-motion it is
// painted once and left alone.
const field = (() => {
  const canvas = document.getElementById('field');
  const ctx = canvas.getContext('2d', { alpha: false });
  const still = window.matchMedia('(prefers-reduced-motion: reduce)').matches;

  // Small cells, sparsely lit. Half the cells filled at this size reads as a broken
  // checkerboard competing with the content rather than as texture behind it, which is
  // what it looked like the first time it was rendered in a real browser.
  const CELL = 14;
  const DENSITY = 0.12;
  let cols = 0, rows = 0, busy = false, timer = null;

  function resize() {
    const dpr = Math.min(window.devicePixelRatio || 1, 2);
    canvas.width = Math.floor(window.innerWidth * dpr);
    canvas.height = Math.floor(window.innerHeight * dpr);
    canvas.style.width = window.innerWidth + 'px';
    canvas.style.height = window.innerHeight + 'px';
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    cols = Math.ceil(window.innerWidth / CELL);
    rows = Math.ceil(window.innerHeight / CELL);
    paint();
  }

  function paint() {
    ctx.fillStyle = '#0a0b0d';
    ctx.fillRect(0, 0, window.innerWidth, window.innerHeight);
    // Two levels, full contrast, exactly as the two-level profiles modulate.
    ctx.fillStyle = busy ? '#ffb020' : '#e8e6e1';
    for (let y = 0; y < rows; y++) {
      for (let x = 0; x < cols; x++) {
        if (Math.random() > DENSITY) continue;
        ctx.fillRect(x * CELL, y * CELL, CELL - 1, CELL - 1);
      }
    }
  }

  function loop() {
    paint();
    timer = setTimeout(() => requestAnimationFrame(loop), busy ? 90 : 900);
  }

  window.addEventListener('resize', resize);
  resize();
  if (!still) loop();

  return {
    working(on) {
      busy = on;
      if (still) paint();
    },
  };
})();

function show(el, message, kind = 'info') {
  el.textContent = message;
  el.className = kind;
}

// download hands a response body to the browser as a file.
//
// The filename comes from Content-Disposition, which the server derives from the name
// stored inside the container. That name has already been sanitised twice by the
// container layer, on write and again on read.
async function download(response, fallbackName) {
  const blob = await response.blob();
  const disposition = response.headers.get('Content-Disposition') || '';
  const match = disposition.match(/filename="(.*)"$/);
  const name = match ? match[1] : fallbackName;

  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = name;
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
  return name;
}

async function errorFrom(response) {
  try {
    const body = await response.json();
    return body.error || `request failed with status ${response.status}`;
  } catch {
    return `request failed with status ${response.status}`;
  }
}

// Tabs.
const tabs = [...document.querySelectorAll('[role="tab"]')];
tabs.forEach((tab) => {
  tab.addEventListener('click', () => {
    tabs.forEach((t) => {
      const selected = t === tab;
      t.setAttribute('aria-selected', String(selected));
      document.getElementById(t.getAttribute('aria-controls')).hidden = !selected;
    });
  });
});

// Encrypt.
document.getElementById('form-seal').addEventListener('submit', async (event) => {
  event.preventDefault();
  const out = document.getElementById('out-seal');
  const form = event.target;
  const button = form.querySelector('button[type="submit"]');

  // Disabled for the whole request: a second submission while the first is running
  // would encrypt the same file twice and download two containers.
  button.disabled = true;
  field.working(true);
  show(out, 'Encrypting...', 'busy');

  try {
    const response = await api('/api/seal', { method: 'POST', body: new FormData(form) });
    if (!response.ok) {
      show(out, await errorFrom(response), 'error');
      return;
    }
    const name = await download(response, 'container.ncry');
    show(out, `Encrypted. Downloaded ${name}.`, 'ok');
  } catch (err) {
    show(out, String(err), 'error');
  } finally {
    field.working(false);
    button.disabled = false;
  }
});

// Decrypt.
document.getElementById('form-open').addEventListener('submit', async (event) => {
  event.preventDefault();
  const out = document.getElementById('out-open');
  const form = event.target;
  const button = form.querySelector('button[type="submit"]');

  button.disabled = true;
  field.working(true);
  show(out, 'Decrypting...', 'busy');

  try {
    const response = await api('/api/open', { method: 'POST', body: new FormData(form) });
    if (!response.ok) {
      show(out, await errorFrom(response), 'error');
      return;
    }
    const signer = response.headers.get('X-NoiseCrypt-Signer');
    const name = await download(response, 'recovered.bin');
    show(
      out,
      signer
        ? `Recovered ${name}. Signature verified, signed by ${signer}.`
        : `Recovered ${name}. Not signed: nothing proves who produced it.`,
      'ok',
    );
  } catch (err) {
    show(out, String(err), 'error');
  } finally {
    field.working(false);
    button.disabled = false;
  }
});

// Keys.
document.getElementById('btn-keygen').addEventListener('click', async () => {
  const out = document.getElementById('out-keys');
  show(out, 'Generating...');

  try {
    const response = await api('/api/keygen', { method: 'POST' });
    if (!response.ok) {
      show(out, await errorFrom(response), 'error');
      return;
    }
    const id = await response.json();

    // Rendered as elements rather than through innerHTML. The values are ours rather
    // than a user's, but building markup from strings is the habit that eventually
    // meets a value that is not.
    out.textContent = '';
    out.className = 'ok';

    const warning = document.createElement('p');
    warning.className = 'warning';
    warning.textContent =
      'Save the private identity now. There is no recovery: lose it and everything ' +
      'encrypted to it is gone permanently.';
    out.append(warning);

    for (const [label, value] of [
      ['Fingerprint', id.short],
      ['Public identity, share this', id.public],
      ['Private identity, keep this secret', id.private],
    ]) {
      const heading = document.createElement('h3');
      heading.textContent = label;
      const box = document.createElement('textarea');
      box.readOnly = true;
      box.rows = label === 'Fingerprint' ? 1 : 4;
      box.value = value;
      out.append(heading, box);
    }
  } catch (err) {
    show(out, String(err), 'error');
  }
});

// Profiles.
(async () => {
  const body = document.querySelector('#profiles tbody');
  try {
    const response = await api('/api/profiles');
    if (!response.ok) return;

    for (const p of await response.json()) {
      const row = document.createElement('tr');
      const cells = [
        p.name,
        `${p.perFrame.toLocaleString()} B`,
        `${Math.round(p.overhead * 100)} %`,
        `${(p.bytesPerSecond / 1024).toFixed(1)} KiB/s`,
        // Three states, not two. "archive" has no platform to be carried across, by
        // design, so a yes/no column reads its local measurement as a gap.
        `${p.evidence}, ${p.evidenceNote}`,
      ];
      cells.forEach((text, i) => {
        const cell = document.createElement('td');
        cell.textContent = text;
        if (i === 4) cell.className = p.evidence === 'platform' ? 'yes' : 'no';
        row.append(cell);
      });
      row.title = p.summary;
      body.append(row);
    }
  } catch {
    // The table is informational; failing to fill it is not worth an alarm.
  }
})();
