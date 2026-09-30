package destination

type DstUser struct {
	UserID    string
	FullName  string
	Address   DstAddress
	Contact   DstContact
	Details   []DstInternalDetail
	CreatedAt string
	UpdatedAt string
}

type DstAddress struct {
	FullStreet string
	CityName   string
}

type DstContact struct {
	EmailAddress string
	PhoneNumber  string
}

type DstInternalDetail struct {
	ItemCode      int
	LocalizedDesc string
}

type DstOrder struct {
	ID          string
	TotalAmount float64
	LineItems   []DstItem
}

type DstItem struct {
	ProductCode string
	Count       int
}

type ComplexTarget struct {
	Value       string
	Ptr         *string
	Slice       []SubTarget
	SliceOfPtrs []*SubTarget
}

type SubTarget struct {
	Value int
}

type TargetWithMap struct {
	ValueMap    map[string]SubTarget
	PtrMap      map[string]*SubTarget
	StringToStr map[string]string
}
