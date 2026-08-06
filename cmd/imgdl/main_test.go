package main

import (
	"context"
	"reflect"
	"testing"

	"github.com/mtgban/simplecloud"
)

func TestParseSetsEmpty(t *testing.T) {
	got := parseSets("")
	if got != nil {
		t.Errorf("parseSets(\"\") = %v, want nil", got)
	}
}

func TestParseSetsUppercases(t *testing.T) {
	got := parseSets("neo,vow, MID")
	want := map[string]bool{"NEO": true, "VOW": true, "MID": true}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseSets = %v, want %v", got, want)
	}
}

func TestOpenBucketLocalDir(t *testing.T) {
	bucket, base, err := openBucket(context.Background(), "./tmp-mirror")
	if err != nil {
		t.Fatalf("openBucket local dir: %v", err)
	}
	if _, ok := bucket.(*simplecloud.FileBucket); !ok {
		t.Errorf("openBucket local dir returned %T, want *simplecloud.FileBucket", bucket)
	}
	if base != "./tmp-mirror" {
		t.Errorf("openBucket local dir base = %q, want %q", base, "./tmp-mirror")
	}
}

func TestOpenBucketRejectsWindowsAbsolutePath(t *testing.T) {
	_, _, err := openBucket(context.Background(), "C:/x")
	if err == nil {
		t.Fatal("openBucket(C:/x) = nil error, want rejection")
	}
}

func TestOpenBucketB2RequiresCreds(t *testing.T) {
	t.Setenv("B2_ACCESS_KEY", "")
	t.Setenv("B2_ACCESS_SECRET", "")
	_, _, err := openBucket(context.Background(), "b2://mybucket/prefix")
	if err == nil {
		t.Fatal("openBucket(b2://... without creds) = nil error, want error")
	}
}
