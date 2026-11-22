package app

// StorySkipped demonstrates the discovery diagnostic: a Story-prefixed
// func in a file without the story build tag never reaches the index —
// the book reports it as "excluded" so the missing //go:build story
// line does not fail silently.
func StorySkipped() {}
