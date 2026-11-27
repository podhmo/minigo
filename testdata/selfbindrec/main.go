package main

import "net/http"

func Write(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Test", "yes")
	w.WriteHeader(404)
	w.Write([]byte("x"))
}
