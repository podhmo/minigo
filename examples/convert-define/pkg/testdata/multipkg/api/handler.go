package api

import "example.com/multipkg-test/models"

// Handler represents an API handler that uses a model from another package.
type Handler struct {
	User models.User `json:"user"`
}
