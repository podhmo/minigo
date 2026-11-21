package main

// Imports packages that the spike measured as compile-triggered builds
// (io/fs, mime/multipart, net/http, net/url, math/bits) and touches
// both their const and type surfaces so the compiler must materialize
// facts from them.
import (
	"io/fs"
	"math/bits"
	"mime/multipart"
	"net/http"
	"net/url"
)

var _ fs.FileInfo
var _ *multipart.Part
var _ http.Handler = http.NotFoundHandler()

func Answer() int {
	u, err := url.Parse("https://example.com/x?y=1")
	if err != nil {
		return -1
	}
	m := http.MethodGet + u.Path
	return bits.OnesCount(uint(len(m)))
}
