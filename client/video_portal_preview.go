package main

import "noxa/internal/netproto"

type VideoPortalPreview = netproto.LinkPreviewResult

func (a *App) VideoPortalPreviewForTab(tabID, provider, resource string) (VideoPortalPreview, error) {
	return a.serverLinkPreview(tabID, provider, resource)
}
