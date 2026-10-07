package inner

type Tag struct {
	lang int
	full *string
}

func Make(l int) Tag { return Tag{lang: l} }

func (t *Tag) IsCompact() bool { return t.full == nil }

func (t *Tag) Lang() int { return t.lang }
