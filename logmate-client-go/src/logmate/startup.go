package logmate

import (
	"fmt"
	"io"
)

func reportStarted(writer io.Writer) {
	_, _ = fmt.Fprintln(writer, "[LogMate] Initialized successfully!")
}
