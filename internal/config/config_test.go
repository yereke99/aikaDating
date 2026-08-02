package config

import "testing"

func TestAdminAllowlist(t *testing.T) {
	ids, err := parseIDs("123, 456")
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{AdminTelegramIDs: ids}
	if !cfg.IsAdmin(123) || !cfg.IsAdmin(456) {
		t.Fatal("configured admins were not recognized")
	}
	if cfg.IsAdmin(789) {
		t.Fatal("ordinary user was recognized as admin")
	}
}
