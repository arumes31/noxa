# Offline camera segmentation assets

Camera effects run in a dedicated local worker. No frames or model requests are
sent to an external service. Disabling effects avoids loading these assets.

Runtime: `@mediapipe/tasks-vision` **0.10.32** (Apache-2.0), pinned in package-lock.json.
The bundled `vision_bundle.js` (renamed from `.cjs` for browser MIME handling), `vision_wasm_nosimd_internal.js`, and
`vision_wasm_nosimd_internal.wasm` are unmodified copies of that package. The
non-SIMD CPU runtime supports machines without WebAssembly SIMD.

Model: Google MediaPipe Selfie Segmenter, float16, version **1**.
Source: https://storage.googleapis.com/mediapipe-models/image_segmenter/selfie_segmenter/float16/1/selfie_segmenter.tflite
SHA-256: `191ac9529ae506ee0beefa6b2c945a172dab9d07d1e802a290a4e4038226658b`
The single confidence output is the person/foreground mask.
Model card: https://storage.googleapis.com/mediapipe-assets/Model%20Card%20MediaPipe%20Selfie%20Segmentation.pdf
Usage documentation: https://ai.google.dev/edge/mediapipe/solutions/vision/image_segmenter/web_js

`LICENSE.txt` contains the upstream Apache-2.0 license. To update the runtime,
pin the new package version and replace all three matching runtime assets
together. The model remains independently versioned and pinned.
