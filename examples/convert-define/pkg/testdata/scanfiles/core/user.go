package core

type User struct {
	ID   string
	Name string
}

func GetUserName(u User) string {
	return u.Name
}
