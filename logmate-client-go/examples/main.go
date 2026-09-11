package main

import logmate "github.com/loghill-oss/logmate-clients/logmate-client-go"

func main() {
	logger := logmate.Instrument(logmate.Config{
		APIURL:     "http://localhost:8080",
		SenderName: "logmate-client-go-example",
	})
	defer logger.Close()

	// Log an info message with metadata
	logger.Info("application started", logmate.LogOptions{
		Metadata: map[string]any{"environment": "development"},
	})

	// Log a warning message
	logger.Warn("This is a warning message.")

	// Log with an event name
	logger.Info("A simple event message", logmate.LogOptions{
		Event: "simple-event",
	})
}
