package inner

type Node struct {
	Kind    int
	Content []*Node
	Alias   *Node
	Index   map[string]*Node
}
