package logmate

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

var eventPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{2,79}$`)

func validateEntry(entry *Entry) error {
	entry.Severity = Severity(strings.ToUpper(string(entry.Severity)))
	if entry.Severity == "" {
		entry.Severity = SeverityInfo
	}
	switch entry.Severity {
	case SeverityUndefined, SeverityTrace, SeverityDebug, SeverityInfo, SeverityWarn, SeverityError, SeverityFatal:
	default:
		return fmt.Errorf("unsupported severity %q", entry.Severity)
	}
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now().UTC()
	} else {
		entry.Timestamp = entry.Timestamp.UTC()
	}
	return nil
}

func sanitizeEventFields(entry *Entry) string {
	if entry.Event != "" && !eventPattern.MatchString(entry.Event) {
		entry.Event = ""
		entry.EventOccurrenceID = ""
		return "invalid event: use only lowercase letters, numbers, '_' or '-', with 3 to 80 characters."
	}
	if utf8.RuneCountInString(entry.EventOccurrenceID) > 200 {
		entry.Event = ""
		entry.EventOccurrenceID = ""
		return "invalid event_occurrence_id: the limit is 200 characters."
	}
	return ""
}

func normalizeMetadata(metadata map[string]any) map[string]any {
	if metadata == nil {
		return map[string]any{}
	}
	data, err := json.Marshal(metadata)
	if err != nil {
		result := make(map[string]any, len(metadata))
		for key, value := range metadata {
			encoded, valueError := json.Marshal(value)
			if valueError != nil {
				result[key] = fmt.Sprint(value)
				continue
			}
			var normalized any
			if json.Unmarshal(encoded, &normalized) == nil {
				result[key] = normalized
			} else {
				result[key] = fmt.Sprint(value)
			}
		}
		return result
	}
	var clone map[string]any
	_ = json.Unmarshal(data, &clone)
	return clone
}
