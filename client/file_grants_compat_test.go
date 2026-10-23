package main

import (
	"errors"
	"hash"
	"os"
)

// Internal transfer fixtures explicitly model a native save selection.
func fixtureDestination(path string) (*downloadDestination, error) {
	a := &App{}
	if _, err := a.grantNativePath("download", path); err != nil {
		return nil, err
	}
	return a.openDownloadSelection(path)
}
func resumeState(path string) (*os.File, int64, hash.Hash, error) {
	d, err := fixtureDestination(path)
	if err != nil {
		return nil, 0, nil, err
	}
	defer d.Close()
	return resumeInRoot(d)
}
func finalizeDownloadedPart(part, path string) error {
	if part != path+partSuffix {
		return errors.New("unexpected partial filename")
	}
	d, err := fixtureDestination(path)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.finalize()
}
func (m *connManager) ftDownloadProgress(id string, ep ftEndpoint, token, transferID, path string, p *ftProgress) error {
	d, err := fixtureDestination(path)
	if err != nil {
		return err
	}
	defer d.Close()
	return m.ftDownloadApproved(id, ep, token, transferID, path, p, d)
}
