package main

import "testing"

func TestHTTPAccessLogEnabled(t *testing.T) {
	for _, value := range []string{"0", "false", "OFF", "no"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("LEVARA_HTTP_ACCESS_LOG", value)
			if httpAccessLogEnabled() {
				t.Fatalf("access log enabled for %q", value)
			}
		})
	}
	t.Setenv("LEVARA_HTTP_ACCESS_LOG", "")
	if !httpAccessLogEnabled() {
		t.Fatal("access log must remain enabled by default")
	}
}
