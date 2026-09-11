package logmate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"time"
)

func (c *Client) Enabled() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.remoteEnabled
}
func (c *Client) SenderID() string   { c.mu.Lock(); defer c.mu.Unlock(); return c.senderID }
func (c *Client) InstanceID() string { c.mu.Lock(); defer c.mu.Unlock(); return c.instanceID }

func (c *Client) Log(entry Entry) error {
	entry.Metadata = normalizeMetadata(entry.Metadata)
	if validationMessage := sanitizeEventFields(&entry); validationMessage != "" {
		c.report(errors.New(validationMessage + " The invalid field was ignored."))
	}
	if err := validateEntry(&entry); err != nil {
		c.report(err)
		return err
	}
	if !c.Enabled() {
		return nil
	}
	return c.enqueue(entry)
}

// Send is an alias for Log, matching the explicit-send API of other clients.
func (c *Client) Send(entry Entry) error { return c.Log(entry) }

// PendingCount returns the number of records waiting for delivery.
func (c *Client) PendingCount() int {
	return c.pendingCount()
}

func (c *Client) Info(message string, metadata map[string]any) error {
	return c.Log(Entry{Severity: SeverityInfo, Message: message, Metadata: metadata})
}
func (c *Client) Debug(message string, metadata map[string]any) error {
	return c.Log(Entry{Severity: SeverityDebug, Message: message, Metadata: metadata})
}
func (c *Client) Warn(message string, metadata map[string]any) error {
	return c.Log(Entry{Severity: SeverityWarn, Message: message, Metadata: metadata})
}
func (c *Client) Error(message string, metadata map[string]any) error {
	return c.Log(Entry{Severity: SeverityError, Message: message, Metadata: metadata})
}
func (c *Client) Fatal(message string, metadata map[string]any) error {
	return c.Log(Entry{Severity: SeverityFatal, Message: message, Metadata: metadata})
}
func (c *Client) Writer(severity Severity) io.Writer {
	return &lineWriter{client: c, severity: severity}
}
func (c *Client) StandardLogger() *log.Logger {
	return log.New(c.Writer(SeverityInfo), "", log.LstdFlags)
}

func (c *Client) Flush(ctx context.Context) bool {
	if !c.Enabled() {
		return false
	}
	c.mu.Lock()
	workerRunning := c.workerRunning
	c.mu.Unlock()
	if !workerRunning {
		return c.PendingCount() == 0
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		c.mu.Lock()
		closed := c.closed
		c.mu.Unlock()
		if c.PendingCount() == 0 {
			return true
		}
		if closed {
			return false
		}
		c.signal()
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
		}
	}
}

func (c *Client) Shutdown(ctx context.Context) error {
	var flushError error
	if c.Enabled() && !c.Flush(ctx) {
		if err := ctx.Err(); err != nil {
			flushError = err
		} else {
			flushError = errors.New("logmate: pending logs could not be flushed")
		}
	}
	c.once.Do(func() {
		c.mu.Lock()
		c.closed = true
		workerRunning := c.workerRunning
		c.mu.Unlock()
		close(c.done)
		c.signal()
		if !workerRunning {
			c.closeQueue()
			c.finishWorker()
		}
	})
	select {
	case <-c.workerDone:
	case <-ctx.Done():
		if flushError == nil {
			flushError = ctx.Err()
		}
	}
	return flushError
}

func (c *Client) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), c.shutdownTimeout)
	defer cancel()
	return c.Shutdown(ctx)
}
func (c *Client) report(err error) {
	if err == nil || c.onError == nil {
		return
	}
	message := err.Error()
	c.reportMu.Lock()
	if _, exists := c.reportedErrors[message]; exists {
		c.reportMu.Unlock()
		return
	}
	c.reportedErrors[message] = struct{}{}
	c.reportMu.Unlock()
	safeNotify(c.onError, err)
}

func (c *Client) reportStatus(message string) {
	if c.onError != nil {
		safeNotify(c.onError, errors.New(message))
	}
}

func safeNotify(handler func(error), err error) {
	defer func() { _ = recover() }()
	handler(err)
}

func (c *Client) disableRemote(reason string) {
	c.mu.Lock()
	c.remoteEnabled = false
	c.senderID, c.instanceID, c.instanceToken = "", "", ""
	onDisabled := c.onDisabled
	c.mu.Unlock()
	if onDisabled != nil {
		onDisabled()
	}
	c.report(fmt.Errorf("%s Logs will continue appearing in the terminal. Already persisted records will be kept for the next initialization.", reason))
}

func (c *Client) finishWorker() {
	c.workerDoneOnce.Do(func() { close(c.workerDone) })
}
