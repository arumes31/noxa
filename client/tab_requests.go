package main

import "fmt"

// requireTabCM captures the expected tab under the activation lock. Native tab
// activation can precede the frontend's tab_reset notification. Once captured,
// the manager stays bound to the original server for the entire operation.
func (a *App) requireTabCM(tabID string) (*connManager, error) {
	a.tabsMu.Lock()
	defer a.tabsMu.Unlock()
	tab := a.tabs[tabID]
	if tabID == "" || tabID != a.activeID || tab == nil || tab.cm == nil || tab.cm != a.cmLoad() {
		return nil, fmt.Errorf("server tab changed; refresh the editor")
	}
	return tab.cm, nil
}
