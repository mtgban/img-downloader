package web_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mtgban/img-downloader/internal/web"
)

func TestGetSendsUserAgent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.WriteString(w, r.Header.Get("User-Agent"))
		if err != nil {
			t.Error(err)
		}
	}))
	defer srv.Close()

	resp, err := web.Get(context.Background(), nil, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	if string(got) != web.UserAgent {
		t.Errorf("User-Agent = %q, want %q", got, web.UserAgent)
	}
}

func TestGetRejectsNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	resp, err := web.Get(context.Background(), srv.Client(), srv.URL+"/bulk")
	if err == nil {
		resp.Body.Close()
		t.Fatal("Get succeeded on a 503")
	}
	if !strings.Contains(err.Error(), srv.URL+"/bulk") || !strings.Contains(err.Error(), "503") {
		t.Errorf("error %q should name the url and the status", err)
	}
}
