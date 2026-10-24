package main

import (
	"fmt"
	"noxa/internal/netproto"
	"strings"
	"time"
)

func (a *App) ImagePreviewForTab(tabID, resource string) (netproto.LinkPreviewResult, error) {
	return a.serverLinkPreview(tabID, "image", resource)
}
func (a *App) LinkPreviewForTab(tabID, resource string) (netproto.LinkPreviewResult, error) {
	return a.serverLinkPreview(tabID, "web", resource)
}
func (a *App) serverLinkPreview(tabID, provider, resource string) (netproto.LinkPreviewResult, error) {
	var result netproto.LinkPreviewResult
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return result, err
	}
	settings := a.GetSettings()
	if settings.LinkPreviews != nil && !*settings.LinkPreviews {
		return result, fmt.Errorf("link previews disabled")
	}
	cm.mu.Lock()
	supported, conn := cm.supportsLinkPreview, cm.conn
	cm.mu.Unlock()
	if !supported {
		return result, fmt.Errorf("server link previews unavailable")
	}
	frame, err := cm.requestOn(conn, netproto.MsgLinkPreviewRequest, netproto.MsgLinkPreviewResult, netproto.LinkPreviewRequest{Provider: provider, Resource: resource}, 12*time.Second)
	if err != nil {
		return result, err
	}
	if err = netproto.Decode(frame, &result); err != nil {
		return result, err
	}
	// A legacy or hostile server must not turn this into a client HTTP request.
	if len(result.Thumbnail) > 280000 || !strings.HasPrefix(result.Thumbnail, "data:image/jpeg;base64,") {
		result.Thumbnail = ""
	}
	return result, nil
}
