package shadowinner

// `const true` shadows the universe literal inside this package — C
// binds the rune 'a', not a bool. D gives the local-shadow check in
// main.go a member whose type differs from the shadowing field.
const true = 'a'
const C = true
const D = 3
