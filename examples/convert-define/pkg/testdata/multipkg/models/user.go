package models

// User is a model defined in its own package.
type User struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}
