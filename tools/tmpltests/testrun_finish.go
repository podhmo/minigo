package main

import "os"

// finishTestRunOutput closes the report; write/close failures are harness errors.
func finishTestRunOutput(reportFile *os.File, out *checkedWriter, err error) error {
	var writeErr error
	if reportFile != nil {
		writeErr = reportFile.Close()
	}
	if werr := out.Err(); writeErr == nil {
		writeErr = werr
	}
	if writeErr != nil {
		err = writeErr
	}
	return err
}
