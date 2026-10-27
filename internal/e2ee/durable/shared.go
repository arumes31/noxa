package durable

import (
	"bytes"
	bolt "go.etcd.io/bbolt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

type sharedEntry struct {
	store   *Store
	refs    int
	dirInfo os.FileInfo
}

var sharedStores = struct {
	sync.Mutex
	entries map[string]*sharedEntry
}{entries: map[string]*sharedEntry{}}

type Handle struct {
	*Store
	key  string
	once sync.Once
	err  error
}

// Share within one process; bbolt still rejects independent processes.
func OpenShared(dir string, ns Namespace, p Protection) (*Handle, error) {
	path, e := filepath.Abs(dir)
	if e != nil {
		return nil, e
	}
	if e = os.MkdirAll(path, 0700); e != nil {
		return nil, e
	}
	if resolved, e := filepath.EvalSymlinks(path); e == nil {
		path = resolved
	}
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
	}
	info, e := os.Stat(path)
	if e != nil {
		return nil, e
	}
	key := path + "\x00" + string(encode(ns))
	sharedStores.Lock()
	defer sharedStores.Unlock()
	entry := sharedStores.entries[key]
	// Windows junction aliases may not be resolved by EvalSymlinks.
	if entry == nil {
		for existingKey, candidate := range sharedStores.entries {
			if candidate.store.ns == string(encode(ns)) && os.SameFile(info, candidate.dirInfo) {
				key, entry = existingKey, candidate
				break
			}
		}
	}
	if entry == nil {
		s, e := Open(path, ns, p, 0)
		if e != nil {
			return nil, e
		}
		entry = &sharedEntry{store: s, dirInfo: info}
		sharedStores.entries[key] = entry
	}
	if p.Open == nil {
		return nil, ErrUnavailable
	}
	if e := entry.store.db.View(func(tx *bolt.Tx) error {
		candidate, e := p.Open(tx.Bucket(keyBucket).Get([]byte("master")))
		if e != nil {
			return ErrCorrupt
		}
		defer clear(candidate)
		if !bytes.Equal(candidate, entry.store.CoreKey()) {
			return ErrCorrupt
		}
		return nil
	}); e != nil {
		if entry.refs == 0 {
			delete(sharedStores.entries, key)
			_ = entry.store.Close()
		}
		return nil, e
	}
	entry.refs++
	return &Handle{Store: entry.store, key: key}, nil
}
func (h *Handle) Close() error {
	h.once.Do(func() {
		sharedStores.Lock()
		defer sharedStores.Unlock()
		entry := sharedStores.entries[h.key]
		if entry == nil {
			return
		}
		entry.refs--
		if entry.refs == 0 {
			delete(sharedStores.entries, h.key)
			h.err = entry.store.Close()
		}
	})
	return h.err
}
