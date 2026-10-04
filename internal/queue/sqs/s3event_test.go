package sqs

import (
	"reflect"
	"testing"
)

func TestParseS3Event(t *testing.T) {
	body := `{"Records":[{"eventVersion":"2.1","eventSource":"aws:s3","eventName":"ObjectCreated:Put",
		"s3":{"bucket":{"name":"podcasts"},"object":{"key":"incoming/my+episode%281%29.json","size":42,"eTag":"abc123"}}}]}`
	got, err := ParseS3Event(body)
	if err != nil {
		t.Fatal(err)
	}
	want := []S3Object{{Bucket: "podcasts", Key: "incoming/my episode(1).json", ETag: "abc123", Size: 42}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestParseS3TestEvent(t *testing.T) {
	got, err := ParseS3Event(`{"Service":"Amazon S3","Event":"s3:TestEvent","Time":"2026-10-04T12:00:00.000Z","Bucket":"podcasts"}`)
	if err != nil || got != nil {
		t.Errorf("got %v, %v; want nil, nil", got, err)
	}
}

func TestParseS3EventErrors(t *testing.T) {
	for _, body := range []string{`not json`, `{}`, `{"Records":[]}`, `{"Records":[{"s3":{"object":{"key":"%zz"}}}]}`} {
		if _, err := ParseS3Event(body); err == nil {
			t.Errorf("ParseS3Event(%q) = nil error", body)
		}
	}
}
