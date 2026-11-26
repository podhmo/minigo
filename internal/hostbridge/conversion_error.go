package hostbridge

import "reflect"

// ConversionError marks a script/host marshalling failure. The VM maps
// it to runtime.TrapCodeHostConversion while preserving the causal error
// for errors.As/Unwrap.
type ConversionError struct{ Err error }

func (e *ConversionError) Error() string { return e.Err.Error() }
func (e *ConversionError) Unwrap() error { return e.Err }

func conversionResult(rv reflect.Value, err error) (reflect.Value, error) {
	if err != nil {
		return rv, &ConversionError{Err: err}
	}
	return rv, nil
}
