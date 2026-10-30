# Handoff: Android signing plan (temporary — delete when done)

This file passes a plan between Claude sessions on two computers. **Delete it
(and commit the deletion) once the plan below is finished.**

We're continuing work on greenies. Read CLAUDE.md first; I'm a beginner, so
explain each step in plain English.

## Background

The Android app on my phone was built on my **work computer** and signed with
that computer's `desktop/android/debug.keystore`. My home laptop has a
different key (creation date Mar 20 2026), so an APK built there fails to
install with "package conflicts with an existing package." v1.2.2 is released
on GitHub but is not yet on my phone.

## Goal

One shared signing key, kept in GitHub, so GitHub's build robot makes the
phone app for every release, whichever computer I use.

## Plan

1. Pull the latest code from GitHub first. This computer's copy is probably
   out of date. Check `git status` before pulling, and don't delete any local
   changes without asking me.
2. Find `desktop/android/debug.keystore` on this computer and confirm it is
   the key that signed the app on my phone. It should *not* have creation date
   Mar 20 2026 (that's the home laptop's key). **Never commit it to Git**, because
   the repo is public.
3. Help me save a backup copy of the key somewhere safe (I use Google Drive).
4. Store the key as a GitHub Actions secret (base64-encoded). The debug key's
   passwords are "android" (already in `desktop/android/build.sh`). Walk me
   through adding it, or use `gh secret set` with my OK.
5. Add an Android build to `.github/workflows/release.yml` that runs on tag
   push. It should build the APK with the same steps as
   `desktop/android/build.sh` (needs build-tools 36.0.0 and
   platforms;android-34 via sdkmanager), sign it with the secret key, and
   attach it to the release.
6. Change the in-app updater (`internal/updater/updater.go`) so that on
   Android (`runtime.GOOS == "android"`) the update button downloads the
   release APK directly and I just tap Install.
7. Before tagging a release, test that the robot-built APK installs over the
   app on my phone without losing data.
8. Delete this HANDOFF.md file and commit the deletion.
