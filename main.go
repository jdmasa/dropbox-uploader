package main

import (
	"embed"
	"io"
	"log"
	"os"
	"path/filepath"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/logger"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"

	"dropboxuploader/internal/config"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	logPath := filepath.Join(config.DataDir(), "log.txt")
	rotateLog(logPath)
	if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
		log.SetOutput(io.MultiWriter(os.Stderr, f))
		defer f.Close()
	}
	log.Printf("Dropbox Uploader %s starting", version)

	app := NewApp()
	cfg := app.cfg.Get()
	width, height := 1280, 820
	if cfg.WindowW >= 1000 && cfg.WindowH >= 650 {
		width, height = cfg.WindowW, cfg.WindowH
	}

	err := wails.Run(&options.App{
		Title:     "Dropbox Uploader",
		Width:     width,
		Height:    height,
		MinWidth:  1000,
		MinHeight: 650,
		AssetServer: &assetserver.Options{
			Assets:  assets,
			Handler: app.media,
		},
		BackgroundColour: &options.RGBA{R: 247, G: 248, B: 250, A: 255},
		DragAndDrop: &options.DragAndDrop{
			EnableFileDrop:     true,
			DisableWebViewDrop: true,
		},
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId:               "c7b1f3a2-5d0e-4c55-9a51-dropbox-uploader",
			OnSecondInstanceLaunch: app.secondInstance,
		},
		OnStartup:     app.startup,
		OnBeforeClose: app.beforeClose,
		OnShutdown:    app.shutdown,
		Logger:        logger.NewFileLogger(filepath.Join(config.DataDir(), "wails.log")),
		Bind:          []interface{}{app},
		Windows: &windows.Options{
			Theme: windows.Light,
		},
	})
	if err != nil {
		log.Printf("error: %v", err)
	}
}

// rotateLog keeps the log file from growing forever.
func rotateLog(p string) {
	if st, err := os.Stat(p); err == nil && st.Size() > 5<<20 {
		_ = os.Rename(p, p+".old")
	}
}
