package funcs

import (
	"context"
	"fmt"

	"github.com/podhmo/go-scan/examples/convert/model"
	"github.com/podhmo/go-scan/examples/convert/sampledata/destination"
	"github.com/podhmo/go-scan/examples/convert/sampledata/source"
)

// UserIDToString converts user id from int64 to string with prefix
func UserIDToString(ctx context.Context, ec *model.ErrorCollector, id int64) string {
	return fmt.Sprintf("user-%d", id)
}

// Translate translates description
func Translate(ctx context.Context, ec *model.ErrorCollector, description string) string {
	// simulate translation
	return "翻訳済み (JP): " + description
}

// ConvertSrcContactToDstContact converts source.SrcContact to destination.DstContact
func ConvertSrcContactToDstContact(ctx context.Context, ec *model.ErrorCollector, src source.SrcContact) destination.DstContact {
	dst := destination.DstContact{
		EmailAddress: src.Email,
	}
	if src.Phone != nil {
		dst.PhoneNumber = *src.Phone
	} else {
		dst.PhoneNumber = "N/A"
	}
	return dst
}

func MakeFullName(firstName string, lastName string) string {
	return firstName + " " + lastName
}
