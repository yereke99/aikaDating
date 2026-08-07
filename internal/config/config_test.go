package config

import (
	"testing"
	"time"
)

func validCalls() CallConfig {
	return CallConfig{
		Enabled: true, InviteTimeout: 45 * time.Second, SetupTimeout: time.Minute,
		EventWait: 20 * time.Second, PresenceTimeout: 45 * time.Second,
		STUNURLs: []string{"stun:stun.l.google.com:19302"}, TURNCredentialTTL: time.Hour,
	}
}

func TestCallConfigurationIsValidated(t *testing.T) {
	if err := validateCalls(validCalls()); err != nil {
		t.Fatalf("the default call configuration was rejected: %v", err)
	}
	// A disabled feature is never validated, so an incomplete TURN block cannot stop the process
	// from starting when calls are switched off.
	if err := validateCalls(CallConfig{TURNURLs: []string{"turn:example.org"}}); err != nil {
		t.Fatalf("disabled calls were validated: %v", err)
	}

	cases := map[string]func(*CallConfig){
		"presence must outlast one idle poll": func(c *CallConfig) { c.PresenceTimeout = c.EventWait },
		"an ICE URL must use an ICE scheme":   func(c *CallConfig) { c.STUNURLs = []string{"https://stun.example.org"} },
		"TURN needs a credential":             func(c *CallConfig) { c.TURNURLs = []string{"turn:turn.example.org:3478"} },
		"the invite timeout has bounds":       func(c *CallConfig) { c.InviteTimeout = time.Second },
	}
	for name, break_ := range cases {
		t.Run(name, func(t *testing.T) {
			calls := validCalls()
			break_(&calls)
			if err := validateCalls(calls); err == nil {
				t.Fatalf("%s: accepted an invalid configuration", name)
			}
		})
	}

	// Either credential style satisfies TURN.
	withSecret := validCalls()
	withSecret.TURNURLs = []string{"turns:turn.example.org:5349"}
	withSecret.TURNSecret = "shared"
	if err := validateCalls(withSecret); err != nil {
		t.Fatalf("TURN with a shared secret was rejected: %v", err)
	}
	withPassword := validCalls()
	withPassword.TURNURLs = []string{"turn:turn.example.org:3478"}
	withPassword.TURNUsername, withPassword.TURNPassword = "aika", "secret"
	if err := validateCalls(withPassword); err != nil {
		t.Fatalf("TURN with a static password was rejected: %v", err)
	}
}

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
