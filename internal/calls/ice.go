package calls

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"strconv"
	"strings"
	"time"

	"aika/internal/config"
)

// ICEServer is one entry of RTCPeerConnection's iceServers array, in exactly the shape the browser
// expects.
type ICEServer struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}

// ICEServers builds the list handed to one authenticated user.
//
// STUN is enough for the common case: both peers discover their public address and connect
// directly, which is the lowest-latency path and the one this feature is built around. TURN is the
// fallback for the networks where that is impossible — symmetric NAT, carrier-grade NAT, some
// corporate and hotel Wi-Fi — and it is listed after STUN so ICE only falls back to a relay when
// no direct candidate pair works.
//
// The credential is minted per request. With TURN_STATIC_AUTH_SECRET the server issues a
// short-lived username/password pair (coturn's `use-auth-secret` REST flow) that expires on its
// own, so nothing long-lived is ever exposed to a browser and the secret itself never leaves the
// server.
func ICEServers(cfg config.CallConfig, userID string, now time.Time) []ICEServer {
	servers := make([]ICEServer, 0, 2)
	if len(cfg.STUNURLs) > 0 {
		servers = append(servers, ICEServer{URLs: cfg.STUNURLs})
	}
	if len(cfg.TURNURLs) == 0 {
		return servers
	}
	turn := ICEServer{URLs: cfg.TURNURLs, Username: cfg.TURNUsername, Credential: cfg.TURNPassword}
	if cfg.TURNSecret != "" {
		turn.Username, turn.Credential = turnCredential(cfg.TURNSecret, userID, now.Add(cfg.TURNCredentialTTL))
	}
	if turn.Username == "" || turn.Credential == "" {
		return servers
	}
	return append(servers, turn)
}

// turnCredential implements the long-term credential REST API coturn expects: the username is the
// expiry timestamp joined to an opaque identifier, and the password is its HMAC under the shared
// secret.
func turnCredential(secret, userID string, expiry time.Time) (string, string) {
	username := strconv.FormatInt(expiry.Unix(), 10) + ":" + sanitizeTURNUser(userID)
	mac := hmac.New(sha1.New, []byte(secret))
	_, _ = mac.Write([]byte(username))
	return username, base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// sanitizeTURNUser keeps the identifier inside the character set the TURN username field allows.
func sanitizeTURNUser(userID string) string {
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		default:
			return -1
		}
	}, userID)
	if cleaned == "" {
		return "aika"
	}
	return cleaned
}
