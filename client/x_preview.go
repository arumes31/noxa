package main

import "noxa/internal/netproto"

type XPostPreview = netproto.LinkPreviewResult

func (a *App) XPostPreviewForTab(tabID, resource string) (XPostPreview, error) {
	return a.serverLinkPreview(tabID, "x", resource)
}
