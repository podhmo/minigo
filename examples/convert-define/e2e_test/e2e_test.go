//go:build e2e

package e2e_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	generated "github.com/podhmo/minigo/examples/convert-define/e2e_test"
	"github.com/podhmo/minigo/examples/convert-define/sampledata/destination"
	"github.com/podhmo/minigo/examples/convert-define/sampledata/source"
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

// Nested c.Map paths: dst.Inner.ID is written past the struct copy,
// src.PIn.Value is read behind a nil guard, and dst.PIn.Value is
// written behind a nil-init — all generated code, asserted end-to-end.
func TestGeneratedNestedConversion(t *testing.T) {
	ctx := context.Background()
	src := &source.SrcNested{
		ID:   42,
		Name: "nested",
		Inner: source.SrcNestedInner{
			ID:    7,
			Value: "inner-value",
		},
		PIn: &source.SrcNestedInner{
			ID:    8,
			Value: "pin-value",
		},
	}

	expected := &destination.DstNested{
		// Inner is struct-converted first (ID:7), then the explicit
		// leaf map overrides it with src.ID.
		Inner: destination.DstNestedInner{ID: 42, Value: "inner-value"},
		// PIn is converted via its pointer fast path, then the
		// explicit leaf write overwrites Value with src.Name.
		PIn:  &destination.DstNestedInner{ID: 8, Value: "nested"},
		Flat: "inner-value",
		Tag:  "pin-value",
	}

	got, err := generated.ConvertSrcNestedToDstNested(ctx, src)
	if err != nil {
		t.Fatalf("ConvertSrcNestedToDstNested() failed: %v", err)
	}
	if diff := cmp.Diff(expected, got); diff != "" {
		t.Errorf("ConvertSrcNestedToDstNested() mismatch (-want +got):\n%s", diff)
	}
}

// With a nil PIn the src guard drops the Tag read entirely, while the
// dst nil-init still allocates dst.PIn to carry src.Name.
func TestGeneratedNestedConversionNilPtr(t *testing.T) {
	ctx := context.Background()
	src := &source.SrcNested{
		ID:   42,
		Name: "nested",
		Inner: source.SrcNestedInner{
			ID:    7,
			Value: "inner-value",
		},
		PIn: nil,
	}

	expected := &destination.DstNested{
		Inner: destination.DstNestedInner{ID: 42, Value: "inner-value"},
		PIn:   &destination.DstNestedInner{ID: 0, Value: "nested"},
		Flat:  "inner-value",
		Tag:   "",
	}

	got, err := generated.ConvertSrcNestedToDstNested(ctx, src)
	if err != nil {
		t.Fatalf("ConvertSrcNestedToDstNested() failed: %v", err)
	}
	if diff := cmp.Diff(expected, got); diff != "" {
		t.Errorf("ConvertSrcNestedToDstNested() mismatch (-want +got):\n%s", diff)
	}
}
