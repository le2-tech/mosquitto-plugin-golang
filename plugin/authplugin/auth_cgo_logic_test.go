package main

import (
	"errors"
	"testing"

	"mosquitto-plugin/internal/pluginutil"
)

func TestRunBasicAuth(t *testing.T) {
	origDBAuth := dbAuthFn
	origRecord := recordAuthEventFn
	origInfoLogger := infoLogger
	origWarnLogger := warnLogger
	origFailOpen := failOpen
	t.Cleanup(func() {
		dbAuthFn = origDBAuth
		recordAuthEventFn = origRecord
		infoLogger = origInfoLogger
		warnLogger = origWarnLogger
		failOpen = origFailOpen
	})
	infoLogger = func(string, map[string]any) {}
	warnLogger = func(string, map[string]any) {}

	type testCase struct {
		name            string
		failOpenValue   bool
		dbAllow         bool
		dbReason        string
		dbErr           error
		recordErr       error
		wantCode        int
		wantEventResult string
		wantEventReason string
	}

	tests := []testCase{
		{
			name:            "allow",
			failOpenValue:   false,
			dbAllow:         true,
			dbReason:        authReasonOK,
			wantCode:        int(authResultCode(true)),
			wantEventResult: authResultSuccess,
			wantEventReason: authReasonOK,
		},
		{
			name:            "deny",
			failOpenValue:   false,
			dbAllow:         false,
			dbReason:        authReasonInvalidPassword,
			wantCode:        int(authResultCode(false)),
			wantEventResult: authResultFail,
			wantEventReason: authReasonInvalidPassword,
		},
		{
			name:            "db error fail closed",
			failOpenValue:   false,
			dbErr:           errors.New("db down"),
			wantCode:        int(authResultCode(false)),
			wantEventResult: authResultFail,
			wantEventReason: authReasonDBError,
		},
		{
			name:            "db error fail open",
			failOpenValue:   true,
			dbErr:           errors.New("db down"),
			wantCode:        int(authResultCode(true)),
			wantEventResult: authResultSuccess,
			wantEventReason: authReasonDBErrorFailOpen,
		},
		{
			name:            "record error does not change decision",
			failOpenValue:   false,
			dbAllow:         true,
			dbReason:        authReasonOK,
			recordErr:       errors.New("insert fail"),
			wantCode:        int(authResultCode(true)),
			wantEventResult: authResultSuccess,
			wantEventReason: authReasonOK,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			failOpen = tc.failOpenValue
			dbAuthFn = func(username, password, clientID string) (bool, string, error) {
				if username != "alice" || password != "pwd" || clientID != "c1" {
					t.Fatalf("unexpected args: %q %q %q", username, password, clientID)
				}
				return tc.dbAllow, tc.dbReason, tc.dbErr
			}

			called := false
			recordAuthEventFn = func(info pluginutil.ClientInfo, result, reason string) error {
				called = true
				if info.ClientID != "c1" || info.Username != "alice" {
					t.Fatalf("unexpected info: %+v", info)
				}
				if result != tc.wantEventResult || reason != tc.wantEventReason {
					t.Fatalf("event mismatch: got %q/%q want %q/%q", result, reason, tc.wantEventResult, tc.wantEventReason)
				}
				return tc.recordErr
			}

			got := runBasicAuth(pluginutil.ClientInfo{
				ClientID: "c1",
				Username: "alice",
			}, "pwd")

			if int(got) != tc.wantCode {
				t.Fatalf("code mismatch: got=%d want=%d", int(got), tc.wantCode)
			}
			if !called {
				t.Fatal("recordAuthEvent should be called")
			}
		})
	}
}

func TestBasicAuthCbNilEventData(t *testing.T) {
	origWarnLogger := warnLogger
	t.Cleanup(func() {
		warnLogger = origWarnLogger
	})
	warnLogger = func(string, map[string]any) {}

	got := basic_auth_cb_c(0, nil, nil)
	want := int(authResultCode(false))
	if int(got) != want {
		t.Fatalf("code mismatch for nil event_data: got=%d want=%d", int(got), want)
	}
}
