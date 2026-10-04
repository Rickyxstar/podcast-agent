package sqs

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
)

// S3Object is an object named in an S3 event notification.
type S3Object struct {
	Bucket string
	// Key is the object key, already URL-decoded.
	Key string
	// ETag identifies the object's content, unquoted.
	ETag string
	Size int64
}

// s3Event is the notification S3 sends to SQS. See
// https://docs.aws.amazon.com/AmazonS3/latest/userguide/notification-content-structure.html
type s3Event struct {
	Records []struct {
		S3 struct {
			Bucket struct {
				Name string `json:"name"`
			} `json:"bucket"`
			Object struct {
				Key  string `json:"key"`
				Size int64  `json:"size"`
				ETag string `json:"eTag"`
			} `json:"object"`
		} `json:"s3"`
	} `json:"Records"`
	// Event is "s3:TestEvent" on the message S3 sends when the notification
	// is configured.
	Event string `json:"Event"`
}

// ParseS3Event returns the objects in an S3 event notification body. The
// test event S3 sends when a notification is configured has none.
func ParseS3Event(body string) ([]S3Object, error) {
	var ev s3Event
	if err := json.Unmarshal([]byte(body), &ev); err != nil {
		return nil, fmt.Errorf("parse S3 event: %w", err)
	}
	if ev.Event == "s3:TestEvent" {
		return nil, nil
	}
	if len(ev.Records) == 0 {
		return nil, errors.New("parse S3 event: no records")
	}
	objs := make([]S3Object, 0, len(ev.Records))
	for _, r := range ev.Records {
		// Keys are form-encoded: a space arrives as "+".
		key, err := url.QueryUnescape(r.S3.Object.Key)
		if err != nil {
			return nil, fmt.Errorf("parse S3 event: key %q: %w", r.S3.Object.Key, err)
		}
		objs = append(objs, S3Object{
			Bucket: r.S3.Bucket.Name,
			Key:    key,
			ETag:   r.S3.Object.ETag,
			Size:   r.S3.Object.Size,
		})
	}
	return objs, nil
}
