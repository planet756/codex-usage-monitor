# Changelog

## 0.1.1

- Share the Desktop connection, source verification, reader scheduling and accounting engine between platforms; retain the Windows native overlay and watcher.
- Add the macOS AppKit/Accessibility adapter with a host-bound statistics worker and a silent lifecycle watcher, distributed as an Apple Silicon DMG.
- Add macOS source-path handling, file notifications and file identity stamps. macOS Desktop interaction still requires native acceptance testing.
- Bound Accessibility calls, retry failed event subscriptions, preserve chat bindings under dialogs, and rearm file watches after log replacement.
- Retire stale identity on new-chat navigation, recognize Electron web dialogs and minimized windows, preserve worker shutdown deadlines across Desktop restarts, and request Electron's accessibility tree with bounded retries.
