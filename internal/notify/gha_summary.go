package notify

import (
	"io"
	"log"
	"os"
)

// AppendGHASummary opens path in append mode (creating it if absent), calls
// render with the open file, and closes it. Both failure modes are logged as
// non-fatal WARNINGs rather than returned to the caller: an open failure
// means there's nothing to write to, and the buffered writes render performs
// are only flushed by Close, so dropping its error would report a summary
// that was never actually finished writing.
func AppendGHASummary(path string, render func(io.Writer)) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		log.Printf("WARNING: failed to open GHA summary file: %v", err)
		return
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			log.Printf("WARNING: GHA summary may be incomplete: %v", cerr)
		}
	}()

	render(f)
}
