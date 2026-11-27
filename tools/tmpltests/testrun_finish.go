package main

import "os"

// finishTestRunOutput preserves the first runner failure while still
// closing the report file; close and earlier write failures become the
// result only when the runner itself succeeded.
func finishTestRunOutput(reportFile *os.File, out *checkedWriter, err error) error {
	if reportFile != nil {
		if cerr := reportFile.Close(); err == nil {
			err = cerr
		}
	}
	if err == nil {
		err = out.Err()
	}
	return err
}
