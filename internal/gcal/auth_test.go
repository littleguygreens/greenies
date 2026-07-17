package gcal

// This is an automated test (run with "go test ./...") for the OAuth "state"
// check — the defence that stops an attacker from tricking Greenies into
// saving a sign-in token for THEIR Google account (see the explanation above
// BuildAuthURL in auth.go).
//
// The test never talks to the real Google. It uses the same credentials.json
// override a power user would: a fake credentials file whose token address
// points at a tiny pretend-Google server running inside the test. That lets
// us walk the entire sign-in callback path — state check, code exchange,
// token save — completely offline.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupFakeGoogle points the auth code at a temporary home directory with a
// credentials.json whose token_uri is a local pretend-Google. Returns nothing;
// cleanup is automatic via t.Setenv / t.TempDir / t.Cleanup.
func setupFakeGoogle(t *testing.T) {
	t.Helper()

	// A pretend Google token endpoint: accepts any code and hands back a
	// well-formed token, so a callback that passes the state check succeeds.
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"test-access","token_type":"Bearer","refresh_token":"test-refresh","expires_in":3600}`)
	}))
	t.Cleanup(fake.Close)

	// A throwaway home directory so the test never touches the real
	// ~/.greenies. os.UserHomeDir reads $HOME on Linux.
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".greenies")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}

	creds := fmt.Sprintf(`{"installed":{"client_id":"test-id","client_secret":"test-secret",`+
		`"auth_uri":"%s/auth","token_uri":"%s/token","redirect_uris":["http://localhost"]}}`,
		fake.URL, fake.URL)
	if err := os.WriteFile(filepath.Join(dir, "credentials.json"), []byte(creds), 0600); err != nil {
		t.Fatal(err)
	}
}

// stateFromAuthURL extracts the state parameter from a sign-in URL.
func stateFromAuthURL(t *testing.T, authURL string) string {
	t.Helper()
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("could not parse auth URL: %v", err)
	}
	return u.Query().Get("state")
}

func TestAuthStateIsRandomAndVerified(t *testing.T) {
	setupFakeGoogle(t)
	const callback = "http://127.0.0.1:8080/auth/callback"
	ctx := context.Background()

	// ── The state must be random, not a fixed word ───────────────────────
	url1, err := BuildAuthURL(callback)
	if err != nil {
		t.Fatal(err)
	}
	state1 := stateFromAuthURL(t, url1)
	if state1 == "state" || len(state1) != 16 {
		t.Fatalf("state should be 16 random hex chars, got %q", state1)
	}

	url2, err := BuildAuthURL(callback)
	if err != nil {
		t.Fatal(err)
	}
	state2 := stateFromAuthURL(t, url2)
	if state1 == state2 {
		t.Fatal("two sign-ins produced the same state — it is not random")
	}

	// ── A callback with the wrong state must be rejected ─────────────────
	err = HandleAuthCallback(ctx, "any-code", "wrong-state", callback)
	if err == nil || !strings.Contains(err.Error(), "sign-in rejected") {
		t.Fatalf("wrong state should be rejected, got: %v", err)
	}

	// ── A callback with the correct state must succeed ───────────────────
	// (The rejection above consumed the pending state, so start fresh.)
	url3, err := BuildAuthURL(callback)
	if err != nil {
		t.Fatal(err)
	}
	state3 := stateFromAuthURL(t, url3)
	if err := HandleAuthCallback(ctx, "good-code", state3, callback); err != nil {
		t.Fatalf("correct state should be accepted, got: %v", err)
	}
	if !TokenExists() {
		t.Fatal("token.json was not saved after a successful callback")
	}

	// ── The same state must never work twice ─────────────────────────────
	err = HandleAuthCallback(ctx, "good-code", state3, callback)
	if err == nil || !strings.Contains(err.Error(), "sign-in rejected") {
		t.Fatalf("reused state should be rejected, got: %v", err)
	}
}
