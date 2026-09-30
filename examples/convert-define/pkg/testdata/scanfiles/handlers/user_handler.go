package handlers

import "example.com/scanfiles/core"

type UserHandler struct {
	UserSvc core.User
}

func NewUserHandler(u core.User) *UserHandler {
	return &UserHandler{UserSvc: u}
}
