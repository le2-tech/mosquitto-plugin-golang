package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"mosquitto-plugin/internal/pluginutil"
)

func TestDBAuth(t *testing.T) {
	origFetch := fetchAuthAccount
	t.Cleanup(func() { fetchAuthAccount = origFetch })

	type testCase struct {
		name       string
		username   string
		password   string
		clientID   string
		account    authAccount
		fetchErr   error
		wantAllow  bool
		wantReason string
		wantErr    error
		wantFetch  bool
	}

	tests := []testCase{
		{
			name:       "missing username",
			password:   "pwd",
			wantAllow:  false,
			wantReason: authReasonMissingCreds,
			wantFetch:  false,
		},
		{
			name:       "missing password",
			username:   "alice",
			wantAllow:  false,
			wantReason: authReasonMissingCreds,
			wantFetch:  false,
		},
		{
			name:       "user not found",
			username:   "alice",
			password:   "pwd",
			clientID:   "c1",
			fetchErr:   pgx.ErrNoRows,
			wantAllow:  false,
			wantReason: authReasonUserNotFound,
			wantFetch:  true,
		},
		{
			name:       "db error",
			username:   "alice",
			password:   "pwd",
			clientID:   "c1",
			fetchErr:   errors.New("db down"),
			wantAllow:  false,
			wantReason: authReasonDBError,
			wantErr:    errors.New("db down"),
			wantFetch:  true,
		},
		{
			name:       "user disabled",
			username:   "alice",
			password:   "pwd",
			clientID:   "c1",
			account:    authAccount{passwordHash: "x", salt: "s", enabled: 0},
			wantAllow:  false,
			wantReason: authReasonUserDisabled,
			wantFetch:  true,
		},
		{
			name:       "invalid password",
			username:   "alice",
			password:   "wrong",
			clientID:   "c1",
			account:    authAccount{passwordHash: pluginutil.SHA256PwdSalt("right", "salt"), salt: "salt", enabled: 1},
			wantAllow:  false,
			wantReason: authReasonInvalidPassword,
			wantFetch:  true,
		},
		{
			name:       "success",
			username:   "alice",
			password:   "right",
			clientID:   "c1",
			account:    authAccount{passwordHash: pluginutil.SHA256PwdSalt("right", "salt"), salt: "salt", enabled: 1},
			wantAllow:  true,
			wantReason: authReasonOK,
			wantFetch:  true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			called := false
			fetchAuthAccount = func(ctx context.Context, username, clientID string) (authAccount, error) {
				called = true
				if username != tc.username {
					t.Fatalf("username mismatch: got=%q want=%q", username, tc.username)
				}
				if clientID != tc.clientID {
					t.Fatalf("clientID mismatch: got=%q want=%q", clientID, tc.clientID)
				}
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("dbAuth should pass timeout context")
				}
				return tc.account, tc.fetchErr
			}

			allow, reason, err := dbAuth(tc.username, tc.password, tc.clientID)

			if allow != tc.wantAllow {
				t.Fatalf("allow mismatch: got=%v want=%v", allow, tc.wantAllow)
			}
			if reason != tc.wantReason {
				t.Fatalf("reason mismatch: got=%q want=%q", reason, tc.wantReason)
			}
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			} else if err == nil || err.Error() != tc.wantErr.Error() {
				t.Fatalf("error mismatch: got=%v want=%v", err, tc.wantErr)
			}
			if called != tc.wantFetch {
				t.Fatalf("fetch call mismatch: got=%v want=%v", called, tc.wantFetch)
			}
		})
	}
}

func TestRecordAuthEvent(t *testing.T) {
	origInsert := insertAuthEvent
	origTimeout := timeout
	t.Cleanup(func() {
		insertAuthEvent = origInsert
		timeout = origTimeout
	})

	timeout = 50 * time.Millisecond
	wantInfo := pluginutil.ClientInfo{
		ClientID: "c1",
		Username: "alice",
		Peer:     "127.0.0.1:1883",
		Protocol: "MQTT/5.0",
	}
	called := false
	insertAuthEvent = func(ctx context.Context, info pluginutil.ClientInfo, result, reason string) error {
		called = true
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("recordAuthEvent should pass timeout context")
		}
		if info != wantInfo {
			t.Fatalf("info mismatch: got=%+v want=%+v", info, wantInfo)
		}
		if result != authResultSuccess || reason != authReasonOK {
			t.Fatalf("result/reason mismatch: got=%q/%q", result, reason)
		}
		return nil
	}

	if err := recordAuthEvent(wantInfo, authResultSuccess, authReasonOK); err != nil {
		t.Fatalf("recordAuthEvent returned error: %v", err)
	}
	if !called {
		t.Fatal("insert hook should be called")
	}
}

func TestRecordAuthEventError(t *testing.T) {
	origInsert := insertAuthEvent
	t.Cleanup(func() { insertAuthEvent = origInsert })

	wantErr := errors.New("insert failed")
	insertAuthEvent = func(context.Context, pluginutil.ClientInfo, string, string) error {
		return wantErr
	}

	err := recordAuthEvent(pluginutil.ClientInfo{}, authResultFail, authReasonDBError)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error mismatch: got=%v want=%v", err, wantErr)
	}
}
