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

type DstNested struct {
	Inner DstNestedInner
	PIn   *DstNestedInner
	Flat  string
	Tag   string
}

type DstNestedInner struct {
	ID    int64
	Value string
}

type DstShapes struct {
	PtrToVal    int64
	ValToPtr    *int64
	PtrPtr      **DstLeaf
	SlicePP     []**DstLeaf
	MapPP       map[string]**DstLeaf
	SlicePtrVal []int64
	SliceValPtr []*int64
	Nested      [][]int64
	MapSlice    map[int64][]int64
	Arr         [2]int64
	PSlice      []int64
}

type DstLeaf struct {
	V int64
}
