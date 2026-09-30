package implements

// --- Interfaces ---

type EmptyInterface interface{}

type SimpleInterface interface {
	Method1()
	Method2(a int) string
}

type PointerReceiverInterface interface {
	Method3()
}

type ValueReceiverInterface interface {
	Method4()
}

type ComplexTypeInterface interface {
	Method5(s []string, m map[string]int) (*bool, error)
}

type UnimplementedInterface interface {
	UnimplementedMethod()
}

// --- Structs ---

// Implements SimpleInterface and EmptyInterface
type SimpleStruct struct{}

func (s SimpleStruct) Method1()             {}
func (s SimpleStruct) Method2(a int) string { return "hello" }

// Implements PointerReceiverInterface and EmptyInterface
type PointerReceiverStruct struct{}

func (p *PointerReceiverStruct) Method3() {}

// Implements ValueReceiverInterface and EmptyInterface
type ValueReceiverStruct struct{}

func (v ValueReceiverStruct) Method4() {}

// Implements ComplexTypeInterface and EmptyInterface
type ComplexTypeStruct struct{}

func (c ComplexTypeStruct) Method5(s []string, m map[string]int) (*bool, error) {
	res := true
	return &res, nil
}

// --- Structs with Mismatches ---

type MissingMethodStruct struct{} // Missing Method2 for SimpleInterface

func (s MissingMethodStruct) Method1() {}

type WrongNameStruct struct{} // Method2 renamed to MethodWrongName for SimpleInterface

func (s WrongNameStruct) Method1()                     {}
func (s WrongNameStruct) MethodWrongName(a int) string { return "wrong" }

type WrongParamCountStruct struct{} // Method2 has too few params for SimpleInterface

func (s WrongParamCountStruct) Method1()        {}
func (s WrongParamCountStruct) Method2() string { return "wrong" }

type WrongParamTypeStruct struct{} // Method2 param 'a' is string instead of int for SimpleInterface

func (s WrongParamTypeStruct) Method1()                {}
func (s WrongParamTypeStruct) Method2(a string) string { return "wrong" }

type WrongReturnCountStruct struct{} // Method2 returns (string, int) instead of string for SimpleInterface

func (s WrongReturnCountStruct) Method1()                    {}
func (s WrongReturnCountStruct) Method2(a int) (string, int) { return "wrong", 1 }

type WrongReturnTypeStruct struct{} // Method2 returns int instead of string for SimpleInterface

func (s WrongReturnTypeStruct) Method1()          {}
func (s WrongReturnTypeStruct) Method2(a int) int { return 0 }

// Struct with only pointer receiver, for testing against ValueReceiverInterface
type OnlyPointerReceiverStruct struct{}

func (p *OnlyPointerReceiverStruct) Method4() {} // Implements Method4 with pointer receiver

// Struct with only value receiver, for testing against PointerReceiverInterface
type OnlyValueReceiverStruct struct{}

func (v OnlyValueReceiverStruct) Method3() {} // Implements Method3 with value receiver

type NoMethodStruct struct{}

// --- Other types for negative tests ---
type NotAStruct int
type NotAnInterface int

// --- Types for cross-package like comparison (simplified) ---
// These will be in the same package, but Implements current logic
// might treat them as distinct if FieldType.PkgName were involved.
// For now, FieldType.Name is the primary comparison point after pointer status.

type AnotherType struct{}

type InterfaceWithAnotherType interface {
	UseAnotherType(val AnotherType) AnotherType
	UsePointerAnotherType(val *AnotherType) *AnotherType
}

type StructWithAnotherType struct{}

func (s StructWithAnotherType) UseAnotherType(val AnotherType) AnotherType           { return val }
func (s *StructWithAnotherType) UsePointerAnotherType(val *AnotherType) *AnotherType { return val }

type StructWithDifferentNamedType struct{}
type YetAnotherType struct{}

func (s StructWithDifferentNamedType) UseAnotherType(val YetAnotherType) YetAnotherType { return val } // Should not match InterfaceWithAnotherType
func (s *StructWithDifferentNamedType) UsePointerAnotherType(val *YetAnotherType) *YetAnotherType {
	return val
}

type StructWithMismatchedPointerForAnotherType struct{}

func (s StructWithMismatchedPointerForAnotherType) UseAnotherType(val *AnotherType) *AnotherType {
	return val
} // val mismatch
func (s *StructWithMismatchedPointerForAnotherType) UsePointerAnotherType(val AnotherType) AnotherType {
	return val
} // val mismatch

type InterfaceWithSliceMap interface {
	ProcessSlice(s []int) []string
	ProcessMap(m map[string]bool) map[int]string
}

type StructImplementingSliceMap struct{}

func (s StructImplementingSliceMap) ProcessSlice(sl []int) []string              { return nil }
func (s StructImplementingSliceMap) ProcessMap(m map[string]bool) map[int]string { return nil }

type StructMismatchSlice struct{}

func (s StructMismatchSlice) ProcessSlice(sl []string) []string           { return nil } // Different element type
func (s StructMismatchSlice) ProcessMap(m map[string]bool) map[int]string { return nil }

type StructMismatchMapValue struct{}

func (s StructMismatchMapValue) ProcessSlice(sl []int) []string           { return nil }
func (s StructMismatchMapValue) ProcessMap(m map[string]bool) map[int]int { return nil } // Different value type in map

type StructMismatchMapKey struct{}

func (s StructMismatchMapKey) ProcessSlice(sl []int) []string           { return nil }
func (s StructMismatchMapKey) ProcessMap(m map[int]bool) map[int]string { return nil } // Different key type in map

// Struct for testing receiver type matching carefully
type MyStructForReceiverTest struct{}

func (s MyStructForReceiverTest) ValRec()  {}
func (p *MyStructForReceiverTest) PtrRec() {}

type InterfaceForValRec interface {
	ValRec()
}
type InterfaceForPtrRec interface {
	PtrRec()
}

// For testing Implements logic regarding receiver name matching.
// Current Implements function extracts receiver type name.
// e.g. for `func (r *MyType) Method()`, `fn.Receiver.Type.Name` might be "*MyType" or "MyType"
// depending on scanner.FieldType population.
// `structCandidate.Name` is "MyType".
// The test needs to ensure this comparison logic is sound.
// scanner.parseTypeExpr for *ast.StarExpr does:
//   underlyingType := s.parseTypeExpr(t.X)
//   underlyingType.IsPointer = true
//   return underlyingType
// This means for `*MyType`, FieldType.Name is "MyType" and IsPointer is true.
// So, `fn.Receiver.Type.Name == structCandidate.Name` (after stripping '*' if present)
// and `fn.Receiver.Type.IsPointer` is the correct check.

// Implements function:
// actualReceiverName := receiverTypeName
// if fn.Receiver.Type.IsPointer && strings.HasPrefix(receiverTypeName, "*") {
// 	 actualReceiverName = strings.TrimPrefix(receiverTypeName, "*")
// }
// if actualReceiverName == structCandidate.Name { ... }

// This logic seems correct. FieldType.Name should store the base name ("MyType")
// and FieldType.IsPointer indicates the pointer.
// The Implements function correctly normalizes the receiver type name from the function
// declaration before comparing with the struct candidate's name.
// No specific new struct/interface needed for this beyond existing pointer/value receiver tests.
// The existing PointerReceiverStruct/Interface and ValueReceiverStruct/Interface,
// along with MyStructForReceiverTest, should cover this.
// The key is that `scanner.ParseFuncDecl` and `scanner.ParseTypeExpr` correctly populate
// `FieldInfo.Type.Name` and `FieldInfo.Type.IsPointer` for receivers.
// `goscan.Implements` then uses this information.

// Test case: Struct implements interface method with value receiver, but struct method is pointer receiver.
// Interface: Method()
// Struct: *Method()
// This should count as an implementation.
type InterfaceValueRecMethod interface {
	DoIt()
}
type StructPointerRecMethodForInterfaceValue struct{}

func (s *StructPointerRecMethodForInterfaceValue) DoIt() {} // Implements InterfaceValueRecMethod.DoIt

// Test case: Struct implements interface method with pointer receiver, but struct method is value receiver.
// Interface: *Method() (though interfaces don't specify receiver pointer directly, methods apply to type)
// Struct: Method()
// This should NOT count as an implementation if interface implies methods applicable to *T.
// However, Go's interface implementation rule is:
// A type T implements an interface if T has all methods of the interface.
// A type *T has all methods of T, plus potentially more.
// If interface I requires method M(), and T has M() with value receiver, then *T also has M().
// If interface I requires method M(), and T has M() with pointer receiver, then only *T has M(), T does not.
// `Implements` function takes `structCandidate *scanner.TypeInfo`.
// This candidate represents the type itself (e.g., `MyStruct`).
// The methods are collected from `pkgInfo.Functions` where `fn.Receiver.Type.Name` matches `structCandidate.Name`
// (potentially after stripping `*` if `fn.Receiver.Type.IsPointer` is true).

// Consider `InterfaceForPtrRec` which has `PtrRec()`.
// If `structCandidate` is `MyStructForReceiverTest` (a value type):
//   - `MyStructForReceiverTest.PtrRec()` (pointer receiver) is NOT in its method set.
//   - So, `MyStructForReceiverTest` does NOT implement `InterfaceForPtrRec`. Correct.
// If `structCandidate` is `*MyStructForReceiverTest` (a pointer type):
//   - How do we represent `*MyStructForReceiverTest` as a `TypeInfo` to pass to `Implements`?
//   - `scanner.TypeInfo` represents named types `type T ...`. It doesn't inherently represent `*T`.
//   - The `Implements` function is designed for `structCandidate TypeInfo` (e.g. `MyStruct`)
//     and then it finds methods for that `MyStruct` (both value and pointer receivers).
//   - This implies `Implements` checks if `MyStruct` or `*MyStruct` implements the interface.

// Let's re-read the Implements function signature:
// `Implements(structCandidate *scanner.TypeInfo, interfaceDef *scanner.TypeInfo, pkgInfo *scanner.PackageInfo)`
// `structCandidate` is `MyStruct`.
// It collects methods for `MyStruct` (value receiver) and `*MyStruct` (pointer receiver) if their base name matches `structCandidate.Name`.

// Case 1: Interface `I { M() }`
// Struct `S {}`, `func (s S) M() {}` => `S` implements `I`. `*S` also implements `I`.
//   `Implements(S_TypeInfo, I_TypeInfo, ...)` should be true.
// Struct `S {}`, `func (s *S) M() {}` => `S` does NOT implement `I`. `*S` implements `I`.
//   `Implements(S_TypeInfo, I_TypeInfo, ...)` should be false.
//   The current `Implements` logic:
//   It iterates `pkgInfo.Functions`. If `fn.Receiver.Type.Name` (normalized) matches `S_TypeInfo.Name`,
//   it includes the method. This means it collects methods for BOTH `S` and `*S`.
//   So `structMethods` map will contain `M` (from `*S`)
//   Then it checks if `interfaceMethod.Name` (e.g., `M`) is in `structMethods`. Yes.
//   This means `Implements(S_TypeInfo, I_TypeInfo, ...)` would return TRUE even if `S` only has `(*S) M()`.
//   This is subtly different from Go's direct assignability rules for `S` vs `*S`.
//   The function effectively checks if "the type S, or its pointer type *S, implements the interface".
//   This might be the intended behavior of `goscan.Implements`. If so, tests should confirm this.

// Let's assume the current behavior of `Implements` (collecting methods for both T and *T associated with T's TypeInfo) is intended.
// Test this behavior:
type InterfaceRequiresMethodX interface {
	MethodX()
}
type StructValueReceiverMethodX struct{}

func (s StructValueReceiverMethodX) MethodX() {} // `StructValueReceiverMethodX` implements. `*StructValueReceiverMethodX` also implements.

type StructPointerReceiverMethodX struct{}

func (p *StructPointerReceiverMethodX) MethodX() {} // `StructPointerReceiverMethodX` does NOT implement directly. `*StructPointerReceiverMethodX` implements.

// Test `Implements(TypeInfo_for_StructPointerReceiverMethodX, TypeInfo_for_InterfaceRequiresMethodX, ...)`
// Expected: true (based on current Implements logic that gathers methods from both T and *T)
// This means if you have `var s StructPointerReceiverMethodX`, `var i InterfaceRequiresMethodX = s` would fail.
// But `var ps *StructPointerReceiverMethodX`, `var i InterfaceRequiresMethodX = ps` would succeed.
// The function seems to answer "is there *any way* type S (either as S or *S) can satisfy I?"

// If the goal is to strictly check if `structCandidate` (as a value type T) implements I,
// then the method collection part of `Implements` would need to filter:
//   - only include `fn` if `fn.Receiver.Type.IsPointer == false`
// If the goal is to strictly check if `*structCandidate` (as a pointer type *T) implements I,
// then `structCandidate` itself needs to represent `*T` or `Implements` needs another param.
// Given `structCandidate` is `TypeInfo` for `T`, the current behavior is the most flexible default.

// For `compareSignatures`:
// Assume `interfaceMethod` has `M(p P1) R1`
// Assume `structMethod` has `M(p P2) R2`
// `compareFieldTypes(P1, P2)` and `compareFieldTypes(R1, R2)` are called.
// The current `compareFieldTypes` is simple:
// `func compareFieldTypes(type1 *scanner.FieldType, type2 *scanner.FieldType) bool`
//   - checks nil
//   - checks `IsPointer`
//   - checks `Name`
// This means it does NOT currently check: PkgName, fullImportPath, IsSlice, IsMap, Elem, MapKey.
// Tests for Implements should reflect this. If `Implements` is expected to be more robust,
// then `compareFieldTypes` needs to be enhanced, and then `Implements` tests would change.
// For now, test the current behavior.

// Example:
// Interface: `Method(p []int)`
// Struct: `Method(p []string)` -> `compareFieldTypes` for `[]int` vs `[]string`.
//   FieldType for `[]int`: Name="slice", IsSlice=true, Elem={Name="int"}
//   FieldType for `[]string`: Name="slice", IsSlice=true, Elem={Name="string"}
//   Current `compareFieldTypes` would compare Name ("slice") and IsPointer (false), returning true.
//   This is INCORRECT. `compareFieldTypes` MUST recurse for Elem if IsSlice/IsMap.

// Let's look at FieldType.String() for hints on structure:
// For `[]T`: IsSlice=true, Elem points to FieldType for T. Name="slice" (or could be "[]T")
// For `*T`: IsPointer=true, Name="T" (base name).
// For `map[K]V`: IsMap=true, MapKey for K, Elem for V. Name="map".

// `compareFieldTypes` needs significant enhancement.
// For the purpose of testing `Implements` *now*, we assume `compareFieldTypes` will be fixed.
// So, the test cases for `Implements` should be written assuming a *correct* `compareFieldTypes`.
// If `compareFieldTypes` is not fixed as part of this task, then the `Implements` tests
// related to complex types will fail or need to be adjusted to expect the current (buggy) behavior.

// Plan:
// 1. Write `Implements` tests assuming `compareFieldTypes` correctly handles pointers, slices, maps, and base names.
// 2. If these tests fail due to `compareFieldTypes` bugs, it highlights the need to fix `compareFieldTypes`.
// 3. The current task is "test Implements". So, focus on `Implements` logic using `compareFieldTypes` as a black box.
//    The test data should include slice/map cases.

// Types for testing slice/map comparisons (assuming compareFieldTypes will be improved)
type InterfaceWithExactSliceMap interface {
	Foo(s []int, m map[string]bool)
}
type StructWithExactSliceMap struct{}

func (s StructWithExactSliceMap) Foo(s []int, m map[string]bool) {} // Matches

type StructWithSliceElemMismatch struct{}

func (s StructWithSliceElemMismatch) Foo(s []string, m map[string]bool) {} // Slice elem type mismatch

type StructWithMapKeyMismatch struct{}

func (s StructWithMapKeyMismatch) Foo(s []int, m map[int]bool) {} // Map key type mismatch

type StructWithMapValueMismatch struct{}

func (s StructWithMapValueMismatch) Foo(s []int, m map[string]int) {} // Map value type mismatch

type StructWithPointerInSlice struct{}

func (s StructWithPointerInSlice) Foo(s []*int, m map[string]bool) {} // Slice of pointers

type InterfaceWithPointerInSlice interface {
	Foo(s []*int, m map[string]bool)
}

type InterfaceWithDifferentPointerInSlice interface {
	Foo(s []*string, m map[string]bool)
}

// --- New types for Slice testing ---

type MyStruct struct {
	ID int
}
type AnotherStruct struct {
	Name string
}

// Interfaces with slice types
type SliceProcessor interface {
	ProcessIntSlice(data []int) []int
	ProcessStringSlice(data []string) []string
}

type SliceStructProcessor interface {
	ProcessStructSlice(data []MyStruct) []MyStruct
	ProcessPointerStructSlice(data []*MyStruct) []*MyStruct
}

type SliceOfPointerProcessor interface {
	ProcessSliceOfPointers(data []*MyStruct) []*MyStruct
	ProcessSliceOfPointerAlias(data []*AnotherType) []*AnotherType
}

// Structs implementing these interfaces
type MySliceProcessorImpl struct{}

func (s MySliceProcessorImpl) ProcessIntSlice(data []int) []int          { return data }
func (s MySliceProcessorImpl) ProcessStringSlice(data []string) []string { return data }

type MySliceStructProcessorImpl struct{}

func (s MySliceStructProcessorImpl) ProcessStructSlice(data []MyStruct) []MyStruct { return data }
func (s MySliceStructProcessorImpl) ProcessPointerStructSlice(data []*MyStruct) []*MyStruct {
	return data
}

type MySliceOfPointerProcessorImpl struct{}

func (s MySliceOfPointerProcessorImpl) ProcessSliceOfPointers(data []*MyStruct) []*MyStruct {
	return data
}
func (s MySliceOfPointerProcessorImpl) ProcessSliceOfPointerAlias(data []*AnotherType) []*AnotherType {
	return data
}

// Structs with mismatches for negative testing
type MismatchedSliceProcessorImpl struct{}

func (s MismatchedSliceProcessorImpl) ProcessIntSlice(data []string) []int    { return nil } // Param type mismatch: []string vs []int
func (s MismatchedSliceProcessorImpl) ProcessStringSlice(data []int) []string { return nil } // Param type mismatch: []int vs []string

type MismatchedReturnSliceProcessorImpl struct{}

func (s MismatchedReturnSliceProcessorImpl) ProcessIntSlice(data []int) []string    { return nil } // Return type mismatch: []string vs []int
func (s MismatchedReturnSliceProcessorImpl) ProcessStringSlice(data []string) []int { return nil } // Return type mismatch: []int vs []string

type MismatchedSliceStructProcessorImpl struct{}

// Param type mismatch: []AnotherStruct vs []MyStruct
func (s MismatchedSliceStructProcessorImpl) ProcessStructSlice(data []AnotherStruct) []MyStruct {
	return nil
}

// Param type mismatch: []*AnotherStruct vs []*MyStruct
func (s MismatchedSliceStructProcessorImpl) ProcessPointerStructSlice(data []*AnotherStruct) []*MyStruct {
	return nil
}

type MismatchedPointerNessSliceStructProcessorImpl struct{}

// Param type mismatch: []MyStruct (value) vs []*MyStruct (pointer)
func (s MismatchedPointerNessSliceStructProcessorImpl) ProcessStructSlice(data []MyStruct) []MyStruct {
	return nil
} // This is for ProcessStructSlice
// Param type mismatch: []*MyStruct (pointer) vs []MyStruct (value) - for a different interface method if needed
func (s MismatchedPointerNessSliceStructProcessorImpl) ProcessPointerStructSlice(data []MyStruct) []*MyStruct {
	return nil
}

type NotASliceProcessorImpl struct{}

func (s NotASliceProcessorImpl) ProcessIntSlice(data int) []int          { return nil } // Param not a slice
func (s NotASliceProcessorImpl) ProcessStringSlice(data string) []string { return nil } // Param not a slice

// Final check on Implements logic for receiver type name:
// `fn.Receiver.Type.Name` comes from `s.parseTypeExpr(recvField.Type)`.
// If `recvField.Type` is `*ast.StarExpr{X: &ast.Ident{Name: "MyStruct"}}`, then `parseTypeExpr` returns
// `FieldType{Name: "MyStruct", IsPointer: true}`.
// If `recvField.Type` is `&ast.Ident{Name: "MyStruct"}`, then `parseTypeExpr` returns
// `FieldType{Name: "MyStruct", IsPointer: false}`.
// The `Implements` function has this:
// ```go
// actualReceiverName := receiverTypeName // receiverTypeName is fn.Receiver.Type.Name
// if fn.Receiver.Type.IsPointer && strings.HasPrefix(receiverTypeName, "*") { // This HasPrefix check is redundant if Name is always base name
//	 actualReceiverName = strings.TrimPrefix(receiverTypeName, "*")
// }
// if actualReceiverName == structCandidate.Name { ... }
// ```
// If `fn.Receiver.Type.Name` is always the base name (e.g., "MyStruct") as per `parseTypeExpr` for `*ast.StarExpr`,
// then `strings.HasPrefix(receiverTypeName, "*")` will always be false.
// The `IsPointer` flag is the important one.
// The current logic in `Implements` for `actualReceiverName` might be a leftover from a previous `FieldType` structure.
// However, it should still work correctly:
// - If Name="MyStruct", IsPointer=true: `actualReceiverName` remains "MyStruct". Compares with "MyStruct". OK.
// - If Name="MyStruct", IsPointer=false: `actualReceiverName` remains "MyStruct". Compares with "MyStruct". OK.
// This seems fine.

// One final edge case: anonymous structs or interfaces?
// `scanner` likely doesn't support them as top-level named types, so `TypeInfo` wouldn't be created for them.
// `Implements` takes `TypeInfo`, so this is out of scope.
