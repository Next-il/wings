package tokens

import (
	"errors"
	"testing"
	"time"

	"github.com/gbrlsnchs/jwt/v3"

	"github.com/pelican/wings/config"
)

const (
	shellTestServer = "6d6f1b5e-1f4b-4c39-9a41-1d2f0f3c0a11"
	shellTestUser   = "00000000-0000-4000-8000-0000000e7d05"
)

// consolePermissions are exactly what NextHost puts in every console token, a customer's included.
var consolePermissions = []string{"*", "admin.websocket.errors", "admin.websocket.install", "admin.websocket.transfer"}

type shellClaims struct {
	jwt.Payload
	UserUUID    string   `json:"user_uuid"`
	ServerUUID  string   `json:"server_uuid"`
	Permissions []string `json:"permissions"`
	Scope       string   `json:"scope"`
	SessionID   string   `json:"session_id,omitempty"`
}

func setupShellTokens(t *testing.T) {
	t.Helper()
	config.Set(&config.Configuration{AuthenticationToken: "shell-test-token"})
	resetDenylistState(t, time.Now().Add(-time.Hour))
}

func signShellClaims(t *testing.T, mutate func(*shellClaims)) []byte {
	t.Helper()
	now := time.Now()
	claims := shellClaims{
		Payload: jwt.Payload{
			IssuedAt:       jwt.NumericDate(now),
			ExpirationTime: jwt.NumericDate(now.Add(2 * time.Minute)),
			JWTID:          t.Name() + now.String(),
		},
		UserUUID:    shellTestUser,
		ServerUUID:  shellTestServer,
		Permissions: []string{PermissionShell},
		Scope:       string(Shell),
		SessionID:   "session-1",
	}
	if mutate != nil {
		mutate(&claims)
	}
	token, err := jwt.Sign(claims, config.GetJwtAlgorithm())
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestShellTokenOpensOneSession(t *testing.T) {
	setupShellTokens(t)
	token := signShellClaims(t, nil)

	payload, err := ParseShellToken(token, shellTestServer)
	if err != nil {
		t.Fatalf("expected the shell token to be accepted: %v", err)
	}
	if payload.SessionID != "session-1" || payload.UserUUID != shellTestUser {
		t.Fatalf("unexpected payload: %+v", payload)
	}

	if _, err := ParseShellToken(token, shellTestServer); !errors.Is(err, ErrShellReused) {
		t.Fatalf("expected a spent token to be refused, got %v", err)
	}
}

func TestConsoleTokenCannotOpenShell(t *testing.T) {
	setupShellTokens(t)

	cases := map[string]struct {
		mutate func(*shellClaims)
		want   error
	}{
		"a console token as NextHost mints it": {func(c *shellClaims) {
			c.Scope = string(Websocket)
			c.Permissions = consolePermissions
		}, ErrShellScope},
		"a console token that also lists the shell permission": {func(c *shellClaims) {
			c.Scope = string(Websocket)
			c.Permissions = []string{"*", "admin.websocket.errors", PermissionShell}
		}, ErrShellScope},
		"the console's permissions in the shell scope": {func(c *shellClaims) {
			c.Permissions = consolePermissions
		}, ErrShellPermission},
		"both scopes at once": {func(c *shellClaims) {
			c.Scope = "websocket shell"
		}, ErrShellScope},
		"no permissions": {func(c *shellClaims) {
			c.Permissions = nil
		}, ErrShellPermission},
		"a file download token": {func(c *shellClaims) {
			c.Scope = string(FileDownload)
		}, ErrShellScope},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseShellToken(signShellClaims(t, c.mutate), shellTestServer); !errors.Is(err, c.want) {
				t.Fatalf("expected %v, got %v", c.want, err)
			}
		})
	}
}

func TestWildcardNeverGrantsShell(t *testing.T) {
	p := WebsocketPayload{
		Payload:     jwt.Payload{IssuedAt: jwt.NumericDate(time.Now())},
		ServerUUID:  shellTestServer,
		UserUUID:    shellTestUser,
		Permissions: []string{"*"},
	}
	resetDenylistState(t, time.Now().Add(-time.Hour))
	if p.HasPermission(PermissionShell) {
		t.Fatal(`"*" grants the shell permission to a console token`)
	}
	if !p.HasPermission("control.console") {
		t.Fatal(`"*" should still grant ordinary console permissions`)
	}
}

func TestShellTokenCannotOpenConsole(t *testing.T) {
	setupShellTokens(t)

	var payload WebsocketPayload
	if err := ParseToken(signShellClaims(t, nil), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.HasScope(Websocket) {
		t.Fatal("a shell token carries the websocket scope")
	}
	if payload.HasPermission("websocket.connect") {
		t.Fatal("a shell token may connect to the console")
	}
}

func TestShellTokenRefusals(t *testing.T) {
	setupShellTokens(t)

	t.Run("another server", func(t *testing.T) {
		if _, err := ParseShellToken(signShellClaims(t, nil), "11111111-2222-4333-8444-555555555555"); !errors.Is(err, ErrShellServer) {
			t.Fatalf("expected ErrShellServer, got %v", err)
		}
	})

	t.Run("no server", func(t *testing.T) {
		token := signShellClaims(t, func(c *shellClaims) { c.ServerUUID = "" })
		if _, err := ParseShellToken(token, shellTestServer); !errors.Is(err, ErrShellServer) {
			t.Fatalf("expected ErrShellServer, got %v", err)
		}
	})

	t.Run("expired", func(t *testing.T) {
		token := signShellClaims(t, func(c *shellClaims) {
			c.IssuedAt = jwt.NumericDate(time.Now().Add(-10 * time.Minute))
			c.ExpirationTime = jwt.NumericDate(time.Now().Add(-time.Minute))
		})
		if _, err := ParseShellToken(token, shellTestServer); !errors.Is(err, jwt.ErrExpValidation) {
			t.Fatalf("expected an expired token to be refused, got %v", err)
		}
	})

	t.Run("no expiry", func(t *testing.T) {
		token := signShellClaims(t, func(c *shellClaims) { c.ExpirationTime = nil })
		if _, err := ParseShellToken(token, shellTestServer); err == nil {
			t.Fatal("expected a token without exp to be refused")
		}
	})

	t.Run("lives longer than a spent token is remembered", func(t *testing.T) {
		token := signShellClaims(t, func(c *shellClaims) {
			c.ExpirationTime = jwt.NumericDate(time.Now().Add(2 * time.Hour))
		})
		if _, err := ParseShellToken(token, shellTestServer); !errors.Is(err, ErrShellLifetime) {
			t.Fatalf("expected ErrShellLifetime, got %v", err)
		}
	})

	t.Run("issued before wings booted", func(t *testing.T) {
		resetDenylistState(t, time.Now().Add(time.Minute))
		if _, err := ParseShellToken(signShellClaims(t, nil), shellTestServer); !errors.Is(err, ErrShellDenylisted) {
			t.Fatalf("expected ErrShellDenylisted, got %v", err)
		}
	})

	t.Run("revoked for the user", func(t *testing.T) {
		resetDenylistState(t, time.Now().Add(-time.Hour))
		token := signShellClaims(t, func(c *shellClaims) { c.IssuedAt = jwt.NumericDate(time.Now().Add(-time.Minute)) })
		DenyForServer(shellTestServer, shellTestUser)
		if _, err := ParseShellToken(token, shellTestServer); !errors.Is(err, ErrShellDenylisted) {
			t.Fatalf("expected ErrShellDenylisted, got %v", err)
		}
	})

	t.Run("no jti", func(t *testing.T) {
		token := signShellClaims(t, func(c *shellClaims) { c.JWTID = "" })
		if _, err := ParseShellToken(token, shellTestServer); !errors.Is(err, ErrShellDenylisted) {
			t.Fatalf("expected ErrShellDenylisted, got %v", err)
		}
	})

	t.Run("signed with another node's token", func(t *testing.T) {
		claims := shellClaims{
			Payload: jwt.Payload{
				IssuedAt:       jwt.NumericDate(time.Now()),
				ExpirationTime: jwt.NumericDate(time.Now().Add(time.Minute)),
				JWTID:          "other-node",
			},
			ServerUUID: shellTestServer, UserUUID: shellTestUser,
			Permissions: []string{PermissionShell}, Scope: string(Shell),
		}
		token, _ := jwt.Sign(claims, jwt.NewHS256([]byte("another-node-token")))
		if _, err := ParseShellToken(token, shellTestServer); err == nil {
			t.Fatal("expected a token signed with another secret to be refused")
		}
	})
}
