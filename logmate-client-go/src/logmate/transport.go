package logmate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
)

func (c *Client) post(path string, payload any, instance, token, origin string, output any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return &requestError{message: fmt.Sprintf("could not convert the log to JSON: %v", err), permanent: true}
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL+path, bytes.NewReader(body))
	if err != nil {
		return &requestError{message: friendlyRequestError(err), permanent: true}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	if instance != "" {
		req.Header.Set("X-Sender-Instance-ID", instance)
		req.Header.Set("X-Sender-Instance-Token", token)
	}
	if origin != "" && origin != instance {
		req.Header.Set("X-LogMate-Origin-Instance-ID", origin)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return &requestError{message: friendlyRequestError(err)}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return apiError(resp)
	}
	if output != nil {
		var decoded any
		if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return &requestError{message: "the API returned a response that is not valid JSON.", permanent: true}
		}
		if _, ok := decoded.(map[string]any); !ok {
			return &requestError{message: "the API returned JSON, but the content is not an object.", permanent: true}
		}
		normalized, _ := json.Marshal(decoded)
		if err := json.Unmarshal(normalized, output); err != nil {
			return &requestError{message: friendlyRequestError(err), permanent: true}
		}
	}
	return nil
}

type requestError struct {
	message   string
	permanent bool
}

func (e *requestError) Error() string { return e.message }
func requestIsPermanent(err error) bool {
	var target *requestError
	return errors.As(err, &target) && target.permanent
}
func apiError(resp *http.Response) error {
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 32<<10)).Decode(&body)
	apiMessage := first(body.Error.Message, body.Message)
	message := apiMessage
	if resp.StatusCode == http.StatusUnauthorized || body.Error.Code == "INVALID_SENDER_KEY" || body.Error.Code == "INVALID_INSTANCE_TOKEN" {
		message = "the instance credential was rejected. Check LOGMATE_SENDER_NAME and restart the application to create a new instance."
	} else {
		switch resp.StatusCode {
		case http.StatusBadRequest:
			message = first(apiMessage, "the API rejected the submitted data. Check the log fields.")
		case http.StatusForbidden:
			message = "the provided key is not allowed to perform this operation."
		case http.StatusNotFound:
			message = "the API route was not found. Check LOGMATE_API_URL and the API version."
		case http.StatusRequestTimeout:
			message = "the API took too long to process the request."
		case http.StatusConflict:
			message = first(apiMessage, "the API encountered a conflict while processing the log.")
		case http.StatusRequestEntityTooLarge:
			message = "the log is too large to send. Reduce the message or metadata."
		case http.StatusUnprocessableEntity:
			message = first(apiMessage, "the API could not validate the submitted data.")
		case http.StatusTooManyRequests:
			message = "the API received too many requests and temporarily rate-limited sends."
		default:
			if resp.StatusCode >= 500 {
				message = "the LogMate server is unavailable or encountered an internal failure."
			}
		}
	}
	if message == "" {
		message = fmt.Sprintf("the API rejected the request with HTTP status %d.", resp.StatusCode)
	}
	retryable := resp.StatusCode == http.StatusRequestTimeout || resp.StatusCode == http.StatusTooEarly || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
	return &requestError{message: message, permanent: !retryable}
}

func friendlyRequestError(err error) string {
	var urlError *url.Error
	if errors.As(err, &urlError) && urlError.Err != nil {
		return friendlyRequestError(urlError.Err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "the API connection timed out."
	}
	var dnsError *net.DNSError
	if errors.As(err, &dnsError) {
		return "could not resolve the API address. Check the domain in LOGMATE_API_URL."
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return "the API connection timed out."
	}
	text := strings.TrimSpace(err.Error())
	if strings.Contains(strings.ToLower(text), "connection refused") {
		return "the API connection was refused. Check that the server is running and the port is correct."
	}
	if text == "" {
		return "could not connect to the API. Check LOGMATE_API_URL, the network, and the server."
	}
	return text
}
