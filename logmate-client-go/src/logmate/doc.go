// Package logmate sends structured application logs to a LogMate server.
//
// Instrument performs the bounded initial connection phase synchronously, then
// a background worker delivers queued records without blocking application log
// calls. Console output follows the compact Python client format.
package logmate
