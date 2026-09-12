package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestUpdateShutdownAuthorizationAndIdentity(t *testing.T) {
	for _, test := range []struct {
		name, token, requestToken string
		enabled                   bool
		pid, want                 int
	}{
		{"missing auth", "secret", "", true, os.Getpid(), http.StatusUnauthorized},
		{"wrong auth", "secret", "wrong", true, os.Getpid(), http.StatusUnauthorized},
		{"no auth mode", "", "", true, os.Getpid(), http.StatusForbidden},
		{"service mode", "secret", "secret", false, os.Getpid(), http.StatusForbidden},
		{"restarted daemon", "secret", "secret", true, os.Getpid() + 1, http.StatusConflict},
		{"user daemon", "secret", "secret", true, os.Getpid(), http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			called := make(chan struct{}, 1)
			s := &Server{token: test.token}
			if test.enabled {
				s.SetUpdateShutdown(func() { called <- struct{}{} })
			}
			req := httptest.NewRequest(http.MethodPost, "/api/update/shutdown", strings.NewReader(fmt.Sprintf(`{"pid":%d}`, test.pid)))
			req.Header.Set("X-Auth-Token", test.requestToken)
			w := httptest.NewRecorder()
			s.auth(http.HandlerFunc(s.handleUpdateShutdown)).ServeHTTP(w, req)
			if w.Code != test.want {
				t.Fatalf("got %d, want %d", w.Code, test.want)
			}
			if test.want == http.StatusOK {
				select {
				case <-called:
				case <-time.After(time.Second):
					t.Fatal("shutdown not requested")
				}
			} else {
				select {
				case <-called:
					t.Fatal("unauthorized shutdown")
				default:
				}
			}
		})
	}
}
