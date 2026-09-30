//go:build e2e

package e2e_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/go-scan/examples/convert/sampledata/destination"
	"github.com/podhmo/go-scan/examples/convert/sampledata/source"
	generated "github.com/podhmo/minigo/examples/convert-define/e2e_test"
)

func TestGeneratedUserConversion(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	updatedAt := now.Add(time.Hour)

	src := &source.SrcUser{
		ID:        101,
		FirstName: "John",
		LastName:  "Doe",
		Address: source.SrcAddress{
			Street: "123 Main St",
			City:   "Anytown",
		},
		ContactInfo: source.SrcContact{
			Email: "john.doe@example.com",
			Phone: nil, // important: phone is nil
		},
		Details: []source.SrcInternalDetail{
			{Code: 1, Description: "Detail 1"},
		},
		CreatedAt: now,
		UpdatedAt: &updatedAt,
	}

	// Expected result from the *generated* converter.
	// With the new annotations, most fields should be correctly converted.
	expected := &destination.DstUser{
		UserID:   "user-101",
		FullName: "John Doe",
		Address: destination.DstAddress{
			FullStreet: "123 Main St",
			CityName:   "Anytown",
		},
		Contact: destination.DstContact{
			EmailAddress: "john.doe@example.com",
			PhoneNumber:  "N/A", // nil phone becomes "N/A"
		},
		Details: []destination.DstInternalDetail{
			{ItemCode: 1, LocalizedDesc: "翻訳済み (JP): Detail 1"},
		},
		CreatedAt: now.Format(time.RFC3339),
		UpdatedAt: updatedAt.Format(time.RFC3339),
	}

	got, err := generated.ConvertSrcUserToDstUser(ctx, src)
	if err != nil {
		t.Fatalf("ConvertSrcUserToDstUser() failed: %v", err)
	}

	if diff := cmp.Diff(expected, got); diff != "" {
		t.Errorf("ConvertSrcUserToDstUser() mismatch (-want +got):\n%s", diff)
	}
}

func TestGeneratedOrderConversion(t *testing.T) {
	ctx := context.Background()
	src := &source.SrcOrder{
		OrderID: "ORD-001",
		Amount:  99.99,
		Items: []source.SrcItem{
			{SKU: "item-1", Quantity: 2},
		},
	}

	// With the new annotations, all fields should be converted.
	expected := &destination.DstOrder{
		ID:          "ORD-001",
		TotalAmount: 99.99,
		LineItems: []destination.DstItem{
			{ProductCode: "item-1", Count: 2},
		},
	}

	got, err := generated.ConvertSrcOrderToDstOrder(ctx, src)
	if err != nil {
		t.Fatalf("ConvertSrcOrderToDstOrder() failed: %v", err)
	}

	if diff := cmp.Diff(expected, got); diff != "" {
		t.Errorf("ConvertSrcOrderToDstOrder() mismatch (-want +got):\n%s", diff)
	}
}
