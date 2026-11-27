package app

type User struct{ Name string }

func LoadUser(id int) User { return User{Name: "real"} }

func LoadScore(id int) (int, error) { return 1, nil }

func Render() string {
	u := LoadUser(1)
	return u.Name
}

func RenderValue() string {
	f := LoadUser
	u := f(2)
	return u.Name
}

func Score() int {
	n, err := LoadScore(3)
	if err != nil {
		return -1
	}
	return n
}
