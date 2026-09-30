package source

import "time"

// convert:import convutil "github.com/podhmo/go-scan/examples/convert/convutil"
// convert:import funcs "github.com/podhmo/go-scan/examples/convert/sampledata/funcs"
// convert:rule "time.Time" -> "string", using=convutil.TimeToString
// convert:rule "*time.Time" -> "string", using=convutil.PtrTimeToString

// @derivingconvert("github.com/podhmo/go-scan/examples/convert/sampledata/destination.DstUser")
// convert:computed FullName = funcs.MakeFullName(src.FirstName, src.LastName)
type SrcUser struct {
	ID          int64 `convert:"UserID,using=funcs.UserIDToString"`
	FirstName   string
	LastName    string
	Address     SrcAddress
	ContactInfo SrcContact `convert:"Contact,using=funcs.ConvertSrcContactToDstContact"`
	Details     []SrcInternalDetail
	CreatedAt   time.Time
	UpdatedAt   *time.Time
}

// @derivingconvert("github.com/podhmo/go-scan/examples/convert/sampledata/destination.DstAddress")
type SrcAddress struct {
	Street string `convert:"FullStreet"`
	City   string `convert:"CityName"`
}

type SrcContact struct {
	Email string
	Phone *string
}

// @derivingconvert("github.com/podhmo/go-scan/examples/convert/sampledata/destination.DstInternalDetail")
type SrcInternalDetail struct {
	Code        int    `convert:"ItemCode"`
	Description string `convert:"LocalizedDesc,using=funcs.Translate"`
}

// @derivingconvert("github.com/podhmo/go-scan/examples/convert/sampledata/destination.DstOrder")
type SrcOrder struct {
	OrderID string    `convert:"ID"`
	Amount  float64   `convert:"TotalAmount"`
	Items   []SrcItem `convert:"LineItems"`
}

// @derivingconvert("github.com/podhmo/go-scan/examples/convert/sampledata/destination.DstItem")
type SrcItem struct {
	SKU      string `convert:"ProductCode"`
	Quantity int    `convert:"Count"`
}

// @derivingconvert("github.com/podhmo/go-scan/examples/convert/sampledata/destination.ComplexTarget")
type ComplexSource struct {
	Value       string
	Ptr         *string
	Slice       []SubSource
	SliceOfPtrs []*SubSource
}

type SubSource struct {
	Value int
}

// @derivingconvert("github.com/podhmo/go-scan/examples/convert/sampledata/destination.TargetWithMap")
type SourceWithMap struct {
	ValueMap    map[string]SubSource
	PtrMap      map[string]*SubSource
	StringToStr map[string]string
}
