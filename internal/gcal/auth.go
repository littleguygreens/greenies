// Package gcal handles all communication with the Google Calendar API.
//
// This file manages authorisation — the process of asking the user to grant
// the program permission to write to their Google Calendar, and then securely
// storing that permission so it doesn't need to be asked again.
//
// How the permission flow works (plain English):
//  1. The first time "greenies plan" runs after Google Calendar is set up,
//     the program opens a browser window and shows a Google sign-in page.
//  2. The user signs in and clicks "Allow".
//  3. Google sends a one-time code back to the program.
//  4. The program exchanges that code for a "token" — a permission slip that
//     lasts a long time (and can be silently refreshed when it expires).
//  5. The token is saved to ~/.greenies/token.json.
//  6. On every future run, the program just loads that file — no browser needed.
//
// This standard approach is called OAuth2 (Open Authorisation, version 2).
// We use golang.org/x/oauth2, the Go team's own library for it, because
// implementing OAuth2 from scratch against raw HTTP is hundreds of lines of
// error-prone code.
package gcal

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// ─────────────────────────────────────────────────────────────────────────────
// Embedded OAuth credentials
// ─────────────────────────────────────────────────────────────────────────────
//
// These are the OAuth client ID and secret for the Greenies Google Cloud
// project. They're embedded directly in the binary so users don't need to
// create their own Google Cloud project or download a credentials.json file —
// the app just works out of the box.
//
// "But aren't secrets supposed to be kept secret?" — Good instinct, but
// Google explicitly treats OAuth client secrets for "installed applications"
// (desktop apps, mobile apps, CLI tools) as NON-CONFIDENTIAL. The reason
// is simple: the secret is baked into the binary, and anyone with the binary
// could extract it anyway. Google knows this and designs the security model
// around it — the real security comes from the user's own Google sign-in,
// not from the client secret.
//
// This is standard practice for open-source tools. For example, rclone (a
// popular file sync tool with millions of users) embeds its Google OAuth
// credentials in its public source code.
//
// If a user wants to use their own Google Cloud project (for example, to get
// their own API quota), there are two ways to override these values:
//
//  1. At build time — anyone compiling Greenies from source can bake their
//     own credentials into their binary with Go's linker flags:
//
//	go build -ldflags "\
//	  -X github.com/littleguygreens/greenies/internal/gcal.embeddedClientID=YOUR_ID \
//	  -X github.com/littleguygreens/greenies/internal/gcal.embeddedClientSecret=YOUR_SECRET"
//
//     The "-X" flag tells the Go linker to replace a variable's value at the
//     moment the binary is assembled — the same mechanism many projects use
//     to stamp a version number. The result is still a single self-contained
//     file, just carrying that person's credentials instead of ours. This is
//     why the two values below are declared with "var" rather than "const":
//     the linker can only overwrite variables.
//
//  2. At run time — place a credentials.json file (downloaded from the
//     Google Cloud console) in ~/.greenies/ and it takes priority over the
//     embedded values. No rebuilding required. See loadConfig below.

var embeddedClientID = "817302381223-g81vtlpcd4l883fvah7et6hnq04i8ftq.apps.googleusercontent.com"
var embeddedClientSecret = "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"

// credentialsPath returns the full path to credentials.json — an optional
// override file. If a user places their own credentials.json here, it takes
// priority over the embedded credentials. Most users will never need this.
func credentialsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".greenies", "credentials.json"), nil
}

// tokenPath returns the full path to token.json — the file where the user's
// long-lived permission slip is stored after the first authorisation.
func tokenPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".greenies", "token.json"), nil
}

// CredentialsExist always returns true because OAuth credentials are
// embedded in the binary. Google features are always available — no setup
// file required.
func CredentialsExist() bool {
	return true
}

// TokenExists returns true if the user has already completed the Google
// sign-in flow and a saved token.json exists. This is separate from
// CredentialsExist — credentials.json is the app's identity (downloaded
// from Google Cloud Console), while token.json is the user's sign-in
// (created after they approve in the browser).
func TokenExists() bool {
	path, err := tokenPath()
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}

// loadConfig builds an OAuth2 configuration — the settings that tell Google
// which app is asking for permission and what permissions it needs.
//
// By default, this uses the credentials embedded in the binary (see the
// constants above). If the user has placed their own credentials.json file
// in ~/.greenies/, that file takes priority — this lets power users use
// their own Google Cloud project with its own API quota.
func loadConfig() (*oauth2.Config, error) {
	// Check for a local credentials.json override file first.
	credPath, err := credentialsPath()
	if err == nil {
		if b, readErr := os.ReadFile(credPath); readErr == nil {
			// The user has their own credentials file — use it.
			config, parseErr := google.ConfigFromJSON(b,
				"https://www.googleapis.com/auth/calendar",
				"https://www.googleapis.com/auth/tasks",
				"https://www.googleapis.com/auth/spreadsheets",
			)
			if parseErr == nil {
				return config, nil
			}
			// If the file exists but can't be parsed, fall through to
			// embedded credentials rather than failing completely.
			fmt.Printf("Warning: could not parse credentials.json, using built-in credentials instead\n")
		}
	}

	// Use the embedded credentials. We build the OAuth2 config directly
	// instead of parsing a JSON file — same result, no file needed.
	//
	// The scopes are the specific permissions we request:
	//   - calendar:     read and write Google Calendar events
	//   - tasks:        read and write Google Tasks (for daily task entries)
	//   - spreadsheets: read and write Google Sheets (for the crop library)
	//
	// IMPORTANT: if you add or remove a scope here, delete ~/.greenies/token.json
	// and run "greenies sync" again. The browser login must be repeated any time
	// the list of permissions changes, because the saved token only covers the
	// scopes that were active when it was created.
	config := &oauth2.Config{
		ClientID:     embeddedClientID,
		ClientSecret: embeddedClientSecret,
		Endpoint:     google.Endpoint,
		Scopes: []string{
			"https://www.googleapis.com/auth/calendar",
			"https://www.googleapis.com/auth/tasks",
			"https://www.googleapis.com/auth/spreadsheets",
		},
		// RedirectURL is set dynamically in runBrowserAuthFlow when the
		// temporary localhost server picks an available port.
	}

	return config, nil
}

// loadSavedToken tries to read a previously saved token from token.json.
// Returns (nil, nil) if the file simply doesn't exist yet — that just means
// the user hasn't authorised yet, so we need to run the browser flow.
func loadSavedToken() (*oauth2.Token, error) {
	tokPath, err := tokenPath()
	if err != nil {
		return nil, err
	}

	f, err := os.Open(tokPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // no token yet — first-time setup needed
		}
		return nil, fmt.Errorf("could not open token.json: %w", err)
	}
	defer f.Close()

	var tok oauth2.Token
	if err := json.NewDecoder(f).Decode(&tok); err != nil {
		return nil, fmt.Errorf("could not read token.json: %w", err)
	}

	return &tok, nil
}

// saveToken writes the token to token.json with restricted file permissions
// (0600 = only the current user can read or write it — no one else on the
// machine can see the file contents). This keeps the permission slip private.
func saveToken(tok *oauth2.Token) error {
	tokPath, err := tokenPath()
	if err != nil {
		return err
	}

	// os.O_RDWR|os.O_CREATE|os.O_TRUNC = open for read/write, create if
	// missing, and wipe any existing content before writing (so old tokens
	// are completely replaced, not left behind).
	f, err := os.OpenFile(tokPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("could not save token.json: %w", err)
	}
	defer f.Close()

	return json.NewEncoder(f).Encode(tok)
}

// AuthorizeClient returns an HTTP client that automatically attaches the
// user's Google Calendar permission to every request it makes.
//
// On first run (CLI): opens the browser, asks the user to approve, saves the token.
// On first run (GUI): the GUI uses BuildAuthURL / HandleAuthCallback instead.
// On subsequent runs: loads the saved token silently. If the token has
// expired, the oauth2 library refreshes it automatically using the internet —
// no user action needed.
func AuthorizeClient(ctx context.Context) (*http.Client, error) {
	config, err := loadConfig()
	if err != nil {
		return nil, err
	}

	tok, err := loadSavedToken()
	if err != nil {
		return nil, err
	}

	if tok == nil {
		// No saved token found — run the one-time browser authorisation flow.
		tok, err = runBrowserAuthFlow(ctx, config)
		if err != nil {
			return nil, err
		}

		// Save for next time. Non-fatal if saving fails — the session still
		// works, the user will just be asked again next run.
		if saveErr := saveToken(tok); saveErr != nil {
			fmt.Printf("Warning: authorisation worked but token could not be saved: %v\n", saveErr)
		}
	}

	// config.Client wraps a standard HTTP client with automatic token
	// attachment and refresh — every API call goes through it transparently.
	return config.Client(ctx, tok), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// GUI-friendly auth flow (redirect-based)
// ─────────────────────────────────────────────────────────────────────────────
//
// The CLI auth flow (runBrowserAuthFlow) launches an external browser and
// spins up a temporary server on a random port. This works on desktop but
// FAILS on Android because:
//   - There's no xdg-open to launch a browser
//   - The Go process is sandboxed and can't start other apps
//   - The WebView IS the browser — we need to redirect it, not open a new one
//
// The GUI flow works differently:
//   1. BuildAuthURL builds the Google sign-in URL with a callback pointing
//      back to the main server (e.g. http://127.0.0.1:8080/auth/callback)
//   2. The frontend redirects the WebView/browser to that URL
//   3. The user signs in on Google's page (inside the same window)
//   4. Google redirects back to /auth/callback with a one-time code
//   5. HandleAuthCallback exchanges the code for a token and saves it
//   6. The handler redirects to /sync — the user sees "Sync Now"
//
// This works everywhere: Android WebView, desktop browser, Chromium --app.

// BuildAuthURL creates a Google sign-in URL that redirects back to the
// given callback URL after the user approves. The callback URL should be
// a route on the main Greenies server (e.g. http://127.0.0.1:8080/auth/callback).
//
// Returns the URL the browser/WebView should navigate to.
func BuildAuthURL(callbackURL string) (string, error) {
	config, err := loadConfig()
	if err != nil {
		return "", err
	}

	config.RedirectURL = callbackURL

	// AccessTypeOffline means Google gives us a refresh token too, so the
	// program can silently renew the permission without the browser again.
	authURL := config.AuthCodeURL("state", oauth2.AccessTypeOffline)
	return authURL, nil
}

// HandleAuthCallback takes the one-time code from Google's redirect,
// exchanges it for a long-lived token, and saves the token to disk.
// This is called by the GUI's /auth/callback route handler.
//
// The callbackURL must match exactly what was passed to BuildAuthURL —
// Google verifies they're the same as a security check.
func HandleAuthCallback(ctx context.Context, code string, callbackURL string) error {
	config, err := loadConfig()
	if err != nil {
		return err
	}

	config.RedirectURL = callbackURL

	// Exchange the one-time code for a long-lived token.
	tok, err := config.Exchange(ctx, code)
	if err != nil {
		return fmt.Errorf("could not exchange authorisation code for token: %w", err)
	}

	// Save the token so the user never has to sign in again.
	if err := saveToken(tok); err != nil {
		return fmt.Errorf("signed in successfully but could not save token: %w", err)
	}

	return nil
}

// runBrowserAuthFlow handles the one-time setup where the user grants
// permission via their browser. It works like this:
//
//  1. Start a tiny temporary web server on a random unused port on localhost.
//  2. Tell Google to redirect back to that server after the user approves.
//  3. Open the user's browser to Google's authorisation page.
//  4. Wait for Google's redirect to arrive (which carries a one-time code).
//  5. Exchange the code for a long-lived token and return it.
//
// The temporary server is shut down as soon as the code is received.
func runBrowserAuthFlow(ctx context.Context, config *oauth2.Config) (*oauth2.Token, error) {
	// Ask the OS for an available port by listening on port 0.
	// The OS picks a free port and tells us what it chose.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("could not start local auth server: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port

	// Set the redirect URL to our local server. Google will send the
	// one-time code here after the user clicks Allow.
	config.RedirectURL = fmt.Sprintf("http://127.0.0.1:%d/callback", port)

	// Channels are Go's way of sending values between concurrent tasks.
	// codeCh receives the authorisation code when the redirect arrives.
	// errCh receives any error from the redirect handler.
	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	// Use a local HTTP mux (router) rather than the global one, so this
	// handler doesn't interfere with anything else in the program.
	mux := http.NewServeMux()
	server := &http.Server{Handler: mux}

	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		if code == "" {
			errCh <- fmt.Errorf("no authorisation code received from Google")
			fmt.Fprintf(w, "Something went wrong. Please close this tab and try again.")
			return
		}
		// Send the code back to the waiting main flow.
		codeCh <- code
		// Show a friendly message in the browser — the user can close the tab.
		fmt.Fprintf(w, "<h2>Authorisation complete!</h2><p>You can close this browser tab and return to greenies.</p>")
	})

	// Start the server in the background — it handles one request then stops.
	go func() { server.Serve(listener) }()

	// Build the URL the user needs to visit to grant permission.
	// AccessTypeOffline means Google gives us a refresh token too, so the
	// program can silently renew the permission without the browser again.
	authURL := config.AuthCodeURL("state", oauth2.AccessTypeOffline)

	fmt.Println("\nOpening your browser to authorise Greenies with Google Calendar...")
	fmt.Println("If the browser doesn't open automatically, visit this URL:")
	fmt.Printf("\n  %s\n\n", authURL)

	// Open the URL in the user's default browser. The command varies by
	// operating system, so we detect the OS and use the right one:
	//   macOS   → "open"
	//   Windows → "rundll32 url.dll,FileProtocolHandler"
	//   Linux   → "xdg-open" (the standard for desktop Linux)
	// We use Start() (not Run()) so the program doesn't wait for the browser to close.
	// If none of these commands work, the user can still copy-paste the URL printed above.
	switch runtime.GOOS {
	case "darwin":
		exec.Command("open", authURL).Start()
	case "windows":
		exec.Command("rundll32", "url.dll,FileProtocolHandler", authURL).Start()
	default: // Linux and anything else
		exec.Command("xdg-open", authURL).Start()
	}

	// Wait for the code, an error, or a 5-minute timeout — whichever comes first.
	var code string
	select {
	case code = <-codeCh:
		// The user approved — continue below.
	case err := <-errCh:
		server.Close()
		return nil, err
	case <-time.After(5 * time.Minute):
		server.Close()
		return nil, fmt.Errorf("timed out waiting for browser authorisation (5 minutes)")
	}

	// Shut down the temporary server — we only needed it for the one redirect.
	server.Close()

	// Exchange the one-time code for a long-lived token.
	// The token contains an access token (expires in ~1 hour) and a refresh
	// token (used automatically to get a new access token when needed).
	tok, err := config.Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("could not exchange authorisation code for token: %w", err)
	}

	fmt.Println("Authorisation successful! Your access has been saved to ~/.greenies/token.json.")
	fmt.Println("You won't be asked to authorise again.")
	fmt.Println()

	return tok, nil
}
