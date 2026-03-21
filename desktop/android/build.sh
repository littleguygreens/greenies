#!/bin/bash
# ─────────────────────────────────────────────────────────────────────────────
# build.sh — Build the Greenies Android APK
# ─────────────────────────────────────────────────────────────────────────────
#
# This script takes the Go source code and the Java wrapper, and produces a
# single greenies.apk file that you can install on any Android phone.
#
# What it does, step by step:
#   1. Cross-compiles the Go binary for Android's ARM64 processor
#   2. Compiles the Android resources (icon, app name)
#   3. Compiles the Java code (MainActivity)
#   4. Converts Java bytecode to Android's DEX format
#   5. Packages everything into an APK
#   6. Signs the APK (Android won't install unsigned apps)
#
# Prerequisites (one-time setup):
#   - Go (already installed)
#   - Java Development Kit: sudo apt install default-jdk
#   - Android SDK command-line tools (see setup instructions in README)
#   - Set ANDROID_HOME to your SDK location (default: ~/android-sdk)
#
# Usage:
#   cd desktop/android
#   ./build.sh
#
# Output:
#   desktop/android/greenies.apk
# ─────────────────────────────────────────────────────────────────────────────

# "set -e" means "stop immediately if any command fails." Without this, the
# script would keep going after an error, producing a broken APK.
set -e

# ── Configuration ────────────────────────────────────────────────────────────

# ANDROID_HOME is where the Android SDK lives on your computer.
# If you haven't set this environment variable, it defaults to ~/android-sdk.
ANDROID_HOME="${ANDROID_HOME:-$HOME/android-sdk}"

# The build tools folder contains all the Android-specific tools we need
# (aapt2, d8, zipalign, apksigner). The version number here must match
# what you installed with sdkmanager.
BUILD_TOOLS="$ANDROID_HOME/build-tools/36.0.0"

# The Android platform JAR — this contains the Android API classes that
# our Java code uses (Activity, WebView, etc.). Think of it as a reference
# book that the Java compiler reads to understand Android-specific code.
PLATFORM="$ANDROID_HOME/platforms/android-34/android.jar"

# SCRIPT_DIR is the folder where this build.sh script lives. We use it
# to find all the other files (AndroidManifest.xml, Java source, etc.)
# regardless of where you run the script from.
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

# OUT is a temporary build folder where all intermediate files go.
# It gets deleted and recreated on each build so you always start fresh.
OUT="$SCRIPT_DIR/build"

# ── Preflight checks ────────────────────────────────────────────────────────
# Make sure all the tools we need are actually installed before we start.

echo "==> Checking prerequisites..."

if ! command -v go &> /dev/null; then
    echo "ERROR: Go is not installed. Install it from https://go.dev/dl/"
    exit 1
fi

if ! command -v javac &> /dev/null; then
    echo "ERROR: Java compiler (javac) not found."
    echo "       Install it with: sudo apt install default-jdk"
    exit 1
fi

if [ ! -d "$BUILD_TOOLS" ]; then
    echo "ERROR: Android build tools not found at $BUILD_TOOLS"
    echo "       See the setup instructions in the README."
    echo ""
    echo "       Quick setup:"
    echo "         mkdir -p ~/android-sdk/cmdline-tools"
    echo "         cd ~/android-sdk/cmdline-tools"
    echo "         wget https://dl.google.com/android/repository/commandlinetools-linux-11076708_latest.zip"
    echo "         unzip commandlinetools-linux-*.zip"
    echo "         mv cmdline-tools latest"
    echo "         export ANDROID_HOME=~/android-sdk"
    echo "         export PATH=\$PATH:\$ANDROID_HOME/cmdline-tools/latest/bin"
    echo "         sdkmanager 'build-tools;36.0.0' 'platforms;android-34'"
    exit 1
fi

if [ ! -f "$PLATFORM" ]; then
    echo "ERROR: Android platform (API 34) not found at $PLATFORM"
    echo "       Install it with: sdkmanager 'platforms;android-34'"
    exit 1
fi

# ── Clean previous build ────────────────────────────────────────────────────

rm -rf "$OUT"
mkdir -p "$OUT/classes"

# ── Step 1: Cross-compile Go for Android ─────────────────────────────────────
#
# Go can compile for almost any operating system and processor from any
# computer. This is called "cross-compilation" — building on Linux x86 but
# targeting Android ARM64.
#
# CGO_ENABLED=0 means "don't use C code" — keeps the binary self-contained.
# GOOS=android tells Go the target operating system.
# GOARCH=arm64 tells Go the target processor (almost all modern phones).
#
# The output goes into lib/arm64-v8a/ and is named libgreenies.so because
# Android only extracts and gives execute permission to .so files in the
# lib/ folder. It's not really a shared library — it's our full Go program.
# Android doesn't check; it just extracts anything ending in .so.

echo "==> Cross-compiling Go binary for android/arm64..."
mkdir -p "$OUT/lib/arm64-v8a"
cd "$SCRIPT_DIR/../.."
CGO_ENABLED=0 GOOS=android GOARCH=arm64 go build -o "$OUT/lib/arm64-v8a/libgreenies.so" .
cd "$SCRIPT_DIR"

echo "    Binary size: $(du -h "$OUT/lib/arm64-v8a/libgreenies.so" | cut -f1)"

# ── Step 2: Compile Android resources ────────────────────────────────────────
#
# "Resources" in Android are things like the app name, icon, colours, and
# layouts. They're defined in XML files under res/. The aapt2 tool compiles
# them into a binary format that Android can read quickly.

echo "==> Compiling resources..."
"$BUILD_TOOLS/aapt2" compile --dir res -o "$OUT/res.zip"

# ── Step 3: Link resources into a base APK ───────────────────────────────────
#
# "Linking" combines the compiled resources with the AndroidManifest.xml to
# produce a skeleton APK. This APK has the icon, app name, and metadata but
# no actual code yet. The --java flag generates an R.java file (a lookup
# table for resources) but we don't use it since our Java code doesn't
# reference resources by ID.

echo "==> Linking resources with manifest..."
"$BUILD_TOOLS/aapt2" link \
    -o "$OUT/base.apk" \
    -I "$PLATFORM" \
    --manifest AndroidManifest.xml \
    "$OUT/res.zip"

# ── Step 4: Compile Java ────────────────────────────────────────────────────
#
# javac is the Java compiler — it turns human-readable .java files into
# .class files (Java bytecode — instructions the Java runtime understands).
#
# -source 11 -target 11 tells javac to use Java 11 syntax. This is the
# version Android's tools expect.
#
# -classpath points to the Android platform JAR so javac knows about
# Android-specific classes like Activity, WebView, Bundle, etc.

echo "==> Compiling Java source..."
javac -source 11 -target 11 \
    -classpath "$PLATFORM" \
    -d "$OUT/classes" \
    java/com/littleguygreens/greenies/MainActivity.java

# ── Step 5: Convert to DEX ──────────────────────────────────────────────────
#
# Android phones don't run Java bytecode directly — they use their own format
# called DEX (Dalvik Executable). The d8 tool converts .class files to .dex.
#
# Think of it like translating a book: javac wrote it in "Java dialect," and
# d8 translates it to "Android dialect."

echo "==> Converting Java bytecode to Android DEX format..."
"$BUILD_TOOLS/d8" \
    --release \
    --lib "$PLATFORM" \
    --output "$OUT" \
    "$OUT/classes/com/littleguygreens/greenies/MainActivity.class"

# ── Step 6: Assemble the final APK ──────────────────────────────────────────
#
# An APK is just a ZIP file with a specific structure. We start from the
# base APK (which has resources + manifest) and add:
#   - classes.dex (the compiled Java code)
#   - lib/arm64-v8a/libgreenies.so (the Go binary)

echo "==> Assembling APK..."
cd "$OUT"
cp base.apk greenies-unsigned.apk

# Add the DEX file (Java code in Android format).
# -j means "don't include directory paths" — just add the file at the root.
zip -j greenies-unsigned.apk classes.dex

# Add the Go binary in the native library directory structure.
# Android's package manager looks for lib/<architecture>/*.so and extracts
# them with execute permission on install.
zip -r greenies-unsigned.apk lib/
cd "$SCRIPT_DIR"

# ── Step 7: Align ───────────────────────────────────────────────────────────
#
# zipalign adjusts the byte alignment of files inside the APK so Android
# can memory-map them efficiently. This is a required step — Android may
# refuse to install an unaligned APK on newer versions.
#
# The "4" means "align to 4-byte boundaries."

echo "==> Aligning APK..."
"$BUILD_TOOLS/zipalign" -f 4 "$OUT/greenies-unsigned.apk" "$OUT/greenies-aligned.apk"

# ── Step 8: Sign ─────────────────────────────────────────────────────────────
#
# Android refuses to install unsigned APKs. The signature proves the APK
# hasn't been tampered with since it was built.
#
# We create a "debug keystore" — a self-signed certificate that's fine for
# personal use and sideloading (installing directly onto your phone). If you
# ever want to publish on the Google Play Store, you'd need a proper release
# key. For now, this debug key is all you need.
#
# The keystore is saved as debug.keystore in the android/ folder so it
# persists between builds. Using the same key means Android recognises
# updates as coming from the same developer and lets you install over the
# old version without losing data.

echo "==> Signing APK..."
KEYSTORE="$SCRIPT_DIR/debug.keystore"
if [ ! -f "$KEYSTORE" ]; then
    echo "    Creating debug signing key (first time only)..."
    keytool -genkeypair -v \
        -keystore "$KEYSTORE" \
        -alias greenies \
        -keyalg RSA -keysize 2048 \
        -validity 10000 \
        -storepass android -keypass android \
        -dname "CN=Greenies,O=LittleGuyGreens" \
        2>&1 | tail -1
fi

"$BUILD_TOOLS/apksigner" sign \
    --ks "$KEYSTORE" \
    --ks-key-alias greenies \
    --ks-pass pass:android \
    --key-pass pass:android \
    "$OUT/greenies-aligned.apk"

# ── Step 9: Move the final APK to a convenient location ─────────────────────

mv "$OUT/greenies-aligned.apk" "$SCRIPT_DIR/greenies.apk"

echo ""
echo "==> Done! APK built successfully."
echo ""
echo "    Output: $SCRIPT_DIR/greenies.apk"
echo "    Size:   $(du -h "$SCRIPT_DIR/greenies.apk" | cut -f1)"
echo ""
echo "    To install on your phone:"
echo "      Option A (USB cable):  adb install $SCRIPT_DIR/greenies.apk"
echo "      Option B (transfer):   Copy greenies.apk to your phone and tap it"
echo "                              (enable 'Install from unknown sources' in Settings)"
