(() => {
  'use strict';
  const field = document.querySelector('.signal-field');
  const stage = document.querySelector('.hero-art');
  const toggle = document.querySelector('.signal-toggle');
  if (!field || !stage || !toggle) return;
  const canvas = field.querySelector('canvas');
  const reduced = matchMedia('(prefers-reduced-motion: reduce)');
  const desktop = matchMedia('(min-width: 901px) and (hover: hover) and (pointer: fine)');
  if (!('IntersectionObserver' in window) || !('ResizeObserver' in window)) return;
  let gl, program, buffer, observer, resize;
  let frame = 0, last = 0, visible = false, paused = false, lost = false;
  let x = .5, y = .5, vx = 0, vy = 0, tx = .5, ty = .5;
  const shaders = [];
  const allowed = () => desktop.matches && !reduced.matches && !navigator.connection?.saveData && !paused && !document.hidden && visible && !lost;
  const stop = () => { cancelAnimationFrame(frame); frame = 0; last = 0; };
  const fallback = () => {
    stop();
    field.dataset.renderer = 'static';
    canvas.hidden = true;
    toggle.hidden = true;
  };
  const compile = (type, source) => {
    const shader = gl.createShader(type);
    shaders.push(shader);
    gl.shaderSource(shader, source);
    gl.compileShader(shader);
    if (!gl.getShaderParameter(shader, gl.COMPILE_STATUS)) throw new Error('Signal shader unavailable');
    return shader;
  };
  let resolution, pointer, ink;
  const paint = () => {
    if (!gl || lost || !visible || document.hidden || reduced.matches || !desktop.matches || navigator.connection?.saveData) return;
    gl.uniform2f(resolution, canvas.width, canvas.height);
    gl.uniform2f(pointer, x, y);
    gl.drawArrays(gl.TRIANGLES, 0, 3);
  };
  const fit = () => {
    const bounds = field.getBoundingClientRect();
    if (!bounds.width || !bounds.height) return;
    const scale = Math.min(devicePixelRatio || 1, 1.25, Math.sqrt(500000 / (bounds.width * bounds.height)));
    canvas.width = Math.max(1, Math.round(bounds.width * scale));
    canvas.height = Math.max(1, Math.round(bounds.height * scale));
    gl.viewport(0, 0, canvas.width, canvas.height);
    paint();
  };
  const tick = now => {
    frame = 0;
    if (!allowed()) return;
    const dt = last ? Math.min((now - last) / 1000, .035) : 1 / 60;
    last = now;
    // A critically damped spring follows the pointer, then comes completely to rest.
    vx += ((tx - x) * 120 - vx * 22) * dt;
    vy += ((ty - y) * 120 - vy * 22) * dt;
    x += vx * dt;
    y += vy * dt;
    paint();
    if (Math.abs(tx - x) + Math.abs(ty - y) + Math.abs(vx) + Math.abs(vy) > .0005) frame = requestAnimationFrame(tick);
    else last = 0;
  };
  const wake = () => { if (gl && allowed() && !frame) frame = requestAnimationFrame(tick); };
  const init = () => {
    if (gl || lost) return;
    try {
      gl = canvas.getContext('webgl', { alpha: true, antialias: false, depth: false, stencil: false, powerPreference: 'low-power', preserveDrawingBuffer: false });
      if (!gl) { lost = true; return fallback(); }
      const vertex = compile(gl.VERTEX_SHADER, 'attribute vec2 position; void main(){gl_Position=vec4(position,0.,1.);}');
      const fragment = compile(gl.FRAGMENT_SHADER, `
        precision mediump float;
        uniform vec2 resolution;
        uniform vec2 pointer;
        uniform vec3 ink;
        void main(){
          vec2 uv=gl_FragCoord.xy/resolution;
          float amount=0.;
          float envelope=sin(uv.x*3.14159265);
          for(int i=0;i<8;i++){
            float strand=float(i);
            float phase=strand*.14+(pointer.x-.5)*1.1;
            float curve=.50+sin(uv.x*5.8+phase)*(.30+strand*.006)*envelope;
            curve+=(strand-3.5)*.026+(pointer.y-.5)*.22*envelope;
            float distance=abs(uv.y-curve);
            float line=1.-smoothstep(.0005,.0022,distance);
            amount=max(amount,line*(.30+strand*.055));
          }
          float edge=smoothstep(0.,.10,uv.x)*(1.-smoothstep(.88,1.,uv.x));
          float alpha=amount*edge;
          gl_FragColor=vec4(ink*alpha,alpha);
        }
      `);
      program = gl.createProgram();
      gl.attachShader(program, vertex);
      gl.attachShader(program, fragment);
      gl.linkProgram(program);
      if (!gl.getProgramParameter(program, gl.LINK_STATUS)) throw new Error('Signal renderer unavailable');
      gl.useProgram(program);
      buffer = gl.createBuffer();
      gl.bindBuffer(gl.ARRAY_BUFFER, buffer);
      gl.bufferData(gl.ARRAY_BUFFER, new Float32Array([-1, -1, 3, -1, -1, 3]), gl.STATIC_DRAW);
      const position = gl.getAttribLocation(program, 'position');
      gl.enableVertexAttribArray(position);
      gl.vertexAttribPointer(position, 2, gl.FLOAT, false, 0, 0);
      resolution = gl.getUniformLocation(program, 'resolution');
      pointer = gl.getUniformLocation(program, 'pointer');
      ink = gl.getUniformLocation(program, 'ink');
      const color = getComputedStyle(field).color.match(/[\d.]+/g)?.map(Number);
      if (!color || color.length < 3) throw new Error('Signal palette unavailable');
      gl.uniform3f(ink, color[0] / 255, color[1] / 255, color[2] / 255);
      resize = new ResizeObserver(fit);
      resize.observe(field);
      fit();
    } catch {
      lost = true;
      fallback();
    }
  };
  const sync = () => {
    if (!desktop.matches || reduced.matches || navigator.connection?.saveData || lost) return fallback();
    if (visible) init();
    if (!gl || lost) return;
    field.dataset.renderer = 'webgl';
    canvas.hidden = false;
    toggle.hidden = false;
    if (allowed()) { paint(); wake(); } else stop();
  };
  observer = new IntersectionObserver(entries => {
    visible = entries[0].isIntersecting;
    sync();
  });
  observer.observe(stage);
  stage.addEventListener('pointermove', event => {
    if (!allowed()) return;
    const bounds = stage.getBoundingClientRect();
    tx = Math.max(0, Math.min(1, (event.clientX - bounds.left) / bounds.width));
    ty = 1 - Math.max(0, Math.min(1, (event.clientY - bounds.top) / bounds.height));
    wake();
  }, { passive: true });
  stage.addEventListener('pointerleave', () => { tx = .5; ty = .5; wake(); });
  toggle.addEventListener('click', () => {
    paused = !paused;
    toggle.setAttribute('aria-pressed', String(paused));
    toggle.textContent = paused ? 'Resume visual effect' : 'Pause visual effect';
    sync();
  });
  reduced.addEventListener('change', sync);
  desktop.addEventListener('change', sync);
  document.addEventListener('visibilitychange', sync);
  canvas.addEventListener('webglcontextlost', event => { event.preventDefault(); lost = true; fallback(); });
  window.addEventListener('pagehide', event => {
    stop();
    visible = false;
    observer.disconnect();
    resize?.disconnect();
    // A back/forward-cache entry keeps its renderer, with all work suspended.
    if (event.persisted) return;
    if (gl && !lost) {
      gl.deleteBuffer(buffer);
      gl.deleteProgram(program);
      for (const shader of shaders) gl.deleteShader(shader);
    }
    lost = true;
    fallback();
  });
  window.addEventListener('pageshow', event => {
    if (!event.persisted) return;
    observer.observe(stage);
    resize?.observe(field);
    sync();
  });
})();
