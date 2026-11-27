package fake

import "github.com/podhmo/minigo/testdata/srcreplace/app"

func LoadUser(id int) app.User { return app.User{Name: "fake"} }
