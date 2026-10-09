// Package i18n holds the few texts shown by the Go side (native dialogs,
// notifications, the browser sign-in page). Everything in the app window is
// translated in frontend/src/i18n.js; errors are sent there as "err.code" keys.
package i18n

import "fmt"

const Default = "ca"

var dict = map[string]map[string]string{
	"ca": {
		"close.title":    "Encara s'estan pujant fitxers",
		"close.message":  "Alguns fitxers encara s'estan pujant.\n\nSi tanques ara, continuaran la propera vegada que obris Dropbox Uploader.\n\nVols tancar igualment?",
		"yes":            "Sí",
		"no":             "No",
		"notify.done":    "Fitxers pujats: %d.",
		"notify.skip":    " Ja eren a Dropbox: %d.",
		"notify.fail":    " No s'han pogut pujar: %d.",
		"pick.folder":    "Tria una carpeta",
		"pick.files":     "Tria fotos i vídeos",
		"filter.media":   "Fotos i vídeos",
		"filter.all":     "Tots els fitxers",
		"login.okTitle":  "Fet!",
		"login.okText":   "Ja pots tancar aquesta pestanya del navegador i tornar a Dropbox Uploader.",
		"login.errTitle": "Alguna cosa ha anat malament",
		"login.errText":  "Torna a Dropbox Uploader i torna-ho a provar.",
	},
	"es": {
		"close.title":    "Todavía se están subiendo archivos",
		"close.message":  "Algunos archivos todavía se están subiendo.\n\nSi cierras ahora, continuarán la próxima vez que abras Dropbox Uploader.\n\n¿Cerrar de todos modos?",
		"yes":            "Sí",
		"no":             "No",
		"notify.done":    "Archivos subidos: %d.",
		"notify.skip":    " Ya estaban en Dropbox: %d.",
		"notify.fail":    " No se han podido subir: %d.",
		"pick.folder":    "Elige una carpeta",
		"pick.files":     "Elige fotos y vídeos",
		"filter.media":   "Fotos y vídeos",
		"filter.all":     "Todos los archivos",
		"login.okTitle":  "¡Listo!",
		"login.okText":   "Ya puedes cerrar esta pestaña del navegador y volver a Dropbox Uploader.",
		"login.errTitle": "Algo ha salido mal",
		"login.errText":  "Vuelve a Dropbox Uploader e inténtalo de nuevo.",
	},
	"en": {
		"close.title":    "Uploads are still running",
		"close.message":  "Some files are still uploading.\n\nIf you close now, they will continue the next time you open Dropbox Uploader.\n\nClose anyway?",
		"yes":            "Yes",
		"no":             "No",
		"notify.done":    "%d files uploaded.",
		"notify.skip":    " %d were already in Dropbox.",
		"notify.fail":    " %d could not be uploaded.",
		"pick.folder":    "Choose a folder",
		"pick.files":     "Choose photos and videos",
		"filter.media":   "Photos and videos",
		"filter.all":     "All files",
		"login.okTitle":  "All set!",
		"login.okText":   "You can close this browser tab and go back to Dropbox Uploader.",
		"login.errTitle": "Something went wrong",
		"login.errText":  "Please go back to Dropbox Uploader and try again.",
	},
}

// Valid reports whether lang is a supported language code.
func Valid(lang string) bool {
	_, ok := dict[lang]
	return ok
}

// T returns the text for key in lang (falling back to Catalan, then the key),
// formatted with args when given.
func T(lang, key string, args ...any) string {
	s, ok := dict[lang][key]
	if !ok {
		if s, ok = dict[Default][key]; !ok {
			s = key
		}
	}
	if len(args) > 0 {
		return fmt.Sprintf(s, args...)
	}
	return s
}
