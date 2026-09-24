package main

import (
	"time"

	"noxa/internal/netproto"
)

// GetServerConfig returns the effective runtime settings exposed to server
// managers. The server checks ManageServer in role mode and legacy admin otherwise.
func (a *App) GetServerConfig() (netproto.ServerConfig, error) {
	m, err := a.requireCM()
	if err != nil {
		return netproto.ServerConfig{}, err
	}
	return m.getServerConfig()
}

func (m *connManager) getServerConfig() (netproto.ServerConfig, error) {
	f, err := m.request(netproto.MsgServerConfigQuery, netproto.MsgServerConfigResponse,
		netproto.ServerConfigQuery{}, 5*time.Second)
	if err != nil {
		return netproto.ServerConfig{}, err
	}
	var cfg netproto.ServerConfig
	if err := netproto.Decode(f, &cfg); err != nil {
		return netproto.ServerConfig{}, err
	}
	return cfg, nil
}

// SetServerConfig validates and persists runtime server settings.
func (a *App) SetServerConfig(cfg netproto.ServerConfig) (netproto.ServerConfig, error) {
	m, err := a.requireCM()
	if err != nil {
		return netproto.ServerConfig{}, err
	}
	return m.setServerConfig(cfg)
}

func (m *connManager) setServerConfig(cfg netproto.ServerConfig) (netproto.ServerConfig, error) {
	f, err := m.request(netproto.MsgServerConfigSet, netproto.MsgServerConfigResponse, cfg, 5*time.Second)
	if err != nil {
		return netproto.ServerConfig{}, err
	}
	var applied netproto.ServerConfig
	if err := netproto.Decode(f, &applied); err != nil {
		return netproto.ServerConfig{}, err
	}
	return applied, nil
}

// GetServerConfigForTab reads only the server shown when the editor opened.
func (a *App) GetServerConfigForTab(tabID string) (netproto.ServerConfig, error) {
	m, err := a.requireTabCM(tabID)
	if err != nil {
		return netproto.ServerConfig{}, err
	}
	return m.getServerConfig()
}

// SetServerConfigForTab cannot redirect a delayed editor save to another tab.
func (a *App) SetServerConfigForTab(tabID string, cfg netproto.ServerConfig) (netproto.ServerConfig, error) {
	m, err := a.requireTabCM(tabID)
	if err != nil {
		return netproto.ServerConfig{}, err
	}
	return m.setServerConfig(cfg)
}
