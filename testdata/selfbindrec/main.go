package main

import "net/http"

func Mutate(xs []string) { xs[0] = "changed" }

func Write(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Test", "yes")
	w.WriteHeader(404)
	w.Write([]byte("x"))
}
