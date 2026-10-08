/* TrackSphere embeddable tracking widget (P2-3). No dependencies.
 *
 * Usage (any https tenant site — CSP frame-ancestors allows https framers):
 *
 *   <div id="ts-track"></div>
 *   <script src="https://app.example.com/widget.js"
 *     data-tracking="TS-8842-LAG" data-target="ts-track"></script>
 *
 * Or programmatically:
 *
 *   TrackSphereWidget.mount(document.getElementById('ts-track'), {
 *     base: 'https://app.example.com', tracking: 'TS-8842-LAG', height: 640,
 *   });
 *
 * Isolation: the tracker runs in a sandboxed iframe (same-origin to the app,
 * so the session cookie rides for nothing — the embed is fully public, same
 * projection as /track). The iframe may only send its height outward; the
 * parent never sends data inward. Modern browsers prefer CSP frame-ancestors
 * over X-Frame-Options, so cross-origin framing works despite SAMEORIGIN.
 */
(function () {
  'use strict';

  function mount(el, opts) {
    if (!el || !opts || !opts.base || !opts.tracking) return null;
    var iframe = document.createElement('iframe');
    iframe.src =
      String(opts.base).replace(/\/$/, '') +
      '/embed/' +
      encodeURIComponent(opts.tracking);
    iframe.title = 'Shipment tracking';
    iframe.style.width = '100%';
    iframe.style.height = (opts.height || 640) + 'px';
    iframe.style.border = '0';
    iframe.setAttribute('sandbox', 'allow-scripts allow-same-origin');
    iframe.setAttribute('loading', 'lazy');
    el.appendChild(iframe);

    // Auto-resize on the embed's height posts (height only — no data).
    function onMessage(ev) {
      var url;
      try {
        url = new URL(opts.base);
      } catch (e) {
        return;
      }
      if (ev.origin !== url.origin) return;
      var d = ev.data;
      if (d && d.tracksphere === 'resize' && typeof d.height === 'number') {
        iframe.style.height = Math.min(Math.max(d.height, 200), 5000) + 'px';
      }
    }
    window.addEventListener('message', onMessage);
    return iframe;
  }

  window.TrackSphereWidget = { mount: mount };

  // Declarative boot: <script ... data-tracking data-target>.
  function boot() {
    var scripts = document.querySelectorAll('script[data-tracking]');
    for (var i = 0; i < scripts.length; i++) {
      var s = scripts[i];
      var target = s.getAttribute('data-target');
      var el = target ? document.getElementById(target) : s.parentElement;
      mount(el, {
        base: s.src.replace(/\/widget\.js.*$/, ''),
        tracking: s.getAttribute('data-tracking'),
      });
    }
  }
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', boot);
  } else {
    boot();
  }
})();
