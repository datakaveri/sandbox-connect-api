package main

import (
	"net/http"
)

func (app *application) router() http.Handler {
	v1 := http.NewServeMux()
	v1.HandleFunc("POST /notebook/create", app.createNotebook)
	v1.HandleFunc("PATCH /notebook/stop", app.stopNotebook)
	v1.HandleFunc("PATCH /notebook/start", app.startNotebook)
	v1.HandleFunc("DELETE /notebook/delete", app.deleteNotebook)
	v1.HandleFunc("GET /notebook/list", app.listNotebooks)
	v1.HandleFunc("GET /notebook/check-exists/{notebook_name}", app.checkNotebookExists)
	v1.HandleFunc("GET /notebook/status/{notebook_name}", app.checkNotebookStatus)
	return v1
}
