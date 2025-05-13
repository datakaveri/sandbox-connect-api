package main

import (
	"net/http"
)

func (app *application) router() http.Handler {
	v1 := http.NewServeMux()
	v1.HandleFunc("POST /notebook/create", app.createNotebook)
	v1.HandleFunc("POST /notebook/check-exists", app.checkNotebookExists)
	v1.HandleFunc("POST /pvc/check-exists", app.checkPVCExists)
	return v1
}
