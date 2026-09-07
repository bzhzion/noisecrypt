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

// Tabs --------------------------------------------------------------------
//
// The keyboard half of this was missing, and its absence was worse than not using the
// pattern at all. Declaring role="tab" tells a screen reader "this is a tab list, the
// arrow keys move between them, one stop holds them all in the page's tab order". None
// of that was true: four separate tab stops, arrow keys inert. Plain buttons with no
// role would have been more honest and more usable.
//
// So: a roving tabindex, arrows, Home and End, per the ARIA authoring practices. Content
// is not moved into the panels, so activation on focus is safe and is what the practices
// prefer, the alternative being a user who arrows past four tabs and reaches none.
const tabs = [...document.querySelectorAll('[role="tab"]')];

function selectTab(tab, moveFocus = true) {
  tabs.forEach((t) => {
    const selected = t === tab;
    t.setAttribute('aria-selected', String(selected));
    // Exactly one tab is reachable by Tab; the arrows reach the rest. Without this the
    // tab list is four stops on the way to the form, every time.
    t.tabIndex = selected ? 0 : -1;
    document.getElementById(t.getAttribute('aria-controls')).hidden = !selected;
  });
  if (moveFocus) tab.focus();
}

tabs.forEach((tab, i) => {
  tab.tabIndex = tab.getAttribute('aria-selected') === 'true' ? 0 : -1;
  tab.addEventListener('click', () => selectTab(tab, false));
  tab.addEventListener('keydown', (event) => {
    const step = { ArrowRight: 1, ArrowDown: 1, ArrowLeft: -1, ArrowUp: -1 }[event.key];
    let target = null;
    if (step) target = tabs[(i + step + tabs.length) % tabs.length];
    else if (event.key === 'Home') target = tabs[0];
    else if (event.key === 'End') target = tabs[tabs.length - 1];
    if (!target) return;
    event.preventDefault();
    selectTab(target);
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
    // An unsigned container that got this far was allowed through on purpose, since the
    // form now offers to refuse one. Saying so points at the checkbox rather than
    // leaving a warning the reader can do nothing about.
    show(
      out,
      signer
        ? `Recovered ${name}. Signature verified, signed by ${signer}.`
        : `Recovered ${name}. Not signed, and no signature was demanded: nothing proves who produced it.`,
      'ok',
    );
  } catch (err) {
    show(out, String(err), 'error');
  } finally {
    field.working(false);
    button.disabled = false;
  }
});

// Unlocking a protected identity -------------------------------------------
//
// An identity stored by `noisecrypt keygen` is locked under a passphrase unless someone
// asked for it not to be, so the interface has to be able to open one or the command line
// and the page disagree about what a key file is.
//
// The field appears only when the identity in front of it actually is locked, rather than
// sitting there permanently. A passphrase box that is usually irrelevant is a passphrase
// box people fill in out of habit, and a habit of typing secrets into fields that did not
// need them is the habit worth not teaching.
function attachUnlock(box) {
  const label = document.createElement('label');
  label.hidden = true;
  label.textContent = 'Passphrase protecting this identity';

  const input = document.createElement('input');
  input.type = 'password';
  input.name = 'identityPassphrase';
  input.autocomplete = 'current-password';
  input.hidden = true;
  input.disabled = true;
  label.htmlFor = input.id = box.id + '-unlock';

  const review = () => {
    const locked = box.value.trim().startsWith('noisecrypt-locked-v1:');
    label.hidden = input.hidden = !locked;
    // Disabled as well as hidden, so a hidden field never submits a stale value from
    // an identity the user has since replaced.
    input.disabled = !locked;
    if (!locked) input.value = '';
  };

  box.addEventListener('input', review);
  box.addEventListener('change', review);
  box.insertAdjacentElement('afterend', label);
  label.insertAdjacentElement('afterend', input);
  review();
  return review;
}

// Loading an identity from a file ------------------------------------------
//
// The page offered to save a key to a file and then insisted you paste it back by hand
// the next time, which meant opening the file, selecting all, copying. The command line
// has taken `-identity <file>` from the start, so the interface was the awkward one.
//
// Attached to every field that expects an identity rather than to a list of ids, so a
// field added later gets it without anyone remembering to wire it up.
for (const box of document.querySelectorAll('textarea[placeholder^="noisecrypt-"]')) {
  // Only the private fields can hold a locked identity; a public one never is, and
  // offering a passphrase box under public data would be inviting a secret where none
  // belongs.
  const reviewLock = box.placeholder.startsWith('noisecrypt-secret') ? attachUnlock(box) : null;
  const picker = document.createElement('input');
  picker.type = 'file';
  // No accept filter. Nothing forces an identity to be named .key or .txt, and a filter
  // that hides the file you are looking for is worse than no filter at all.
  picker.hidden = true;

  const button = document.createElement('button');
  button.type = 'button';
  button.className = 'btn btn-secondary btn-small';
  button.textContent = 'Load from a file';
  button.addEventListener('click', () => picker.click());

  picker.addEventListener('change', async () => {
    const file = picker.files[0];
    if (!file) return;
    try {
      // Trimmed, because the file we write ends with a newline and so does anything
      // `keygen -out` produced. The server trims too, but a textarea showing a stray
      // blank line looks like a mistake the reader then goes hunting for.
      box.value = (await file.text()).trim();
      // Setting value from code fires no input event, so the check has to be asked for.
      // Without this a key loaded from a file looked unlocked and the passphrase field
      // never appeared, which is exactly the case this whole thing exists for.
      if (reviewLock) reviewLock();
      confirmOn(button, 'Loaded');
    } catch (err) {
      confirmOn(button, 'Unreadable');
    } finally {
      // Cleared so choosing the same file twice fires change again.
      picker.value = '';
    }
  });

  const bar = document.createElement('div');
  bar.className = 'keyactions';
  bar.append(button, picker);
  box.parentNode.insertBefore(bar, box);
}

// Keys --------------------------------------------------------------------

// confirmOn briefly replaces a button's label, because a copy that gives no sign of
// having happened leaves you pressing it again to be sure.
function confirmOn(button, message) {
  const original = button.textContent;
  button.textContent = message;
  button.disabled = true;
  setTimeout(() => {
    button.textContent = original;
    button.disabled = false;
  }, 1400);
}

function copyButton(value) {
  const button = document.createElement('button');
  button.type = 'button';
  button.className = 'btn btn-secondary btn-small';
  button.textContent = 'Copy';
  button.addEventListener('click', async () => {
    try {
      // Available because a page served from 127.0.0.1 counts as a secure context,
      // the same way https does. The fallback covers a browser that disagrees rather
      // than leaving the button silently inert.
      await navigator.clipboard.writeText(value);
      confirmOn(button, 'Copied');
    } catch {
      const scratch = document.createElement('textarea');
      scratch.value = value;
      scratch.style.position = 'fixed';
      scratch.style.opacity = '0';
      document.body.append(scratch);
      scratch.select();
      const ok = document.execCommand('copy');
      scratch.remove();
      confirmOn(button, ok ? 'Copied' : 'Copy failed');
    }
  });
  return button;
}

function saveButton(value, filename) {
  const button = document.createElement('button');
  button.type = 'button';
  button.className = 'btn btn-secondary btn-small';
  button.textContent = 'Save to a file';
  button.addEventListener('click', () => {
    // A trailing newline, and nothing else in the file, so what the browser writes is
    // byte for byte what `noisecrypt keygen -out` writes. A file saved here therefore
    // works with `-identity` on the command line, which it would not if this added a
    // header or a label for readability.
    const blob = new Blob([value + '\n'], { type: 'text/plain' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = filename;
    document.body.append(a);
    a.click();
    a.remove();
    URL.revokeObjectURL(url);
    confirmOn(button, 'Saved');
  });
  return button;
}

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

    for (const [label, value, filename] of [
      ['Fingerprint', id.short, null],
      ['Public identity, share this', id.public, 'noisecrypt-public.txt'],
      ['Private identity, keep this secret', id.private, 'noisecrypt-identity.key'],
    ]) {
      const heading = document.createElement('h3');
      heading.textContent = label;

      // The page used to tell you to save the private identity and then offer no way
      // to do it, leaving select-all-and-copy by hand as the only route to something it
      // called unrecoverable. An instruction the interface does not support is not an
      // instruction, it is a reproach.
      const bar = document.createElement('div');
      bar.className = 'keyactions';
      bar.append(copyButton(value));
      if (filename) bar.append(saveButton(value, filename));

      const box = document.createElement('textarea');
      box.readOnly = true;
      box.rows = label === 'Fingerprint' ? 1 : 4;
      box.value = value;

      out.append(heading, bar, box);
    }
  } catch (err) {
    show(out, String(err), 'error');
  }
});

// Video ------------------------------------------------------------------
//
// The two long-running operations. Neither reports progress, and pretending otherwise
// would be worse than saying so: the encoder hands frames to FFmpeg through a pipe with
// no total to divide by, so any bar drawn here would be an animation rather than a
// measurement.

// duration formats seconds the way someone deciding whether to wait reads them.
function duration(seconds) {
  if (seconds < 60) return `${Math.round(seconds)} s`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)} min ${String(Math.round(seconds % 60)).padStart(2, '0')} s`;
  return `${Math.floor(seconds / 3600)} h ${String(Math.floor((seconds % 3600) / 60)).padStart(2, '0')} min`;
}

// A long request needs its button held down for its whole duration, and needs the field
// to say something is happening. Every video call goes through this.
async function longRunning(button, out, message, work) {
  button.disabled = true;
  field.working(true);
  show(out, message, 'busy');
  try {
    return await work();
  } catch (err) {
    show(out, String(err), 'error');
    return null;
  } finally {
    field.working(false);
    button.disabled = false;
  }
}

document.getElementById('btn-estimate').addEventListener('click', async (event) => {
  const form = document.getElementById('form-encode');
  const out = document.getElementById('out-encode');
  if (!form.reportValidity()) return;

  await longRunning(event.target, out, 'Sealing, to measure it for real...', async () => {
    const response = await api('/api/estimate', { method: 'POST', body: new FormData(form) });
    if (!response.ok) {
      show(out, await errorFrom(response), 'error');
      return;
    }
    const e = await response.json();
    show(
      out,
      `${e.frames.toLocaleString()} frames, ${duration(e.seconds)} of video at ` +
        `${e.width}x${e.height}, ${e.fps} fps. Sealed size ${e.sealed.toLocaleString()} B.`,
      'ok',
    );
  });
});

document.getElementById('form-encode').addEventListener('submit', async (event) => {
  event.preventDefault();
  const form = event.target;
  const out = document.getElementById('out-encode');
  const button = form.querySelector('button[type="submit"]');

  await longRunning(button, out, 'Encoding. This runs for as long as the video is long.', async () => {
    const response = await api('/api/encode', { method: 'POST', body: new FormData(form) });
    if (!response.ok) {
      show(out, await errorFrom(response), 'error');
      return;
    }
    const name = await download(response, 'container.mp4');
    show(out, `Encoded. Downloaded ${name}.`, 'ok');
  });
});

document.getElementById('form-decode').addEventListener('submit', async (event) => {
  event.preventDefault();
  const form = event.target;
  const out = document.getElementById('out-decode');
  const button = form.querySelector('button[type="submit"]');

  const address = document.getElementById('dec-url').value.trim();
  const body = new FormData(form);
  // Both entries are dropped when empty rather than sent blank. An empty file input
  // submits a part all the same, with no name and no bytes, so a request that named an
  // address would arrive looking like one that named both and be refused.
  if (address === '') body.delete('url');
  else body.delete('file');

  const waiting = address === ''
    ? 'Reading every frame...'
    : 'Downloading, then reading every frame...';

  await longRunning(button, out, waiting, async () => {
    const response = await api('/api/decode', { method: 'POST', body });
    if (!response.ok) {
      show(out, await errorFrom(response), 'error');
      return;
    }
    const seen = response.headers.get('X-NoiseCrypt-Frames');
    const bad = response.headers.get('X-NoiseCrypt-Unreadable');
    const signer = response.headers.get('X-NoiseCrypt-Signer');
    const name = await download(response, 'recovered.bin');

    // The unreadable count is reported even on success, because it is the one number
    // that says how much margin was left. Redundancy absorbing damage silently is the
    // system working, and also the thing you want to know before trusting it again.
    const dropped = response.headers.get('X-NoiseCrypt-Discarded');
    let text = `Recovered ${name} from ${seen} frames`;
    text += bad === '0' ? ', none unreadable' : `, ${bad} unreadable`;
    // Two different losses, both reported. A frame the geometry could not locate and a
    // frame whose shard failed its CRC are not the same event, and only the first used
    // to appear anywhere: this line could say "none unreadable" on a video that had lost
    // frames all the same.
    text += dropped === '0' ? ' and none discarded.' : ` and ${dropped} discarded, all corrected.`;
    text += signer
      ? ` Signature verified, signed by ${signer}.`
      : ' Not signed: nothing proves who produced it.';
    show(out, text, 'ok');
  });
});

// yt-dlp is not bundled either, and its absence costs far less than FFmpeg's: everything
// works, and only the field that would have saved a manual download is missing. So it is
// a sentence rather than a warning, and the field only exists when the tool does.
//
// The file input stops being required the moment an address is typed, and becomes
// required again when the address is cleared. Leaving `required` on both would make the
// browser refuse to submit a perfectly good request, and taking it off both would let an
// empty form through to be refused by the server instead of by the field.
function announceYtdlp(tools) {
  const file = document.getElementById('dec-file');
  const box = document.getElementById('dec-url-box');
  const url = document.getElementById('dec-url');

  if (!tools.ytdlp) {
    const absent = document.getElementById('dec-url-absent');
    absent.innerHTML =
      'Install <code>yt-dlp</code> and a field appears here for pasting the address of ' +
      'a video, so it can be fetched and decoded in one step instead of being ' +
      'downloaded by hand first.';
    absent.hidden = false;
    return;
  }

  document.getElementById('dec-url-hint').innerHTML =
    'Fetched with <code>yt-dlp</code>, at the highest quality the channel still offers. ' +
    'A lower rendition has fewer pixels to a macropixel, and below the profile\'s ' +
    'tolerance the file does not come back at all.';
  box.hidden = false;

  const sync = () => {
    const typed = url.value.trim() !== '';
    file.required = !typed;
    // Disabled and not merely optional, so the exclusivity is visible rather than
    // discovered by having a request refused.
    file.disabled = typed;
  };
  url.addEventListener('input', sync);
  sync();
}

// FFmpeg is not bundled. Say so once, up front, rather than letting a button fail after
// the user has chosen a file and typed a passphrase.
(async () => {
  try {
    const response = await api('/api/tools');
    if (!response.ok) return;
    const tools = await response.json();
    announceYtdlp(tools);
    if (tools.ffmpeg) return;

    const banner = document.getElementById('no-ffmpeg');
    banner.textContent =
      'FFmpeg was not found, so the video steps cannot run. Everything else on this ' +
      'page works without it. ' + (tools.reason || '');
    banner.hidden = false;
    document.querySelectorAll('#panel-video form button').forEach((b) => (b.disabled = true));
  } catch {
    // Not being able to ask is not worth an alarm; the routes report it themselves.
  }
})();

// Profiles.
(async () => {
  const body = document.querySelector('#profiles tbody');
  const selects = [...document.querySelectorAll('#enc-profile, #dec-profile')];
  const hint = document.getElementById('enc-profile-hint');
  try {
    const response = await api('/api/profiles');
    if (!response.ok) return;

    const profiles = await response.json();

    // The channel pickers are filled from the same source as the table, so a profile
    // cannot exist in one and not the other.
    const summaries = {};
    for (const p of profiles) {
      summaries[p.name] = p.summary;
      for (const select of selects) {
        const option = document.createElement('option');
        option.value = p.name;
        option.textContent = p.name;
        select.append(option);
      }
    }
    const encode = document.getElementById('enc-profile');
    const describe = () => { hint.textContent = summaries[encode.value] || ''; };
    encode.addEventListener('change', describe);
    describe();

    for (const p of profiles) {
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

// ── L'identite de ce poste, annoncee partout ou elle sert ─────────────────────────
//
// Le defaut que ceci corrige : les champs « votre identite privee » etaient vides, donc
// l'interface ne disait jamais qu'il y avait deja une identite sur la machine ni laquelle.
// L'utilisateur en deduisait qu'il n'avait rien, ou ne savait pas ce qui serait utilise.
// Or la ligne de commande, elle, prend l'identite du profil des qu'on ne precise rien : le
// comportement etait donc correct et invisible.
//
// L'empreinte vient du fichier public, qui n'est PAS chiffre, donc tout ceci s'affiche
// sans reclamer de phrase de passe. C'est ce qui rend l'annonce gratuite.
async function chargerIdentite() {
  const boite = document.getElementById('id-etat');
  let e;
  try {
    e = await (await fetch('api/identity')).json();
  } catch (err) {
    if (boite) boite.innerHTML = '<p class="hint">Could not read the identity state.</p>';
    return null;
  }

  if (boite) {
    if (!e.exists) {
      boite.innerHTML =
        '<p><b>No identity on this machine yet.</b></p>' +
        '<p class="hint">Create one below. You need it to receive files encrypted for you ' +
        'specifically, and to sign what you produce. Encrypting with a passphrase works ' +
        'without one.</p>';
    } else {
      const empreinte = e.fingerprint
        ? '<p>Fingerprint <code>' + e.fingerprint + '</code></p>' +
          '<p class="hint">Read that out to whoever sent you theirs, over some other ' +
          'channel than the one the identity travelled on. That is the whole point of it.</p>'
        : '<p class="hint">Its public half is missing. Run <code>noisecrypt identity</code> ' +
          'once and it will be written back from the private key.</p>';
      boite.innerHTML =
        '<p><b>This machine has an identity, and it is the one used by default.</b></p>' +
        '<p class="hint">Private key <code>' + e.path + '</code>' +
        (e.locked ? ', protected by a passphrase.' :
          '. <b>Not protected</b>: anyone who reads that file has it.') + '</p>' +
        (e.publicPath ? '<p class="hint">Public half <code>' + e.publicPath + '</code></p>' : '') +
        empreinte;
    }
  }

  // La meme information la ou elle change une decision : au-dessus des champs qui
  // acceptent une identite privee. Dire « c'est celle du poste qui sert » et offrir d'en
  // designer une autre POUR CETTE FOIS, sans jamais toucher a celle du poste.
  for (const id of ['open-key', 'dec-key', 'seal-sign', 'enc-sign']) {
    const champ = document.getElementById(id);
    if (!champ) continue;

    // ⚠️ Reutiliser la note existante plutot que de sauter quand elle est deja la.
    // La premiere version posait un drapeau et passait son tour aux appels suivants :
    // apres avoir installe une identite depuis l'onglet Identites, ces lignes
    // continuaient donc d'affirmer « il n'y a pas d'identite sur cette machine ». Une
    // annonce qui ne se rafraichit pas est pire qu'une absence d'annonce, puisqu'elle
    // decrit un etat revolu avec l'autorite du present.
    const signe = id.endsWith('sign');
    let note = champ.previousElementSibling;
    if (!note || !note.classList.contains('id-annonce')) {
      note = document.createElement('p');
      note.className = 'hint id-annonce';
      champ.insertAdjacentElement('beforebegin', note);
    }
    if (e.exists) {
      note.innerHTML = 'Leave this empty and ' +
        (signe ? 'the signature uses ' : 'opening uses ') +
        'this machine’s identity' +
        (e.fingerprint ? ' (<code>' + e.fingerprint + '</code>)' : '') +
        '. Fill it only to use a different one, just for this operation. Nothing here ' +
        'changes or replaces the identity on this machine.';
    } else {
      note.innerHTML = 'There is no identity on this machine, so this field is the only ' +
        'way to supply one. See the Identities tab.';
    }
  }
  return e;
}

// Appelee au chargement, puis apres chaque generation : sans ce second appel la page
// continuerait d'annoncer « aucune identite » juste apres en avoir cree une, ce qui est
// exactement le genre d'incoherence qui fait douter de ce que l'outil a reellement fait.
chargerIdentite();
document.getElementById('btn-keygen')?.addEventListener('click', () => {
  setTimeout(chargerIdentite, 400);
});

// ── Installer l'identite du poste ────────────────────────────────────────────────
//
// Deux boutons volontairement distincts, et la distinction est le point :
//
//   Generate         fabrique une identite et la remet a cette page. Rien sur le disque.
//   Install on disk  fabrique une identite ET l'ecrit ou la machine la cherchera.
//
// Confondre les deux etait le defaut d'origine : un seul bouton nomme « Generate » qui
// n'ecrivait rien, donc l'utilisateur croyait avoir equipe sa machine et le dossier restait
// vide. Un nom qui ne dit pas si quelque chose a ete ecrit ne peut pas etre compris.
document.getElementById('btn-install')?.addEventListener('click', async () => {
  const sortie = document.getElementById('out-install');
  const phrase = document.getElementById('id-pass');
  const sans = document.getElementById('id-nopass');

  const e = await (await fetch('api/identity')).json().catch(() => ({}));
  const ou = e.path || 'the default location';

  // L'alerte nomme le CHEMIN EXACT avant d'ecrire. « on va mettre une cle sur le disque »
  // sans dire ou est precisement l'information qui manquait et qui a fait poser la question
  // « ca m'a cree une identite, mais ou ? ».
  let question = 'A new identity will be generated and written to:\n\n' + ou +
    '\n\nIt becomes the identity this machine uses. Continue?';
  if (e.exists) {
    question = 'An identity ALREADY EXISTS at:\n\n' + ou +
      '\n\nReplacing it DESTROYS access to everything encrypted to it. ' +
      'This cannot be undone and there is no recovery.\n\n' +
      (e.fingerprint ? 'The one you would lose has fingerprint ' + e.fingerprint + '.\n\n' : '') +
      'Replace it?';
  }
  if (!confirm(question)) {
    sortie.textContent = 'Nothing written.';
    return;
  }

  const corps = new FormData();
  if (sans?.checked) {
    corps.set('noPassphrase', '1');
  } else {
    corps.set('passphrase', phrase?.value || '');
  }
  if (e.exists) corps.set('force', '1');

  sortie.textContent = 'Writing…';
  try {
    const r = await fetch('api/identity/install', { method: 'POST', body: corps });
    const t = await r.text();
    if (!r.ok) { sortie.textContent = t.trim(); return; }
    const d = JSON.parse(t);
    sortie.innerHTML = 'Written to <code>' + d.path + '</code>' +
      (d.protected ? ', protected by your passphrase.' :
        '. <b>Not protected.</b>') +
      '<br>Fingerprint <code>' + d.fingerprint + '</code>' +
      '<br>Back that file up. Lose it and everything encrypted to it is gone.';
    if (phrase) phrase.value = '';
    chargerIdentite();
  } catch (err) {
    sortie.textContent = String(err);
  }
});
