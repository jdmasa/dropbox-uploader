# Dropbox Uploader

A simple Windows desktop app for **uploading** photos and videos to Dropbox without syncing anything back to the computer.

- **Left panel:** this computer. Browse folders with large photo and video previews, and tick files or whole folders.
- **Right panel:** your Dropbox. Pick the folder to upload into, or create a new one.
- **Bottom panel:** the upload list, with overall and per-file progress, speed and time left. You can pause, cancel, retry, or move a file to the front.

Why not the browser or the official app?

- The official Dropbox app syncs everything.
- The website uploads only a few files at a time and often times out on big videos.

This app does it differently:

- Uploads several files in parallel (1–8, default 4).
- Sends large files in 8 MB pieces, retrying any piece that fails.
- Picks up interrupted uploads where they stopped, even after closing the app or restarting the PC.
- Skips files that are already in Dropbox with identical content.

## Features

- Desktop app (Go + [Wails](https://wails.io), WebView2). No browser and no local web server; only a temporary localhost port during the one-time Dropbox sign-in.
- Previews:
  - JPEG/PNG/GIF/WebP thumbnails, rotated correctly.
  - HEIC, RAW and video thumbnails through Windows' own thumbnail feature (the same one Explorer uses).
  - Full-size preview with ← / → to step through photos and play videos.
- Drag and drop:
  - From Windows Explorer onto the Dropbox panel.
  - From the left panel onto the Dropbox panel or onto a Dropbox folder.
- Upload engine:
  - Uploads use Dropbox upload sessions with `finish_batch_v2`. Committing in batches avoids Dropbox's `too_many_write_operations` errors.
  - Retries back off exponentially and respect Dropbox's `Retry-After` header.
  - Wrong-offset errors are corrected automatically, and the file's original date is kept.
- Duplicates are detected with Dropbox's content hash. Name clashes with different content are renamed ("photo (1).jpg").
- The queue is saved to disk (`%LOCALAPPDATA%\DropboxUploader\queue.json`) and survives restarts.
- The PC is kept awake while uploading, and a Windows notification appears when everything is done.
- Closing the window during uploads asks for confirmation. Only one copy of the app runs at a time.

## One-time setup: create a Dropbox app

The app signs in with OAuth 2 PKCE, so it needs a Dropbox **App key** but no secret.

1. Go to <https://www.dropbox.com/developers/apps> and click **Create app**.
2. Choose **Scoped access** → **Full Dropbox**, and give it a name (e.g. "Family Uploader").
3. On the **Settings** tab:
   - Under **OAuth 2 → Redirect URIs**, add `http://localhost:53682/callback`.
   - Copy the **App key**.
4. On the **Permissions** tab, enable `files.metadata.read`, `files.content.write`, `files.content.read` and `account_info.read`, then click **Submit**.
5. While the app is in "Development" status, up to 500 users can link it, which is plenty for family use.

Then either:

- **Build the key into the app (recommended):** in the GitHub repo, go to **Settings → Secrets and variables → Actions → Variables** and add `DROPBOX_APP_KEY`. Releases will then include it.
- **Or paste it on first run:** without a built-in key, the welcome screen asks for it once. It is stored in `%APPDATA%\DropboxUploader\config.json`.

## Download and install (Windows 10/11)

Download `DropboxUploader-Setup-x.y.z.exe` from the [Releases](../../releases) page and run it. The installer:

- adds Start-menu and desktop shortcuts;
- installs Microsoft WebView2 if it is missing (it is already part of Windows 10/11).

The app is not code-signed, so Windows SmartScreen may show "Windows protected your PC". Click **More info → Run anyway**.

A portable single `.exe` is also attached to each release.

## Development

Requirements: Go 1.26+, Node 20+, and the Wails CLI (`go install github.com/wailsapp/wails/v2/cmd/wails@v2.16.0`).

```sh
wails dev                       # live-reloading dev build (macOS/Windows)
go test -race ./internal/...    # unit tests (uploader engine, content hash, file listing)

# Windows build from any OS (the Windows build is pure Go):
wails build -platform windows/amd64 -ldflags "-X main.dropboxAppKey=YOUR_KEY"
# Windows installer (needs NSIS's makensis on PATH):
wails build -platform windows/amd64 -nsis -ldflags "-X main.dropboxAppKey=YOUR_KEY"
```

### Project layout

| Path | What it does |
|---|---|
| `main.go` | Window setup, file drop, single-instance lock, logging |
| `app.go` | Methods called from the UI (listing, login, queue actions) |
| `internal/dbx` | Minimal Dropbox API v2 client: PKCE login, token refresh, listing, upload sessions, batch commit, thumbnails, content hash |
| `internal/uploader` | Upload queue: worker pool, chunked sessions, retries, duplicate check, batch finisher, persistence, progress events |
| `internal/localfs` | Drives and known folders, folder listing, thumbnails (Go decoders + Windows thumbnail provider) |
| `internal/media` | Serves thumbnails and files to the window through Wails' asset server |
| `internal/sysutil` | Keep the PC awake while uploading |
| `frontend/` | Plain HTML/CSS/JS UI (built with Vite) |

### Files on the user's PC

| File | Contents |
|---|---|
| `%APPDATA%\DropboxUploader\config.json` | App key (if pasted), refresh token, settings |
| `%LOCALAPPDATA%\DropboxUploader\queue.json` | Upload queue, including resume points |
| `%LOCALAPPDATA%\DropboxUploader\thumbs\` | Thumbnail cache |
| `%LOCALAPPDATA%\DropboxUploader\log.txt` | Log (account menu → *Open log folder*) |

## Releasing

Push a tag. GitHub Actions runs the tests, builds the installer and portable `.exe` on Windows, and publishes a GitHub Release:

```sh
git tag v1.0.0
git push origin v1.0.0
```

Every push to `main` also builds the app; the result is attached to the workflow run as an artifact.
