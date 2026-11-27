package main

import "github.com/podhmo/minigo/testdata/srcreplace/app"

func Render() string { return app.Render() + "/" + app.RenderValue() }

func Direct() string {
	u := app.LoadUser(3)
	return u.Name
}
