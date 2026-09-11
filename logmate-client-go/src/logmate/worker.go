package logmate

import (
	"fmt"
	"time"
)

func (c *Client) run() {
	defer func() {
		c.mu.Lock()
		c.workerRunning = false
		c.mu.Unlock()
		c.closeQueue()
		c.finishWorker()
	}()
	health := time.NewTicker(c.healthInterval)
	defer health.Stop()
	consecutiveFailures := 0
	queueNoticeShown := false
	offlineAnnounced := false
	for {
		select {
		case <-c.done:
			return
		case <-health.C:
			c.healthcheck()
		default:
		}
		if !c.connected() {
			if err := c.initialize(); err != nil {
				if requestIsPermanent(err) {
					c.disableRemote(err.Error())
					return
				}
				consecutiveFailures++
				queueNoticeShown, offlineAnnounced = c.reportDeliveryFailure(err, consecutiveFailures, queueNoticeShown, offlineAnnounced)
				if !c.wait(c.retryInterval) {
					return
				}
				continue
			}
			if offlineAnnounced {
				c.reportStatus(fmt.Sprintf("Connection restored. Resending %d pending log(s) in order.", c.PendingCount()))
			}
			consecutiveFailures = 0
			queueNoticeShown = false
			offlineAnnounced = false
		}
		item, ok := c.next()
		if !ok {
			select {
			case <-c.done:
				return
			case <-c.wake:
			case <-health.C:
				c.healthcheck()
			}
			continue
		}
		if err := c.send(item); err != nil {
			if requestIsPermanent(err) {
				c.disableRemote(err.Error())
				return
			}
			consecutiveFailures++
			queueNoticeShown, offlineAnnounced = c.reportDeliveryFailure(err, consecutiveFailures, queueNoticeShown, offlineAnnounced)
			if !c.wait(c.retryInterval) {
				return
			}
			continue
		}
		c.drop(item)
		if consecutiveFailures > 0 || offlineAnnounced {
			c.reportStatus(fmt.Sprintf("Connection restored. Resending %d pending log(s) in order.", c.PendingCount()))
		}
		consecutiveFailures = 0
		queueNoticeShown = false
		offlineAnnounced = false
	}
}

func (c *Client) initializeBlocking() bool {
	retriesDone := 0
	for c.Enabled() {
		if err := c.initialize(); err != nil {
			if requestIsPermanent(err) {
				c.disableRemote(err.Error())
				return false
			}
			if retriesDone >= c.retryAttempts {
				c.reportStatus("LogMate is unavailable after the initial attempts. The application will continue without further attempts in this run.")
				return false
			}
			retriesDone++
			c.reportStatus(fmt.Sprintf("Failed to initialize the API connection. Retry (%d/%d): %s", retriesDone, c.retryAttempts, err))
			if !c.wait(c.retryInterval) {
				return false
			}
			continue
		}
		return true
	}
	return false
}

func (c *Client) initialize() error {
	var response map[string]any
	if err := c.post("/api/v1/instances/init", map[string]string{"sender_name": c.senderName}, "", "", "", &response); err != nil {
		return err
	}
	senderID := responseValue(response["sender_id"])
	if senderID == "" {
		senderID = responseValue(response["sender"])
	}
	instanceID := responseValue(response["instance_id"])
	instanceToken := responseValue(response["instance_token"])
	if senderID == "" || instanceID == "" || instanceToken == "" {
		return &requestError{message: "the API did not return sender_id, instance_id, and instance_token while initializing the logger.", permanent: true}
	}
	c.mu.Lock()
	c.senderID, c.instanceID, c.instanceToken = senderID, instanceID, instanceToken
	c.mu.Unlock()
	discarded := c.clearPersistentQueue()
	if discarded > 0 {
		c.reportStatus(fmt.Sprintf("%d persisted record(s) were removed from the queue at startup.", discarded))
	}
	if c.onStarted != nil {
		c.onStarted()
	}
	return nil
}

func responseValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case bool:
		if !typed {
			return ""
		}
		return "True"
	case float64:
		if typed == 0 {
			return ""
		}
		return fmt.Sprint(typed)
	default:
		return fmt.Sprint(typed)
	}
}

func (c *Client) send(item queuedEntry) error {
	c.mu.Lock()
	sender, instance, token := c.senderID, c.instanceID, c.instanceToken
	c.mu.Unlock()
	if item.SenderID != "" && item.SenderID != sender {
		return &requestError{message: "queued record belongs to another sender", permanent: true}
	}
	return c.post("/api/v1/logs", struct {
		Entry
		SenderID string `json:"sender_id"`
	}{item.Entry, sender}, instance, token, item.OriginInstanceID, nil)
}

func (c *Client) healthcheck() {
	c.mu.Lock()
	sender, instance, token := c.senderID, c.instanceID, c.instanceToken
	c.mu.Unlock()
	if sender == "" {
		return
	}
	if err := c.post("/api/v1/senders/"+sender+"/health", map[string]any{"status": "healthy", "details": map[string]any{"client": "go-slog"}}, instance, token, "", nil); err != nil {
		c.report(fmt.Errorf("health check failed: %w", err))
	}
}

func (c *Client) connected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.instanceID != "" && c.instanceToken != ""
}
func (c *Client) clearConnection() {
	c.mu.Lock()
	c.senderID, c.instanceID, c.instanceToken = "", "", ""
	c.mu.Unlock()
}

func (c *Client) reportDeliveryFailure(err error, failures int, queueNoticeShown, offlineAnnounced bool) (bool, bool) {
	if !queueNoticeShown {
		c.reportStatus("API connection failed. Logs are being queued for automatic retry.")
		queueNoticeShown = true
	}
	if failures <= c.retryAttempts {
		c.reportStatus(fmt.Sprintf("Connection retry (%d/%d): %s", failures, c.retryAttempts, err))
	} else if !offlineAnnounced {
		c.reportStatus("LogMate is unavailable. Logs will remain queued and be resent automatically.")
		offlineAnnounced = true
		c.clearConnection()
	}
	return queueNoticeShown, offlineAnnounced
}
