package features

import "net/http"

// DefaultItemID is the default ID for an item.
const DefaultItemID = 1

// Item represents a product with an ID and Name.
type Item struct {
	// The unique identifier for the item.
	ID   int    `json:"id"`
	Name string `json:"name"` // Name of the item.
}

// UserID is a custom type for user identifiers.
type UserID int64

// HandlerFunc defines a standard HTTP handler function signature.
type HandlerFunc func(w http.ResponseWriter, r *http.Request)

// ProcessItem is a function with documentation.
func ProcessItem(item Item) error {
	// implementation
	return nil
}
