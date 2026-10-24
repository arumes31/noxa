package main

import "noxa/internal/netproto"

type YouTubePreview = netproto.LinkPreviewResult

func (a *App) YouTubePreviewForTab(tabID, resource string) (YouTubePreview, error) {
	return a.serverLinkPreview(tabID, "youtube", resource)
}
