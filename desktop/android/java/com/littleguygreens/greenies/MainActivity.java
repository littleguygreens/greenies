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
import android.os.Build;
import android.os.Bundle;
import android.view.Gravity;
import android.view.View;
import android.view.WindowManager;
import android.webkit.WebSettings;
import android.webkit.WebView;
import android.webkit.WebViewClient;
import android.view.ViewGroup;
import android.widget.FrameLayout;
import android.widget.TextView;

import java.io.BufferedReader;
import java.io.InputStreamReader;
import java.net.InetSocketAddress;
import java.net.Socket;

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

    // Collects anything the Go binary prints to its output (including errors).
    // If the server fails to start, we show this text so the user (or
    // developer) can see exactly what went wrong — instead of a generic
    // "could not start" message.
    private String serverOutput = "";

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

        // ── Full-screen layout ──────────────────────────────────────────
        // Modern Android phones have notches, rounded corners, and system
        // bars that eat into the screen. By default, the app content gets
        // squeezed into a smaller "safe area" — leaving black bars on the
        // edges (which is what we saw in the first build).
        //
        // These flags tell Android: "let my app use the ENTIRE screen,
        // including the area behind the status bar and navigation bar.
        // I'll handle the spacing myself." Our web CSS already handles
        // layout, so this just removes the black borders.

        // Make the app draw behind the status bar and navigation bar.
        getWindow().getDecorView().setSystemUiVisibility(
            View.SYSTEM_UI_FLAG_LAYOUT_STABLE
            | View.SYSTEM_UI_FLAG_LAYOUT_FULLSCREEN
            | View.SYSTEM_UI_FLAG_LAYOUT_HIDE_NAVIGATION
        );

        // Make the status bar and navigation bar transparent so the dark
        // background shows through seamlessly.
        getWindow().setStatusBarColor(Color.TRANSPARENT);
        getWindow().setNavigationBarColor(Color.TRANSPARENT);

        // On Android 9+ (API 28+), allow content to render into the
        // display cutout area (the notch). Without this, Android leaves
        // a black bar where the notch is.
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.P) {
            getWindow().getAttributes().layoutInDisplayCutoutMode =
                WindowManager.LayoutParams.LAYOUT_IN_DISPLAY_CUTOUT_MODE_SHORT_EDGES;
        }

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
                    // If the server didn't start in time, show the error
                    // along with whatever the Go binary printed. This is
                    // the key to debugging — without it, we're flying blind.
                    String detail = serverOutput.isEmpty()
                        ? "(no output from server)"
                        : serverOutput;
                    loadingText.setText(
                        "Could not start the Greenies server.\n\n"
                        + detail
                    );
                    loadingText.setTextSize(14);
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
            String nativeDir = getApplicationInfo().nativeLibraryDir;
            String binaryPath = nativeDir + "/libgreenies.so";

            // Record the path we're trying — if the binary doesn't exist
            // or can't execute, this is the first thing we want to see.
            StringBuilder diag = new StringBuilder();
            diag.append("Binary: ").append(binaryPath).append("\n");

            // Check if the file actually exists and is executable.
            java.io.File binFile = new java.io.File(binaryPath);
            diag.append("Exists: ").append(binFile.exists()).append("\n");
            diag.append("Can execute: ").append(binFile.canExecute()).append("\n");
            diag.append("Size: ").append(binFile.length()).append(" bytes\n");

            // List everything in the native library directory so we can
            // see what Android actually extracted from the APK.
            java.io.File nativeDirFile = new java.io.File(nativeDir);
            String[] files = nativeDirFile.list();
            if (files != null) {
                diag.append("Native dir contents: ");
                for (String f : files) {
                    diag.append(f).append(" ");
                }
                diag.append("\n");
            } else {
                diag.append("Native dir is empty or unreadable\n");
            }

            diag.append("HOME: ").append(getFilesDir().getAbsolutePath()).append("\n");
            diag.append("\n");

            // If the binary doesn't exist, no point trying to run it.
            if (!binFile.exists()) {
                serverOutput = diag.toString() + "ERROR: Binary not found!";
                return;
            }

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
            diag.append("Process started OK\n");

            // Read ALL output from the process synchronously in this thread.
            // We do this here (not in a separate thread) because if the
            // binary crashes instantly, a separate reader thread might not
            // have started yet — and we'd see "(no output from server)".
            //
            // This is safe because waitForServer() runs AFTER startGoServer()
            // returns, and both are already on a background thread (not the
            // UI thread). If the server starts successfully, readLine() will
            // block on the last line ("Greenies GUI running at...") and
            // waitForServer() will connect before we need more output.
            //
            // We read in a separate thread but use a shared StringBuilder
            // that's pre-loaded with the diagnostics we already collected.
            final Process proc = serverProcess;
            final StringBuilder sharedDiag = diag;
            new Thread(() -> {
                try {
                    BufferedReader reader = new BufferedReader(
                        new InputStreamReader(proc.getInputStream()));
                    String line;
                    while ((line = reader.readLine()) != null) {
                        sharedDiag.append(line).append("\n");
                        // Keep only the last ~2000 characters to avoid
                        // eating memory if the server is very chatty.
                        if (sharedDiag.length() > 2000) {
                            sharedDiag.delete(0, sharedDiag.length() - 2000);
                        }
                        // Update serverOutput after every line so it's
                        // always current — even if the process crashes
                        // mid-output.
                        serverOutput = sharedDiag.toString();
                    }
                    // Process ended — check exit code.
                    try {
                        int exitCode = proc.waitFor();
                        sharedDiag.append("\nProcess exited with code: ")
                                  .append(exitCode).append("\n");
                    } catch (InterruptedException ignored) {}
                    serverOutput = sharedDiag.toString();
                } catch (Exception e) {
                    sharedDiag.append("\nReader error: ").append(e.getMessage())
                              .append("\n");
                    serverOutput = sharedDiag.toString();
                }
            }).start();

            // Give the reader thread a moment to start before
            // waitForServer() begins polling.
            Thread.sleep(100);

        } catch (Exception e) {
            // If something goes wrong (binary not found, permission denied),
            // capture the error. The waitForServer() method will time out and
            // show this to the user.
            serverOutput = "Failed to start process: " + e.getMessage();
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
                // Try to open a raw TCP connection to the server's port.
                // We use a plain socket instead of HttpURLConnection because
                // Android 9+ blocks cleartext (non-HTTPS) HTTP connections
                // by default. A raw socket just checks "is anything listening
                // on this port?" without triggering HTTP-level restrictions.
                Socket socket = new Socket();
                socket.connect(
                    new InetSocketAddress("127.0.0.1", SERVER_PORT),
                    POLL_INTERVAL_MS
                );
                socket.close();
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
        // Give the output reader thread a moment to collect any crash
        // messages before we display the error.
        try { Thread.sleep(500); } catch (InterruptedException ignored) {}
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

        // Tell the WebView to use wide viewport mode so it respects the
        // viewport meta tag in our HTML. Without this, some Android versions
        // render the page at a narrow or oddly-sized width.
        settings.setUseWideViewPort(true);
        settings.setLoadWithOverviewMode(true);

        // Load the Greenies dashboard.
        webView.loadUrl("http://127.0.0.1:" + SERVER_PORT);

        // Explicitly set the WebView to fill the entire screen.
        // MATCH_PARENT means "be as wide and tall as the parent" — in this
        // case, the parent is the screen itself.
        webView.setLayoutParams(new ViewGroup.LayoutParams(
            ViewGroup.LayoutParams.MATCH_PARENT,
            ViewGroup.LayoutParams.MATCH_PARENT
        ));

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
