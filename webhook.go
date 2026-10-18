package popnow

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// WebhookDelivery is a verified task callback.
type WebhookDelivery struct {
	// TaskID is the task the signature was computed over. Use this one, not a
	// value you read out of the body yourself.
	TaskID string
	// DeliveryID is the envelope's request_id. It stays the same across
	// retries: store it with a unique index and ignore repeats.
	DeliveryID string
	// PayloadVersion is the X-Webhook-Payload-Version header, "2" for every
	// current integration.
	PayloadVersion string
	// Task is the task record carried by a version 2 delivery, the same shape
	// GetTask returns. Nil for version 1. Its signed links expire after about
	// twenty minutes; store the task ID, not the link.
	Task *Task
}

// taskCallbackBody is the smallest shape that locates the task ID in either
// payload version.
type taskCallbackBody struct {
	TaskID    string          `json:"task_id"`
	RequestID string          `json:"request_id"`
	Data      json.RawMessage `json:"data"`
}

// VerifyWebhook authenticates one callBackUrl delivery (a task callback).
//
// The signature is
//
//	base64( HMAC-SHA256( secret, "<taskId>.<timestamp>.<sha256_hex(rawBody)>" ) )
//
// where secret is the account's webhook signing secret (POPNOW_WEBHOOK_SECRET
// in the API console), timestamp is X-Webhook-Timestamp, and taskId comes
// from data.taskId in payload version 2 and from top-level task_id in the
// older version 1, as X-Webhook-Payload-Version says.
//
// rawBody must be the exact bytes that arrived. Decoding and re-encoding the
// JSON first changes key order, spacing and escaping, and the digest then
// fails every time for a reason that looks like a wrong secret.
//
// tolerance bounds the accepted clock skew; pass 0 for WebhookTolerance. Every
// retry is signed again with a fresh timestamp.
func VerifyWebhook(secret string, header http.Header, rawBody []byte, tolerance time.Duration) (*WebhookDelivery, error) {
	if secret == "" {
		return nil, fmt.Errorf("%w: no signing secret configured", ErrWebhookRejected)
	}
	version := strings.TrimSpace(header.Get("X-Webhook-Payload-Version"))
	timestamp := strings.TrimSpace(header.Get("X-Webhook-Timestamp"))
	signature := strings.TrimSpace(header.Get("X-Webhook-Signature"))
	if version != "1" && version != "2" {
		return nil, fmt.Errorf("%w: unsupported payload version %q", ErrWebhookRejected, version)
	}
	if timestamp == "" || signature == "" {
		return nil, fmt.Errorf("%w: missing timestamp or signature header", ErrWebhookRejected)
	}
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("%w: timestamp %q is not unix seconds", ErrWebhookRejected, timestamp)
	}

	// The task ID has to come out of an unverified payload, but only to recompute the signature:
	// a forged ID produces a signature that does not match.
	var body taskCallbackBody
	if err := json.Unmarshal(rawBody, &body); err != nil {
		return nil, fmt.Errorf("%w: body is not JSON", ErrWebhookRejected)
	}
	taskID := body.TaskID
	if version == "2" {
		var data struct {
			TaskID string `json:"taskId"`
		}
		_ = json.Unmarshal(body.Data, &data)
		taskID = data.TaskID
	}
	if taskID == "" {
		return nil, fmt.Errorf("%w: body carries no task ID for version %s", ErrWebhookRejected, version)
	}

	digest := sha256.Sum256(rawBody)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(taskID + "." + timestamp + "." + hex.EncodeToString(digest[:])))
	expected := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	// Constant-time comparison: an ordinary == returns at the first differing byte, and that
	// timing difference is enough to recover the signature one byte at a time.
	if !hmac.Equal([]byte(expected), []byte(signature)) {
		return nil, ErrWebhookSignature
	}

	// Freshness comes after the signature on purpose. A forger writes their own timestamp; judged
	// first, an old one would be reported as "stale", and the reasonable reaction to a screen full
	// of staleness is to widen the tolerance - exactly the knob that would let those deliveries
	// through. With the signature checked first, "stale" means nothing but clock skew.
	if err := checkFreshness(seconds, tolerance); err != nil {
		return nil, err
	}

	delivery := &WebhookDelivery{TaskID: taskID, DeliveryID: body.RequestID, PayloadVersion: version}
	if version == "2" && len(body.Data) > 0 {
		task := &Task{}
		if err := json.Unmarshal(body.Data, task); err == nil {
			delivery.Task = task
		}
	}
	return delivery, nil
}

// EndpointEvent is a verified account endpoint delivery.
type EndpointEvent struct {
	// ID is the webhook-id header. It stays the same across retries: use it
	// to ignore duplicates.
	ID string `json:"-"`
	// Type is task.completed, task.succeeded, task.failed, task.expired or
	// task.canceled. Treat the set as open.
	Type string `json:"type"`
	Data struct {
		// Object is the task record at the moment the event was created.
		Object *Task `json:"object"`
	} `json:"data"`
	// Raw is the verified body, for fields this struct does not name.
	Raw json.RawMessage `json:"-"`
}

// VerifyEndpointWebhook authenticates one delivery to an account endpoint
// registered in the API console. These follow the Standard Webhooks format:
//
//	webhook-signature: v1,base64( HMAC-SHA256( key, "<webhook-id>.<webhook-timestamp>.<rawBody>" ) )
//
// Two differences from VerifyWebhook: the raw body itself is signed (no
// SHA-256 step), and the HMAC key is the Base64-decoded part of the endpoint's
// whsec_ secret, not the secret string. During a secret rotation the header
// can hold several space-separated signatures; any match is accepted.
//
// tolerance bounds the accepted clock skew; pass 0 for WebhookTolerance.
func VerifyEndpointWebhook(secret string, header http.Header, rawBody []byte, tolerance time.Duration) (*EndpointEvent, error) {
	key, err := endpointKey(secret)
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(header.Get("webhook-id"))
	timestamp := strings.TrimSpace(header.Get("webhook-timestamp"))
	signatures := strings.TrimSpace(header.Get("webhook-signature"))
	if id == "" || timestamp == "" || signatures == "" {
		return nil, fmt.Errorf("%w: missing webhook-id, webhook-timestamp or webhook-signature header", ErrWebhookRejected)
	}
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("%w: timestamp %q is not unix seconds", ErrWebhookRejected, timestamp)
	}

	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(id + "." + timestamp + "."))
	mac.Write(rawBody)
	expected := []byte(base64.StdEncoding.EncodeToString(mac.Sum(nil)))

	matched := false
	for _, candidate := range strings.Fields(signatures) {
		scheme, value, ok := strings.Cut(candidate, ",")
		// Compare every candidate rather than stopping at the first hit, so timing does not reveal
		// which of the rotation signatures matched.
		if ok && scheme == "v1" && hmac.Equal(expected, []byte(value)) {
			matched = true
		}
	}
	if !matched {
		return nil, ErrWebhookSignature
	}
	if err := checkFreshness(seconds, tolerance); err != nil {
		return nil, err
	}

	event := &EndpointEvent{}
	if err := json.Unmarshal(rawBody, event); err != nil {
		return nil, fmt.Errorf("%w: body is not JSON", ErrWebhookRejected)
	}
	event.ID = id
	event.Raw = append(json.RawMessage(nil), rawBody...)
	return event, nil
}

// endpointKey turns a whsec_ secret into the HMAC key it stands for.
func endpointKey(secret string) ([]byte, error) {
	value := strings.TrimPrefix(strings.TrimSpace(secret), "whsec_")
	if value == "" {
		return nil, fmt.Errorf("%w: no endpoint secret configured", ErrWebhookRejected)
	}
	key, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("%w: endpoint secret is not Base64 after whsec_", ErrWebhookRejected)
	}
	return key, nil
}

// checkFreshness rejects a timestamp further than tolerance from now.
func checkFreshness(seconds int64, tolerance time.Duration) error {
	if tolerance <= 0 {
		tolerance = WebhookTolerance
	}
	if skew := time.Since(time.Unix(seconds, 0)); skew > tolerance || skew < -tolerance {
		return fmt.Errorf("%w: %s off", ErrWebhookStale, skew.Round(time.Second))
	}
	return nil
}
