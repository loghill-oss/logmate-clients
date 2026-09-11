// Package logmate sends structured application logs to a LogMate server.
//
// Its implementation lives under src/logmate, following the layout of the
// other LogMate clients. This facade keeps the public Go import concise:
//
//	import logmate "github.com/loghill-oss/logmate-clients/logmate-client-go"
package logmate

import impl "github.com/loghill-oss/logmate-clients/logmate-client-go/src/logmate"

const (
	Version           = impl.Version
	SeverityUndefined = impl.SeverityUndefined
	SeverityTrace     = impl.SeverityTrace
	SeverityDebug     = impl.SeverityDebug
	SeverityInfo      = impl.SeverityInfo
	SeverityWarn      = impl.SeverityWarn
	SeverityError     = impl.SeverityError
	SeverityFatal     = impl.SeverityFatal
)

type (
	Severity   = impl.Severity
	Entry      = impl.Entry
	LogOptions = impl.LogOptions
	Config     = impl.Config
	Client     = impl.Client
	Logger     = impl.Logger
)

// New creates a LogMate client.
func New(config Config) (*Client, error) { return impl.New(config) }

// Instrument returns the process-wide ready-to-use logger.
func Instrument(configs ...Config) *Logger { return impl.Instrument(configs...) }

// CreateLogger is a compatibility alias for Instrument.
func CreateLogger(configs ...Config) *Logger { return impl.CreateLogger(configs...) }
