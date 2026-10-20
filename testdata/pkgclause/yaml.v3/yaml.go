// Package yaml lives in a directory named yaml.v3, like gopkg.in/yaml.v3:
// an unaliased import binds yaml, not the path tail.
package yaml

// Node is a decoded document node.
type Node struct {
	Value string
}

// Marshal renders s.
func Marshal(s string) string { return "yaml:" + s }
