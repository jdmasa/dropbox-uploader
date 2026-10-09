package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/runtime"

	"dropboxuploader/internal/config"
	"dropboxuploader/internal/dbx"
	"dropboxuploader/internal/i18n"
	"dropboxuploader/internal/localfs"
	"dropboxuploader/internal/media"
	"dropboxuploader/internal/sysutil"
	"dropboxuploader/internal/uploader"
)

// Set at build time: -ldflags "-X main.dropboxAppKey=xxxx -X main.version=1.2.3"
var (
	dropboxAppKey = ""
	version       = "dev"
)

// App holds the application state; its exported methods are callable from the UI.
type App struct {
	ctx    context.Context
	cfg    *config.Store
	thumbs *localfs.Thumbnailer
	media  *media.Handler
	queue  *uploader.Manager

	mu            sync.Mutex
	client        *dbx.Client
	cancelConnect context.CancelFunc
	notify        bool // system notifications available (Windows 10+)
}

func NewApp() *App {
	a := &App{cfg: config.Load()}
	a.thumbs = localfs.NewThumbnailer(filepath.Join(config.DataDir(), "thumbs"))
	a.media = media.New(a.thumbs, a.dropbox)
	cfg := a.cfg.Get()
	a.queue = uploader.New(nil, uploader.Options{
		Workers:   cfg.Workers,
		StatePath: filepath.Join(config.DataDir(), "queue.json"),
		Emit: func(event string, data any) {
			if a.ctx != nil {
				runtime.EventsEmit(a.ctx, event, data)
			}
		},
		OnBusy:    sysutil.KeepAwake,
		OnAllDone: a.allDone,
	})
	if key := a.appKey(); key != "" && cfg.RefreshToken != "" {
		a.setClient(dbx.New(dbx.NewTokens(key, cfg.RefreshToken)))
	}
	return a
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	if sysutil.ToastsSupported() {
		a.notify = runtime.InitializeNotifications(ctx) == nil
	}
	a.queue.Start()
}

// beforeClose asks for confirmation while uploads are running. Returning true keeps the window open.
func (a *App) beforeClose(ctx context.Context) bool {
	if w, h := runtime.WindowGetSize(ctx); w > 0 && h > 0 && !runtime.WindowIsMaximised(ctx) {
		_ = a.cfg.Update(func(c *config.Config) { c.WindowW, c.WindowH = w, h })
	}
	if !a.queue.Busy() {
		return false
	}
	lang := a.lang()
	yes, no := i18n.T(lang, "yes"), i18n.T(lang, "no")
	choice, err := runtime.MessageDialog(ctx, runtime.MessageDialogOptions{
		Type:          runtime.QuestionDialog,
		Title:         i18n.T(lang, "close.title"),
		Message:       i18n.T(lang, "close.message"),
		Buttons:       []string{yes, no},
		DefaultButton: no,
		CancelButton:  no,
	})
	// On Windows the system buttons always answer "Yes"/"No".
	return err == nil && choice != yes && choice != "Yes" && choice != "Ok"
}

// secondInstance brings the existing window to the front when the app is opened twice.
func (a *App) secondInstance(options.SecondInstanceData) {
	runtime.WindowUnminimise(a.ctx)
	runtime.WindowShow(a.ctx)
}

func (a *App) shutdown(ctx context.Context) {
	a.queue.Close()
	sysutil.KeepAwake(false)
	if a.notify {
		runtime.CleanupNotifications(ctx)
	}
}

func (a *App) lang() string {
	if l := a.cfg.Get().Lang; i18n.Valid(l) {
		return l
	}
	return i18n.Default
}

func (a *App) appKey() string {
	if k := strings.TrimSpace(a.cfg.Get().AppKey); k != "" {
		return k
	}
	return dropboxAppKey
}

func (a *App) dropbox() *dbx.Client {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.client
}

func (a *App) setClient(c *dbx.Client) {
	a.mu.Lock()
	a.client = c
	a.mu.Unlock()
	if c == nil {
		a.queue.SetAPI(nil)
	} else {
		a.queue.SetAPI(c)
	}
}

func (a *App) allDone(s uploader.RunStats) {
	if a.ctx == nil {
		return
	}
	runtime.EventsEmit(a.ctx, "queue:alldone", s)
	lang := a.lang()
	body := i18n.T(lang, "notify.done", s.Done)
	if s.Skipped > 0 {
		body += i18n.T(lang, "notify.skip", s.Skipped)
	}
	if s.Failed > 0 {
		body += i18n.T(lang, "notify.fail", s.Failed)
	}
	if !a.notify {
		return
	}
	_ = runtime.SendNotification(a.ctx, runtime.NotificationOptions{
		ID:    fmt.Sprintf("done-%d", time.Now().Unix()),
		Title: "Dropbox Uploader",
		Body:  body,
	})
}

// ---- State & login ----

type State struct {
	Lang         string `json:"lang"`
	Connected    bool   `json:"connected"`
	NeedsAppKey  bool   `json:"needsAppKey"`
	AccountName  string `json:"accountName"`
	AccountEmail string `json:"accountEmail"`
	Workers      int    `json:"workers"`
	ViewMode     string `json:"viewMode"`
	LastLocal    string `json:"lastLocal"`
	LastDropbox  string `json:"lastDropbox"`
	Version      string `json:"version"`
	Platform     string `json:"platform"`
	RedirectURI  string `json:"redirectUri"`
}

func (a *App) GetState() State {
	c := a.cfg.Get()
	return State{
		Lang:         a.lang(),
		Connected:    a.dropbox() != nil,
		NeedsAppKey:  a.appKey() == "",
		AccountName:  c.AccountName,
		AccountEmail: c.AccountEmail,
		Workers:      c.Workers,
		ViewMode:     c.ViewMode,
		LastLocal:    c.LastLocal,
		LastDropbox:  c.LastDropbox,
		Version:      version,
		Platform:     goruntime.GOOS,
		RedirectURI:  dbx.RedirectURI,
	}
}

// SetAppKey stores the Dropbox App Console key (only needed when not built in).
func (a *App) SetAppKey(key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("err.appKeyEmpty")
	}
	return a.cfg.Update(func(c *config.Config) { c.AppKey = key })
}

// SetLanguage stores the UI language ("ca", "es" or "en").
func (a *App) SetLanguage(lang string) {
	if i18n.Valid(lang) {
		_ = a.cfg.Update(func(c *config.Config) { c.Lang = lang })
	}
}

// Connect opens Dropbox's sign-in page in the browser and waits for the user to allow access.
func (a *App) Connect() (State, error) {
	key := a.appKey()
	if key == "" {
		return a.GetState(), errors.New("err.appKeyMissing")
	}
	ctx, cancel := context.WithTimeout(a.ctx, 10*time.Minute)
	defer cancel()
	a.mu.Lock()
	a.cancelConnect = cancel
	a.mu.Unlock()
	refresh, err := dbx.Login(ctx, key, a.lang(), func(u string) { runtime.BrowserOpenURL(a.ctx, u) })
	if errors.Is(err, context.Canceled) {
		return a.GetState(), errors.New("err.signinCancelled")
	}
	if err != nil {
		log.Printf("login: %v", err)
		return a.GetState(), err
	}
	client := dbx.New(dbx.NewTokens(key, refresh))
	acct, err := client.CurrentAccount(ctx)
	if err != nil {
		return a.GetState(), err
	}
	if err := a.cfg.Update(func(c *config.Config) {
		c.RefreshToken = refresh
		c.AccountName = acct.Name
		c.AccountEmail = acct.Email
	}); err != nil {
		return a.GetState(), err
	}
	a.setClient(client)
	runtime.WindowShow(a.ctx)
	return a.GetState(), nil
}

// CancelConnect stops waiting for the browser sign-in.
func (a *App) CancelConnect() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancelConnect != nil {
		a.cancelConnect()
	}
}

func (a *App) Disconnect() State {
	a.queue.Pause()
	_ = a.cfg.Update(func(c *config.Config) {
		c.RefreshToken, c.AccountName, c.AccountEmail = "", "", ""
	})
	a.setClient(nil)
	return a.GetState()
}

// ---- Left panel: this computer ----

func (a *App) Places() []localfs.Place { return localfs.Places() }

func (a *App) ListLocal(dir string) (*localfs.Listing, error) {
	if dir == "" {
		dir = a.cfg.Get().LastLocal
	}
	if dir == "" {
		if places := localfs.Places(); len(places) > 0 {
			dir = places[0].Path
		}
	}
	l, err := localfs.List(dir)
	if err != nil {
		return nil, friendlyLocalError(err)
	}
	_ = a.cfg.Update(func(c *config.Config) { c.LastLocal = l.Path })
	return l, nil
}

func friendlyLocalError(err error) error {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return errors.New("err.folderGone")
	case errors.Is(err, os.ErrPermission):
		return errors.New("err.folderDenied")
	}
	return err
}

// ChooseFolder shows the Windows folder picker.
func (a *App) ChooseFolder() (string, error) {
	return runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title:            i18n.T(a.lang(), "pick.folder"),
		DefaultDirectory: a.cfg.Get().LastLocal,
	})
}

// ChooseFiles shows the Windows file picker for photos and videos.
func (a *App) ChooseFiles() ([]string, error) {
	return runtime.OpenMultipleFilesDialog(a.ctx, runtime.OpenDialogOptions{
		Title:            i18n.T(a.lang(), "pick.files"),
		DefaultDirectory: a.cfg.Get().LastLocal,
		Filters: []runtime.FileFilter{
			{DisplayName: i18n.T(a.lang(), "filter.media"), Pattern: "*.jpg;*.jpeg;*.png;*.heic;*.gif;*.webp;*.mp4;*.mov;*.m4v;*.avi;*.mts;*.3gp"},
			{DisplayName: i18n.T(a.lang(), "filter.all"), Pattern: "*.*"},
		},
	})
}

// ---- Right panel: Dropbox ----

type DropboxEntry struct {
	Name     string       `json:"name"`
	Path     string       `json:"path"`
	Kind     localfs.Kind `json:"kind"`
	Size     int64        `json:"size"`
	Modified time.Time    `json:"modified"`
	Thumb    bool         `json:"thumb"`
}

type DropboxListing struct {
	Path    string          `json:"path"`
	Parent  string          `json:"parent"`
	Crumbs  []localfs.Crumb `json:"crumbs"`
	Entries []DropboxEntry  `json:"entries"`
}

var dropboxThumbExt = map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".tiff": true, ".tif": true, ".gif": true, ".webp": true, ".bmp": true, ".heic": true}

func (a *App) ListDropbox(folder string) (*DropboxListing, error) {
	c := a.dropbox()
	if c == nil {
		return nil, dbx.ErrNotLoggedIn
	}
	folder = strings.TrimRight(folder, "/")
	ctx, cancel := context.WithTimeout(a.ctx, 2*time.Minute)
	defer cancel()
	entries, err := c.ListFolder(ctx, folder)
	if err != nil {
		var ae *dbx.APIError
		if folder != "" && errors.As(err, &ae) && strings.Contains(ae.Summary, "not_found") {
			return a.ListDropbox("") // the remembered folder was deleted
		}
		return nil, err
	}
	l := &DropboxListing{Path: folder, Crumbs: []localfs.Crumb{{Name: "Dropbox", Path: ""}}}
	if folder != "" {
		l.Parent = path.Dir(folder)
		if l.Parent == "/" {
			l.Parent = ""
		}
		acc := ""
		for _, part := range strings.Split(strings.Trim(folder, "/"), "/") {
			acc += "/" + part
			l.Crumbs = append(l.Crumbs, localfs.Crumb{Name: part, Path: acc})
		}
	}
	for _, e := range entries {
		de := DropboxEntry{Name: e.Name, Path: e.PathDisplay, Size: e.Size, Modified: e.ServerModified}
		switch e.Tag {
		case "folder":
			de.Kind = localfs.Folder
		case "file":
			de.Kind = localfs.KindOf(e.Name)
			de.Thumb = dropboxThumbExt[strings.ToLower(path.Ext(e.Name))] && e.Size < 20<<20
		default:
			continue
		}
		l.Entries = append(l.Entries, de)
	}
	sort.SliceStable(l.Entries, func(i, j int) bool {
		x, y := l.Entries[i], l.Entries[j]
		if (x.Kind == localfs.Folder) != (y.Kind == localfs.Folder) {
			return x.Kind == localfs.Folder
		}
		return localfs.NaturalLess(strings.ToLower(x.Name), strings.ToLower(y.Name))
	})
	_ = a.cfg.Update(func(c *config.Config) { c.LastDropbox = folder })
	return l, nil
}

func (a *App) CreateDropboxFolder(parent, name string) (string, error) {
	c := a.dropbox()
	if c == nil {
		return "", dbx.ErrNotLoggedIn
	}
	name = strings.TrimSpace(name)
	if name == "" || strings.ContainsAny(name, `/\`) {
		return "", errors.New("err.folderName")
	}
	p := path.Join("/", parent, name)
	ctx, cancel := context.WithTimeout(a.ctx, time.Minute)
	defer cancel()
	return p, c.CreateFolder(ctx, p)
}

// OpenInDropbox shows a file or folder on dropbox.com in the browser.
func (a *App) OpenInDropbox(p string, isFolder bool) {
	u := "https://www.dropbox.com/home"
	if isFolder {
		u += escapePath(p)
	} else {
		u += escapePath(path.Dir(p)) + "?preview=" + url.QueryEscape(path.Base(p))
	}
	runtime.BrowserOpenURL(a.ctx, u)
}

func escapePath(p string) string {
	if p == "/" || p == "" {
		return ""
	}
	parts := strings.Split(strings.Trim(p, "/"), "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return "/" + strings.Join(parts, "/")
}

// ---- Queue ----

func (a *App) Enqueue(paths []string, dest string) (int, error) {
	if a.dropbox() == nil {
		return 0, dbx.ErrNotLoggedIn
	}
	n, err := a.queue.Enqueue(paths, dest)
	if err != nil {
		return n, friendlyLocalError(err)
	}
	a.queue.Resume()
	return n, nil
}

func (a *App) GetQueue() uploader.Update { return a.queue.Snapshot() }
func (a *App) Pause()                    { a.queue.Pause() }
func (a *App) Resume()                   { a.queue.Resume() }
func (a *App) CancelAll()                { a.queue.CancelAll() }
func (a *App) ClearFinished()            { a.queue.ClearFinished() }
func (a *App) RetryFailed()              { a.queue.Retry(nil) }
func (a *App) Retry(ids []int64)         { a.queue.Retry(ids) }
func (a *App) Remove(ids []int64)        { a.queue.Remove(ids) }
func (a *App) MoveToTop(ids []int64)     { a.queue.MoveToTop(ids) }
func (a *App) Move(id int64, delta int)  { a.queue.Move(id, delta) }
func (a *App) SetViewMode(mode string) {
	_ = a.cfg.Update(func(c *config.Config) { c.ViewMode = mode })
}
func (a *App) SetWorkers(n int) {
	a.queue.SetWorkers(n)
	_ = a.cfg.Update(func(c *config.Config) { c.Workers = n })
}

// OpenLogFolder opens the folder with the log and queue files (for troubleshooting).
func (a *App) OpenLogFolder() {
	dir := config.DataDir()
	switch goruntime.GOOS {
	case "windows":
		_ = exec.Command("explorer", dir).Start()
	case "darwin":
		_ = exec.Command("open", dir).Start()
	default:
		_ = exec.Command("xdg-open", dir).Start()
	}
}
