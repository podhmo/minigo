package basic

// AppName is the name of the application.
const AppName = "MyAwesomeApp"

// User represents a basic user model.
type User struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// GetUserName returns the user's name.
func (u *User) GetUserName() string {
	return u.Name
}
