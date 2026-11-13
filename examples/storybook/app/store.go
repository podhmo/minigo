package app

import (
	"fmt"
	"maps"
	"slices"
)

// MemoryStore is the server's in-memory user store.
type MemoryStore map[string]*User

func (s MemoryStore) Find(id string) (*User, error) {
	if u, ok := s[id]; ok {
		return u, nil
	}
	return nil, fmt.Errorf("no such user: %s", id)
}

func (s MemoryStore) List() []*User {
	return slices.Collect(maps.Values(s))
}
