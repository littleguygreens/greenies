package com.littleguygreens.greenies;

// ─────────────────────────────────────────────────────────────────────────────
// MainActivity.java — the Android wrapper for Greenies
// ─────────────────────────────────────────────────────────────────────────────
//
// This is a very thin shell. The real app is the Go binary — it runs a web
// server on localhost:8080 and serves the full Greenies GUI. This Java code
// does three things:
//
//   1. Starts the Go server as a background process
//   2. Waits until the server is ready (polls localhost:8080)
//   3. Opens a WebView (a built-in browser window) pointed at the server
//
// From the user's perspective, they tap the app icon and see Greenies.
// They never know there's a Go server running behind the scenes.
// ─────────────────────────────────────────────────────────────────────────────

import android.app.Activity;
import android.graphics.Color;
import android.os.Bundle;
import android.view.Gravity;
import android.view.View;
import android.webkit.WebSettings;
import android.webkit.WebView;
import android.webkit.WebViewClient;
import android.widget.FrameLayout;
import android.widget.TextView;

import java.net.HttpURLConnection;
import java.net.URL;

/**
 * MainActivity is the entry point of the Android app — the first thing that
 * runs when you tap the Greenies icon. An "Activity" in Android is one screen
 * of the app. We only have one: a full-screen WebView showing the GUI.
 */
public class MainActivity extends Activity {

    // A reference to the Go server process so we can stop it later.
    // "Process" here means a running program — like how your laptop can run
    // multiple programs at once, the phone runs our Go server alongside
    // this Java code.
    private Process serverProcess;

    // The WebView — think of it as a browser window without the address bar.
    // It renders the same HTML/CSS/JavaScript as Chrome would.
    private WebView webView;

    // The port the Go server listens on. Must match what the Go code uses.
    private static final int SERVER_PORT = 8080;

    // How long to wait for the server to start (in milliseconds).
    // 15 seconds is generous — the Go server usually starts in under a second,
    // but on older phones it might take a moment.
    private static final int MAX_WAIT_MS = 15000;

    // How often to check if the server is ready (in milliseconds).
    private static final int POLL_INTERVAL_MS = 200;

    /**
     * onCreate is called by Android when the app launches. Think of it as
     * the "main" function — the starting point of everything.
     */
    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);

        // Show a simple "Starting..." message while the server boots.
        // This replaces a blank screen during the 0.5–2 seconds it takes
        // for the Go binary to start accepting connections.
        TextView loadingText = new TextView(this);
        loadingText.setText("Starting Greenies...");
        loadingText.setTextColor(Color.parseColor("#8bc34a")); // greenies green
        loadingText.setTextSize(20);
        loadingText.setGravity(Gravity.CENTER);

        FrameLayout container = new FrameLayout(this);
        container.setBackgroundColor(Color.parseColor("#1a1a1a")); // dark background
        container.addView(loadingText);
        setContentView(container);

        // Start the Go server in a background thread so the UI doesn't freeze.
        // A "thread" is like a separate lane of traffic — the server starts up
        // in one lane while the loading screen shows in another.
        new Thread(() -> {
            startGoServer();
            boolean ready = waitForServer();

            // Once the server is ready, switch to the WebView on the UI thread.
            // Android requires all screen updates to happen on the "UI thread"
            // (the main lane). runOnUiThread says "do this on the main lane."
            runOnUiThread(() -> {
                if (ready) {
                    setupWebView();
                } else {
                    // If the server didn't start in time, show an error.
                    loadingText.setText(
                        "Could not start the Greenies server.\n\n"
                        + "Try closing and reopening the app."
                    );
                    loadingText.setTextColor(Color.parseColor("#ff5252"));
                }
            });
        }).start();
    }

    /**
     * Starts the Go binary as a background process.
     *
     * The Go binary is packaged inside the APK as lib/arm64-v8a/libgreenies.so.
     * (It's not really a shared library — it's a standalone executable. But
     * Android only extracts and gives execute permission to files in the lib/
     * folder that end in .so, so we use that naming trick.)
     *
     * Android's package manager extracts it to the app's "nativeLibraryDir"
     * (a folder with execute permission) when the app is installed.
     */
    private void startGoServer() {
        try {
            // Find the Go binary. Android puts extracted .so files in a
            // special directory that has execute permission.
            String binaryPath = getApplicationInfo().nativeLibraryDir
                                + "/libgreenies.so";

            // ProcessBuilder creates and starts a new process (a running
            // program). We pass "gui" as the argument — same as typing
            // "greenies gui" in the terminal.
            ProcessBuilder pb = new ProcessBuilder(binaryPath, "gui");

            // Set environment variables that the Go binary reads:
            //
            // HOME — tells Go's os.UserHomeDir() where to store data.
            //   On Android, getFilesDir() returns the app's private storage
            //   directory (something like /data/data/com.littleguygreens.greenies/files).
            //   The Go code creates ~/.greenies/ inside this, so all data files
            //   (tasks.json, crops.csv, etc.) live safely inside the app.
            //
            // GREENIES_NO_HEARTBEAT — tells the Go server not to auto-shutdown
            //   when heartbeat pings stop. On desktop, the server shuts down
            //   when you close the browser tab. On Android, WE manage the
            //   lifecycle — we kill the server in onDestroy() when the app closes.
            pb.environment().put("HOME", getFilesDir().getAbsolutePath());
            pb.environment().put("GREENIES_NO_HEARTBEAT", "1");

            // Merge the server's error output into its regular output.
            // This makes it easier to read logs if we ever need to debug.
            pb.redirectErrorStream(true);

            // Start the process. From this moment, the Go server is running
            // in the background and will start listening on localhost:8080.
            serverProcess = pb.start();

        } catch (Exception e) {
            // If something goes wrong (binary not found, permission denied),
            // print the error. The waitForServer() method will time out and
            // show the error message to the user.
            e.printStackTrace();
        }
    }

    /**
     * Polls localhost:8080 until the Go server responds, or gives up after
     * MAX_WAIT_MS milliseconds.
     *
     * Returns true if the server is ready, false if it timed out.
     */
    private boolean waitForServer() {
        int waited = 0;
        while (waited < MAX_WAIT_MS) {
            try {
                // Try to connect to the server. If it's not ready yet,
                // this will throw an exception and we'll try again.
                URL url = new URL("http://127.0.0.1:" + SERVER_PORT);
                HttpURLConnection conn = (HttpURLConnection) url.openConnection();
                conn.setConnectTimeout(POLL_INTERVAL_MS);
                conn.setReadTimeout(POLL_INTERVAL_MS);
                conn.connect();
                conn.disconnect();
                return true; // Server responded — it's ready!
            } catch (Exception e) {
                // Server not ready yet. Wait a bit and try again.
                try {
                    Thread.sleep(POLL_INTERVAL_MS);
                } catch (InterruptedException ie) {
                    return false;
                }
                waited += POLL_INTERVAL_MS;
            }
        }
        return false; // Timed out — server never responded.
    }

    /**
     * Creates a full-screen WebView and loads the Greenies GUI.
     *
     * A WebView is a browser engine built into every Android phone. It
     * renders HTML, CSS, and JavaScript just like Chrome — but without
     * the address bar, tabs, or bookmarks. The user sees a native-looking
     * app, not a website.
     */
    private void setupWebView() {
        webView = new WebView(this);

        // Configure the WebView's capabilities.
        WebSettings settings = webView.getSettings();

        // JavaScript must be enabled — the GUI uses htmx (a small JavaScript
        // library) for all its interactive features (form submissions, live
        // updates, collapsible sections).
        settings.setJavaScriptEnabled(true);

        // DOM storage (localStorage) must be enabled — the GUI uses it to
        // remember things like which nav sections are collapsed.
        settings.setDomStorageEnabled(true);

        // Keep all navigation inside the WebView. Without this, clicking a
        // link would open the phone's default browser app instead of staying
        // inside Greenies.
        webView.setWebViewClient(new WebViewClient());

        // Load the Greenies dashboard.
        webView.loadUrl("http://127.0.0.1:" + SERVER_PORT);

        // Replace the loading screen with the WebView.
        setContentView(webView);
    }

    /**
     * onDestroy is called by Android when the app is being closed (swiped
     * away from the recent apps list, or the system needs to free memory).
     *
     * We use it to kill the Go server process. Without this, the server
     * would keep running in the background wasting battery and memory.
     */
    @Override
    protected void onDestroy() {
        super.onDestroy();
        if (serverProcess != null) {
            serverProcess.destroy();
        }
    }

    /**
     * Handle the phone's back button (or back gesture).
     *
     * By default, pressing back would close the entire app. Instead, we
     * check if the WebView has navigation history (like pressing back in
     * a browser). If it does, go back one page. If it doesn't (we're on
     * the dashboard), let Android handle it normally (close the app).
     */
    @Override
    public void onBackPressed() {
        if (webView != null && webView.canGoBack()) {
            webView.goBack();
        } else {
            super.onBackPressed();
        }
    }
}
