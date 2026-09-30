package generics

import "fmt"

// Constraint interface
type Stringer interface {
	String() string
}

// Generic function with a single type parameter
func Print[T any](val T) {
	fmt.Println(val)
}

// Generic function with a type constraint (interface)
func PrintStringer[T Stringer](val T) {
	fmt.Println(val.String())
}

// Generic function with comparable constraint
func AreEqual[T comparable](a, b T) bool {
	return a == b
}

// Generic struct
type List[T any] struct {
	items []T
	Value T // Field using type parameter
}

// Method on generic struct
func (l *List[T]) Add(item T) {
	l.items = append(l.items, item)
}

// Method on generic struct using its type parameter in arg and receiver
func (l *List[T]) Get(index int) T {
	return l.items[index]
}

// Generic struct with multiple type parameters and constraints
type KeyValue[K comparable, V any] struct {
	Key   K
	Value V
}

// Function that returns a generic type instance
func NewList[T any](cap int) List[T] {
	return List[T]{items: make([]T, 0, cap)}
}

// Function that uses an instantiated generic type
func ProcessStringList(list List[string]) {
	for i := 0; i < len(list.items); i++ {
		fmt.Println(list.Get(i))
	}
}

// Function with type parameter as argument and result
func Identity[T any](val T) T {
	return val
}

// Type alias for a generic type instantiation
type StringList = List[string]

// Type alias for a generic type itself.
// type GenList[T any] = List[T] // This form of alias for generic type is a new type definition in effect.
// A true alias for a generic type specifier might look like:
// type MyList = List
// But Go doesn't directly support aliasing a generic type specifier without its type parameters yet.
// The above `GenList` example will be treated by `go/ast` as a new type `GenList[T]` whose underlying type is `List[T]`.

// Let's use a more direct new type definition that uses an embedded generic type for clarity in testing.
type GenList[T any] struct {
	InnerList List[T]
}

// Generic type used as a field in a non-generic struct
type Container struct {
	IntList List[int]
	KV      KeyValue[string, float64]
}

// Generic function used as a type for a field (not directly possible, but func type can be generic)
// This is more about function types:
type GenericFuncType[T any] func(T) T

var MyGenericFuncInstance GenericFuncType[int] = Identity[int]

// Interface with method using type parameters
type Processor[T any, U Stringer] interface {
	Process(data T) List[T]
	ProcessKeyValue(kv KeyValue[string, T]) U
}

// Struct implementing a generic interface concept
type IntStringProcessor struct{}

func (ip *IntStringProcessor) Process(data int) List[int] {
	l := NewList[int](1)
	l.Add(data * 2)
	return l
}

type myString string

func (ms myString) String() string { return string(ms) }

func (ip *IntStringProcessor) ProcessKeyValue(kv KeyValue[string, int]) myString {
	fmt.Printf("Processing KV: %s -> %d\n", kv.Key, kv.Value)
	return myString(fmt.Sprintf("%s:%d", kv.Key, kv.Value))
}

// Recursive generic type (e.g., for a tree node)
type Node[T any] struct {
	Value    T
	Children []Node[T] // Recursive use of Node[T]
}

func NewNode[T any](value T) *Node[T] {
	return &Node[T]{Value: value}
}

func (n *Node[T]) AddChild(child Node[T]) {
	n.Children = append(n.Children, child)
}
