package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestHTTPProxy(t *testing.T) {
	// ProxyFromEnvironment caches its settings; use a fresh process for this test.
	if os.Getenv("FAVICON_PROXY_TEST") == "1" {
		client := newHTTPClient()
		defer client.CloseIdleConnections()
		for _, target := range []string{"http://public.invalid/icon", os.Getenv("FAVICON_DIRECT_TEST")} {
			res, err := client.Get(target)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(res.Body)
			res.Body.Close()
			if err != nil || string(body) != "icon" {
				t.Fatalf("unexpected response: %q, %v", body, err)
			}
		}
		return
	}
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.String() != "http://public.invalid/icon" {
			t.Errorf("unexpected proxy request: %s", r.URL)
		}
		_, _ = io.WriteString(w, "icon")
	}))
	defer proxy.Close()
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "icon")
	}))
	defer direct.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHTTPProxy$")
	for _, e := range os.Environ() {
		key := strings.ToUpper(strings.SplitN(e, "=", 2)[0])
		if key != "HTTP_PROXY" && key != "HTTPS_PROXY" && key != "NO_PROXY" && key != "REQUEST_METHOD" {
			cmd.Env = append(cmd.Env, e)
		}
	}
	cmd.Env = append(cmd.Env, "FAVICON_PROXY_TEST=1", "HTTP_PROXY="+proxy.URL, "NO_PROXY=127.0.0.1", "FAVICON_DIRECT_TEST="+direct.URL)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("proxy test: %v\n%s", err, out)
	}
}
