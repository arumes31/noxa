import { regionFrameLayout } from "./share-frame-geometry.js";

// One bounded stream with crop metadata, no canvas or extra codec. Keep the
// same alignment contract on the worker and on runtimes without workers.
export function captureFrameTransform({ region, getOutput, onLayout }) {
    let lastLayout = "";
    return new TransformStream({
        transform(frame, controller) {
            let wrapped = null, transferred = false;
            try {
                const layout = regionFrameLayout(frame, region, getOutput());
                const visible = frame.visibleRect || { x: 0, y: 0, width: frame.codedWidth, height: frame.codedHeight };
                if (Object.keys(layout.visibleRect).every(key => layout.visibleRect[key] === visible[key]) &&
                    layout.displayWidth === frame.displayWidth && layout.displayHeight === frame.displayHeight) {
                    controller.enqueue(frame);
                    transferred = true;
                } else {
                    wrapped = new VideoFrame(frame, layout);
                    controller.enqueue(wrapped);
                    wrapped = null;
                }
                const key = JSON.stringify(layout);
                if (key !== lastLayout) { lastLayout = key; onLayout(layout); }
            } finally {
                if (!transferred) frame.close();
                wrapped?.close();
            }
        },
    });
}
