// studio-gl.js — WebGL renderer for the Studio program (20).
//
// The program canvas stays a 2D canvas: it owns the one stable captureStream
// track, and a canvas that has a WebGL context can never fall back to 2D. The
// scene is composited here on the GPU (textured quads with crop, position and
// alpha; crossfades through two framebuffers) and copied to the program canvas
// with one drawImage. If WebGL is unavailable or the context is lost, the
// engine draws with its 2D path instead; the published track never changes.
//
// Colour handling matches the 2D canvas: sources are uploaded premultiplied
// and blended source-over (ONE, ONE_MINUS_SRC_ALPHA) without colour-space
// conversion beyond the browser default, over an opaque black background.

// Frames a source texture may stay unused (about 3 s at 30 fps) before it is
// deleted; a source hidden briefly keeps its texture.
export const TEXTURE_IDLE_FRAMES = 90;

const VERTEX = `#version 300 es
in vec2 a_unit;
uniform vec4 u_dest;   // x, y, w, h in output pixels (top-left origin)
uniform vec4 u_src;    // u0, v0, u1, v1 in texture space
uniform vec2 u_size;   // output width, height
out vec2 v_uv;
void main() {
    vec2 pixel = u_dest.xy + a_unit * u_dest.zw;
    vec2 clip = vec2(pixel.x / u_size.x * 2.0 - 1.0, 1.0 - pixel.y / u_size.y * 2.0);
    gl_Position = vec4(clip, 0.0, 1.0);
    v_uv = mix(u_src.xy, u_src.zw, a_unit);
}`;

const FRAGMENT = `#version 300 es
precision mediump float;
in vec2 v_uv;
uniform sampler2D u_texture;
uniform float u_alpha;
uniform vec4 u_color;   // premultiplied; used when u_solid is 1
uniform int u_solid;
out vec4 outColor;
void main() {
    vec4 color = u_solid == 1 ? u_color : texture(u_texture, v_uv);
    outColor = color * u_alpha;
}`;

/** Parses #rgb / #rrggbb into premultiplied [r, g, b, 1] floats (opaque). */
export function parseHexColor(value) {
    const text = String(value || "").trim();
    let match = /^#([0-9a-f]{6})$/i.exec(text);
    if (match) {
        const n = parseInt(match[1], 16);
        return [(n >> 16 & 255) / 255, (n >> 8 & 255) / 255, (n & 255) / 255, 1];
    }
    match = /^#([0-9a-f]{3})$/i.exec(text);
    if (match) {
        const [r, g, b] = match[1].split("").map(c => parseInt(c + c, 16) / 255);
        return [r, g, b, 1];
    }
    return [0, 0, 0, 1];
}

/**
 * Normalised texture rectangle for a crop {l, t, r, b} (fractions removed from
 * each side). Texture row 0 is the top of the uploaded image (no flip).
 */
export function cropToUV(crop = {}) {
    const l = crop.l || 0, t = crop.t || 0, r = crop.r || 0, b = crop.b || 0;
    return [l, t, 1 - r, 1 - b];
}

// Software rasterisers (no GPU, remote desktops, blocklisted drivers) run
// WebGL far slower than the GPU-backed 2D canvas: in a SwiftShader run the
// Studio program took about 51 ms per frame on WebGL versus 4 ms in 2D.
const SOFTWARE_RENDERER = /swiftshader|llvmpipe|softpipe|software|basic render driver|microsoft basic/i;

/** True when the renderer string names a software rasteriser. */
export function isSoftwareRenderer(name) {
    return SOFTWARE_RENDERER.test(String(name || ""));
}

export class StudioGLRenderer {
    /**
     * Throws when WebGL2 is unavailable; callers fall back to 2D.
     * onLost runs once when the context is lost (the engine switches to 2D).
     */
    constructor({ document = globalThis.document, width, height, onLost = () => {}, allowSoftware = false }) {
        this.width = width;
        this.height = height;
        this.canvas = document.createElement("canvas");
        this.canvas.width = width;
        this.canvas.height = height;
        this.lost = false;
        this.onLost = onLost;
        this.canvas.addEventListener?.("webglcontextlost", event => {
            event.preventDefault();
            this.markLost();
        });
        const gl = this.canvas.getContext("webgl2", {
            alpha: false, antialias: false, depth: false, stencil: false,
            premultipliedAlpha: true, preserveDrawingBuffer: true, powerPreference: "high-performance",
        });
        if (!gl) throw new Error("WebGL2 is unavailable");
        this.gl = gl;
        const info = gl.getExtension("WEBGL_debug_renderer_info");
        this.rendererName = String((info ? gl.getParameter(info.UNMASKED_RENDERER_WEBGL) : gl.getParameter(gl.RENDERER)) || "");
        if (!allowSoftware && isSoftwareRenderer(this.rendererName)) {
            gl.getExtension("WEBGL_lose_context")?.loseContext();
            throw new Error("WebGL runs on a software rasteriser");
        }
        // source object -> { texture, version, used }; entries unused for
        // TEXTURE_IDLE_FRAMES are deleted so GPU memory follows the scene.
        this.textures = new Map();
        this.frame = 0;
        this.framebuffers = [];
        this.init();
    }

    init() {
        const gl = this.gl;
        const compile = (type, source) => {
            const shader = gl.createShader(type);
            gl.shaderSource(shader, source);
            gl.compileShader(shader);
            if (!gl.getShaderParameter(shader, gl.COMPILE_STATUS)) throw new Error(gl.getShaderInfoLog(shader) || "shader compile failed");
            return shader;
        };
        const program = gl.createProgram();
        gl.attachShader(program, compile(gl.VERTEX_SHADER, VERTEX));
        gl.attachShader(program, compile(gl.FRAGMENT_SHADER, FRAGMENT));
        gl.linkProgram(program);
        if (!gl.getProgramParameter(program, gl.LINK_STATUS)) throw new Error(gl.getProgramInfoLog(program) || "program link failed");
        gl.useProgram(program);
        this.program = program;
        this.uniforms = Object.fromEntries(["u_dest", "u_src", "u_size", "u_alpha", "u_color", "u_solid", "u_texture"]
            .map(name => [name, gl.getUniformLocation(program, name)]));
        const buffer = gl.createBuffer();
        gl.bindBuffer(gl.ARRAY_BUFFER, buffer);
        gl.bufferData(gl.ARRAY_BUFFER, new Float32Array([0, 0, 1, 0, 0, 1, 1, 1]), gl.STATIC_DRAW);
        const location = gl.getAttribLocation(program, "a_unit");
        gl.enableVertexAttribArray(location);
        gl.vertexAttribPointer(location, 2, gl.FLOAT, false, 0, 0);
        gl.enable(gl.BLEND);
        gl.blendFunc(gl.ONE, gl.ONE_MINUS_SRC_ALPHA);
        gl.pixelStorei(gl.UNPACK_PREMULTIPLY_ALPHA_WEBGL, true);
        gl.uniform1i(this.uniforms.u_texture, 0);
        gl.activeTexture(gl.TEXTURE0);
    }

    markLost() {
        if (this.lost) return;
        this.lost = true;
        this.onLost();
    }

    get usable() {
        return !this.lost && !this.gl.isContextLost();
    }

    newTexture() {
        const gl = this.gl, texture = gl.createTexture();
        gl.bindTexture(gl.TEXTURE_2D, texture);
        gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR);
        gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.LINEAR);
        gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE);
        gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE);
        return texture;
    }

    /**
     * Binds a texture for source. Live sources (video, canvas) upload every
     * frame; static ones (image bitmaps, rendered text) only when their
     * version changes. Returns false when the upload failed.
     */
    bindSource(source, { live = true, version = 0 } = {}) {
        const gl = this.gl;
        let entry = this.textures.get(source);
        if (!entry) {
            entry = { texture: this.newTexture(), version: -1, used: this.frame };
            this.textures.set(source, entry);
        }
        entry.used = this.frame;
        gl.bindTexture(gl.TEXTURE_2D, entry.texture);
        if (live || entry.version !== version) {
            try {
                gl.texImage2D(gl.TEXTURE_2D, 0, gl.RGBA, gl.RGBA, gl.UNSIGNED_BYTE, source);
            } catch {
                return false;
            }
            entry.version = version;
        }
        return true;
    }

    /** Ends a frame: deletes textures of sources not drawn for a while. */
    endFrame() {
        this.frame++;
        for (const [source, entry] of this.textures) {
            if (this.frame - entry.used > TEXTURE_IDLE_FRAMES) {
                this.gl.deleteTexture(entry.texture);
                this.textures.delete(source);
            }
        }
    }

    /** Releases the texture of a source that is no longer drawn. */
    forget(source) {
        const entry = this.textures.get(source);
        if (!entry) return;
        this.gl.deleteTexture(entry.texture);
        this.textures.delete(source);
    }

    framebuffer(index) {
        const gl = this.gl;
        if (!this.framebuffers[index]) {
            const texture = this.newTexture();
            gl.texImage2D(gl.TEXTURE_2D, 0, gl.RGBA, this.width, this.height, 0, gl.RGBA, gl.UNSIGNED_BYTE, null);
            const fbo = gl.createFramebuffer();
            gl.bindFramebuffer(gl.FRAMEBUFFER, fbo);
            gl.framebufferTexture2D(gl.FRAMEBUFFER, gl.COLOR_ATTACHMENT0, gl.TEXTURE_2D, texture, 0);
            gl.bindFramebuffer(gl.FRAMEBUFFER, null);
            this.framebuffers[index] = { fbo, texture };
        }
        return this.framebuffers[index];
    }

    /** Starts drawing into framebuffer index (or the canvas when null). */
    target(index = null) {
        const gl = this.gl;
        gl.bindFramebuffer(gl.FRAMEBUFFER, index === null ? null : this.framebuffer(index).fbo);
        gl.viewport(0, 0, this.width, this.height);
        gl.uniform2f(this.uniforms.u_size, this.width, this.height);
        gl.clearColor(0, 0, 0, 1);
        gl.clear(gl.COLOR_BUFFER_BIT);
    }

    quad(dest, uv, alpha) {
        const gl = this.gl;
        gl.uniform4f(this.uniforms.u_dest, dest.x, dest.y, dest.w, dest.h);
        gl.uniform4f(this.uniforms.u_src, uv[0], uv[1], uv[2], uv[3]);
        gl.uniform1f(this.uniforms.u_alpha, alpha);
        gl.drawArrays(gl.TRIANGLE_STRIP, 0, 4);
    }

    /** Draws the bound texture region uv into dest (output pixels). */
    drawTexture(dest, uv, alpha = 1) {
        this.gl.uniform1i(this.uniforms.u_solid, 0);
        this.quad(dest, uv, alpha);
    }

    /** Fills dest with an opaque CSS hex colour. */
    fill(dest, color) {
        const gl = this.gl, [r, g, b, a] = parseHexColor(color);
        gl.uniform1i(this.uniforms.u_solid, 1);
        gl.uniform4f(this.uniforms.u_color, r, g, b, a);
        this.quad(dest, [0, 0, 1, 1], 1);
    }

    /**
     * Draws framebuffer index over the current target with alpha. Framebuffer
     * textures have their origin at the bottom, so the rows are flipped.
     */
    drawFramebuffer(index, alpha) {
        const gl = this.gl;
        gl.bindTexture(gl.TEXTURE_2D, this.framebuffer(index).texture);
        gl.uniform1i(this.uniforms.u_solid, 0);
        this.quad({ x: 0, y: 0, w: this.width, h: this.height }, [0, 1, 1, 0], alpha);
    }

    dispose() {
        const gl = this.gl;
        for (const entry of this.textures.values()) gl.deleteTexture(entry.texture);
        this.textures.clear();
        for (const entry of this.framebuffers) {
            if (!entry) continue;
            gl.deleteFramebuffer(entry.fbo);
            gl.deleteTexture(entry.texture);
        }
        this.framebuffers = [];
        gl.getExtension("WEBGL_lose_context")?.loseContext();
        this.canvas.width = 0;
        this.canvas.height = 0;
    }
}
