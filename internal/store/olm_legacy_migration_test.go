package store

import (
	"strings"
	"testing"
)

func TestOlmAdmissionMigrationPreservesLegacyPendingAndHistory(t *testing.T) {
	s := testScratchStore(t)
	applyPreLedgerMigrations(t, s, "062")
	if _, e := s.db.Exec(`CREATE TABLE schema_migrations(filename TEXT PRIMARY KEY,checksum TEXT,applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); e != nil {
		t.Fatal(e)
	}
	migrations, e := loadEmbeddedMigrations()
	if e != nil {
		t.Fatal(e)
	}
	for _, m := range migrations {
		if strings.HasPrefix(m.filename, "062") {
			break
		}
		if _, e = s.db.Exec(`INSERT INTO schema_migrations(filename,checksum) VALUES($1,$2)`, m.filename, m.checksum); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = s.db.Exec(`INSERT INTO users(unique_id,nickname) VALUES('legacy','legacy');INSERT INTO user_devices(device_id,principal,kind,name,ed25519_public,first_device) VALUES('legacy-device','legacy','desktop','old device','existing-noXa-key',TRUE);INSERT INTO offline_messages(from_user_id,to_user_id,from_unique_id,message,to_device_id) SELECT id,id,'legacy','legacy NaCl pending ciphertext','legacy-device' FROM users WHERE unique_id='legacy';INSERT INTO device_history_chunks(device_id,seq,total,from_device_id,data) VALUES('legacy-device',0,1,'old-device',convert_to('legacy sealed history','UTF8'))`); e != nil {
		t.Fatal(e)
	}
	if e = s.Migrate(); e != nil {
		t.Fatal(e)
	}
	var pending, history string
	if e = s.db.QueryRow(`SELECT message FROM offline_messages WHERE to_device_id='legacy-device' AND delivered_at IS NULL`).Scan(&pending); e != nil || pending != "legacy NaCl pending ciphertext" {
		t.Fatal(pending, e)
	}
	if e = s.db.QueryRow(`SELECT convert_from(data,'UTF8') FROM device_history_chunks WHERE device_id='legacy-device'`).Scan(&history); e != nil || history != "legacy sealed history" {
		t.Fatal(history, e)
	}
}
