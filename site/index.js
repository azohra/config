// Serves bootstrap.sh at bootstrap.azohra.com. curl gets the script, a browser
// gets a page — browsers send Accept: text/html, curl sends */*.

import script from '../bootstrap.sh';

const command = 'curl -fsSL bootstrap.azohra.com | bash';
const escape = (s) => s.replace(/[&<>]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;' })[c]);

const page = `<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="theme-color" content="#1d2021">
<meta name="description" content="One command turns a factory-fresh Mac into your Mac, from a machine repository you control.">
<title>Config</title>
<style>
  :root {
    color-scheme:dark;
    --bg:#1d2021; --panel:#282828; --panel-hi:#32302f; --fg:#ebdbb2;
    --muted:#a89984; --faint:#665c54; --yellow:#fabd2f; --green:#b8bb26;
    --aqua:#8ec07c;
  }
  * { box-sizing:border-box }
  html { min-height:100%; background:var(--bg) }
  body {
    min-height:100vh; margin:0; padding:clamp(1.5rem,5vw,4rem) 1.25rem;
    display:grid; place-items:center; overflow-x:hidden; color:var(--fg);
    background:
      radial-gradient(circle at 18% 8%,rgba(250,189,47,.09),transparent 30rem),
      radial-gradient(circle at 82% 85%,rgba(142,192,124,.07),transparent 28rem),
      var(--bg);
    font:16px/1.6 ui-sans-serif,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;
  }
  main { position:relative; width:min(100%,48rem) }
  a { color:var(--aqua) }
  .hero { padding:0 0 clamp(2.4rem,6vw,3.5rem) }
  h1 { max-width:11ch; margin:0; font-size:clamp(3rem,9vw,6.5rem); line-height:.9;
       letter-spacing:-.065em; font-weight:750 }
  h1 span { color:var(--yellow) }
  .lede { max-width:36rem; margin:1.75rem 0 0; color:var(--muted); font-size:clamp(1rem,2.5vw,1.2rem) }
  .terminal { overflow:hidden; border:1px solid var(--faint); border-radius:14px; background:var(--panel);
              box-shadow:0 1.5rem 5rem rgba(0,0,0,.28),0 0 0 1px rgba(235,219,178,.025) inset }
  .terminal-bar { min-height:2.75rem; display:flex; align-items:center;
                  padding:0 1rem; border-bottom:1px solid var(--faint); background:var(--panel-hi) }
  .lights { display:flex; gap:.42rem }
  .lights i { width:.58rem; height:.58rem; border-radius:50%; background:var(--faint) }
  .lights i:first-child { background:#fb4934 } .lights i:nth-child(2) { background:var(--yellow) }
  .lights i:last-child { background:var(--green) }
  .command-row { display:grid; grid-template-columns:minmax(0,1fr) auto; gap:1rem; align-items:center;
                 padding:1.15rem 1.25rem; border-bottom:1px solid var(--faint) }
  .command { min-width:0; display:flex; align-items:center; gap:.75rem; overflow-x:auto;
             scrollbar-width:none; font:500 clamp(.76rem,2.2vw,.92rem)/1.5 ui-monospace,SFMono-Regular,Menlo,monospace;
             white-space:nowrap; user-select:all }
  .command::-webkit-scrollbar { display:none }
  .prompt { color:var(--yellow); user-select:none }
  button { min-width:5.4rem; padding:.55rem .72rem; border:1px solid var(--faint); border-radius:7px;
           color:var(--fg); background:transparent; cursor:pointer; font:700 .65rem/1 ui-monospace,SFMono-Regular,Menlo,monospace;
           letter-spacing:.1em; transition:border-color .18s,color .18s,background .18s }
  button:hover { border-color:var(--yellow); color:var(--yellow); background:rgba(250,189,47,.06) }
  button:focus-visible { outline:2px solid var(--aqua); outline-offset:3px }
  button.copied { border-color:var(--green); color:var(--green); background:rgba(184,187,38,.06) }
  .output { padding:1.25rem; font:500 .8rem/1.9 ui-monospace,SFMono-Regular,Menlo,monospace }
  .boot-line { display:grid; grid-template-columns:1.1rem 1fr; gap:.55rem; opacity:0;
               transform:translateY(.35rem); animation:reveal .35s ease-out forwards;
               animation-delay:calc(180ms + var(--i) * 140ms) }
  .mark { color:var(--green) } .boot-line:last-child .mark { color:var(--yellow) }
  .cursor { display:inline-block; width:.48rem; height:.9rem; margin-left:.35rem; vertical-align:-.14rem;
            background:var(--yellow); animation:blink 1.1s steps(1,end) infinite }
  .how { margin:2rem 0 0; color:var(--muted); font-size:.95rem }
  .how p { margin:0 0 .75rem }
  details { margin-top:2rem; border-top:1px solid var(--faint); border-bottom:1px solid var(--faint) }
  summary { padding:1rem .1rem; display:flex; justify-content:space-between; color:var(--muted);
            cursor:pointer; list-style:none; font:600 .82rem/1.4 ui-sans-serif,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif }
  summary::-webkit-details-marker { display:none }
  summary::after { content:"+"; color:var(--yellow); font-size:1rem }
  details[open] summary::after { content:"−" }
  pre { max-height:28rem; margin:0 0 1rem; padding:1rem; overflow:auto; border:1px solid var(--faint);
        border-radius:8px; background:#1b1b1b; color:var(--muted);
        font:12px/1.6 ui-monospace,SFMono-Regular,Menlo,monospace; white-space:pre }
  @keyframes reveal { to { opacity:1; transform:none } }
  @keyframes blink { 50% { opacity:0 } }
  @media (max-width:35rem) {
    .command-row { grid-template-columns:1fr; gap:.8rem }
    button { width:100% }
  }
  @media (prefers-reduced-motion:reduce) {
    *,*::before,*::after { scroll-behavior:auto!important; animation-duration:.001ms!important;
                           animation-delay:0ms!important; transition-duration:.001ms!important }
  }
</style></head>
<body><main>
  <section class="hero">
    <h1>Make this Mac <span>yours.</span></h1>
    <p class="lede">One command takes a factory-fresh Mac from zero to Config, restored from a machine repository you control.</p>
  </section>
  <section class="terminal" aria-label="Bootstrap command and sequence">
    <div class="terminal-bar">
      <span class="lights" aria-hidden="true"><i></i><i></i><i></i></span>
    </div>
    <div class="command-row">
      <div class="command"><span class="prompt">$</span><code data-command-text>${command}</code></div>
      <button type="button" data-copy="${command}"><span data-copy-label aria-live="polite">Copy</span></button>
    </div>
    <div class="output" role="list">
      <div class="boot-line" role="listitem" style="--i:0"><span class="mark">✓</span><span>Xcode tools</span></div>
      <div class="boot-line" role="listitem" style="--i:1"><span class="mark">✓</span><span>Config verified</span></div>
      <div class="boot-line" role="listitem" style="--i:2"><span class="mark">✓</span><span>Machine repository</span></div>
      <div class="boot-line" role="listitem" style="--i:3"><span class="mark">→</span><span>Machine restore<span class="cursor" aria-hidden="true"></span></span></div>
    </div>
  </section>
  <section class="how">
    <p>The script asks for the Git URL of your machine repository, downloads the latest Config release and verifies it against the checksums published with it, then reaches your repository with whatever Git access this Mac already has and hands off to Config. A private HTTPS repository asks once for a personal access token, since GitHub does not accept an account password there. An SSH repository needs a key already on the Mac. Nothing else is installed; your repository declares every tool.</p>
    <p>Config is open source. <a href="https://github.com/azohra/config#readme">Read how a machine repository is declared.</a></p>
  </section>
  <details><summary>Source</summary><pre>${escape(script)}</pre></details>
  <script>
    const button = document.querySelector('[data-copy]');
    const label = button.querySelector('[data-copy-label]');
    button.addEventListener('click', async () => {
      try {
        await navigator.clipboard.writeText(button.dataset.copy);
        label.textContent = 'Copied';
        button.classList.add('copied');
        window.setTimeout(() => {
          label.textContent = 'Copy';
          button.classList.remove('copied');
        }, 1600);
      } catch {
        const range = document.createRange();
        range.selectNodeContents(document.querySelector('[data-command-text]'));
        const selection = window.getSelection();
        selection.removeAllRanges();
        selection.addRange(range);
        label.textContent = 'Selected';
      }
    });
  </script>
</main></body></html>`;

export default {
  fetch(request) {
    if (request.method !== 'GET' && request.method !== 'HEAD') {
      return new Response('Method not allowed\n', { status: 405, headers: { allow: 'GET, HEAD' } });
    }
    const html = (request.headers.get('accept') || '').includes('text/html');
    const headers = {
      'content-type': html ? 'text/html; charset=utf-8' : 'text/plain; charset=utf-8',
      'cache-control': 'no-store',
      'x-content-type-options': 'nosniff',
      'vary': 'accept',
    };
    if (html) {
      headers['content-security-policy'] = "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'";
      headers['referrer-policy'] = 'no-referrer';
    }
    return new Response(request.method === 'HEAD' ? null : html ? page : script, { headers });
  },
};
