package logmate

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

func (c *Client) prepareQueue() {
	if err := os.MkdirAll(filepath.Dir(c.queueFile), 0o700); err != nil {
		c.report(fmt.Errorf("could not create queue directory: %w", err))
		return
	}
	db, err := sql.Open("sqlite", c.queueFile)
	if err != nil {
		c.report(fmt.Errorf("could not open SQLite queue: %w", err))
		return
	}
	db.SetMaxOpenConns(1)
	for _, statement := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=FULL",
		"PRAGMA busy_timeout=5000",
		`CREATE TABLE IF NOT EXISTS log_queue (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			payload TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`,
	} {
		if _, err = db.Exec(statement); err != nil {
			_ = db.Close()
			c.report(fmt.Errorf("could not prepare SQLite queue: %w", err))
			return
		}
	}
	c.mu.Lock()
	c.queueDB = db
	c.persistent = true
	c.mu.Unlock()
}

func (c *Client) enqueue(entry Entry) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return errors.New("logmate: client is closed")
	}
	item := queuedEntry{Entry: entry, SenderID: c.senderID, OriginInstanceID: c.instanceID}
	var persistError error
	if c.persistent && c.queueDB != nil {
		var payload []byte
		payload, persistError = json.Marshal(item)
		if persistError == nil {
			_, persistError = c.queueDB.Exec(
				"INSERT INTO log_queue (payload, created_at) VALUES (?, ?)",
				string(payload), time.Now().UTC().Format(time.RFC3339Nano),
			)
		}
	}
	if persistError != nil || !c.persistent {
		c.memoryQueue = append(c.memoryQueue, item)
	}
	c.mu.Unlock()
	if persistError != nil {
		c.report(fmt.Errorf("could not persist the log in SQLite; keeping it in memory: %w", persistError))
	}
	c.signal()
	return nil
}

func (c *Client) clearPersistentQueue() int {
	c.mu.Lock()
	if !c.persistent || c.queueDB == nil {
		c.mu.Unlock()
		return 0
	}
	var count int
	if err := c.queueDB.QueryRow("SELECT COUNT(*) FROM log_queue").Scan(&count); err != nil {
		c.persistent = false
		c.mu.Unlock()
		c.report(fmt.Errorf("could not clear the persistent queue during initialization: %w. The SQLite queue will not be used in this run", err))
		return 0
	}
	if _, err := c.queueDB.Exec("DELETE FROM log_queue"); err != nil {
		c.persistent = false
		c.mu.Unlock()
		c.report(fmt.Errorf("could not clear the persistent queue during initialization: %w. The SQLite queue will not be used in this run", err))
		return 0
	}
	c.mu.Unlock()
	return count
}

func (c *Client) next() (queuedEntry, bool) {
	c.mu.Lock()
	if c.persistent && c.queueDB != nil {
		var id int64
		var payload string
		err := c.queueDB.QueryRow("SELECT id, payload FROM log_queue ORDER BY id ASC LIMIT 1").Scan(&id, &payload)
		if err == nil {
			var item queuedEntry
			if decodeErr := json.Unmarshal([]byte(payload), &item); decodeErr == nil {
				item.queueID, item.persisted = id, true
				c.mu.Unlock()
				return item, true
			}
			_, _ = c.queueDB.Exec("DELETE FROM log_queue WHERE id = ?", id)
			c.mu.Unlock()
			c.report(errors.New("removed an invalid record from the SQLite queue"))
			return c.next()
		}
		if !errors.Is(err, sql.ErrNoRows) {
			c.mu.Unlock()
			c.report(fmt.Errorf("could not read SQLite queue: %w", err))
			return queuedEntry{}, false
		}
	}
	if len(c.memoryQueue) == 0 {
		c.mu.Unlock()
		return queuedEntry{}, false
	}
	item := c.memoryQueue[0]
	c.mu.Unlock()
	return item, true
}

func (c *Client) drop(item queuedEntry) {
	c.mu.Lock()
	var err error
	if item.persisted && c.queueDB != nil {
		_, err = c.queueDB.Exec("DELETE FROM log_queue WHERE id = ?", item.queueID)
	} else if len(c.memoryQueue) > 0 {
		c.memoryQueue = c.memoryQueue[1:]
	}
	c.mu.Unlock()
	if err != nil {
		c.report(fmt.Errorf("could not acknowledge SQLite queue record: %w", err))
	}
}

func (c *Client) pendingCount() int {
	c.mu.Lock()
	total := len(c.memoryQueue)
	var countError error
	if c.persistent && c.queueDB != nil {
		var disk int
		if err := c.queueDB.QueryRow("SELECT COUNT(*) FROM log_queue").Scan(&disk); err != nil {
			countError = err
		} else {
			total += disk
		}
	}
	c.mu.Unlock()
	if countError != nil {
		c.report(fmt.Errorf("could not count SQLite queue records: %w", countError))
	}
	return total
}

func (c *Client) signal() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *Client) wait(delay time.Duration) bool {
	select {
	case <-c.done:
		return false
	case <-time.After(delay):
		return true
	}
}

func (c *Client) closeQueue() {
	c.mu.Lock()
	db := c.queueDB
	c.queueDB = nil
	c.mu.Unlock()
	if db != nil {
		_ = db.Close()
	}
}
