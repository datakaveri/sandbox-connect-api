package main

import (
	"net/http"
)

func (app *application) router() http.Handler {
	v1 := http.NewServeMux()
	v1.HandleFunc("POST /notebook/create", app.createNotebook)
	return v1
}
