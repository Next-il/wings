package tokens

import (
	"slices"
	"time"

	"emperror.dev/errors"
	"github.com/gbrlsnchs/jwt/v3"
)

// NextHost: the staff shell (router/router_server_shell.go).
const (
	// Shell is the scope of a shell token. It is the token's only scope: a token that also
	// carries "websocket" is refused, so no single token opens both the console and a shell.
	Shell = JwtScope("shell")

	// PermissionShell is the one permission that opens a shell. It has to be listed as is:
	// "*" never grants it (it would not anyway - "*" skips every "admin" permission, see
	// WebsocketPayload.HasPermission). NextHost mints it only for its staff, in a token of the
	// shell scope, and never puts it in a console token.
	PermissionShell = "admin.shell"

	// MaxShellTokenLifetime is the longest a shell token may be valid for. A spent JTI is
	// remembered for an hour (TokenStore), so a token must expire well before it is forgotten.
	MaxShellTokenLifetime = 15 * time.Minute
)

var (
	ErrShellScope      = errors.New("jwt: not a shell token")
	ErrShellPermission = errors.New("jwt: missing shell permission")
	ErrShellServer     = errors.New("jwt: shell token is for another server")
	ErrShellDenylisted = errors.New("jwt: shell token issued before wings booted or revoked")
	ErrShellReused     = errors.New("jwt: shell token already used")
	ErrShellLifetime   = errors.New("jwt: shell token lives too long")
)

// ShellPayload is the JWT a staff shell socket authenticates with, signed with the node token
// like the console's. It opens one session: the JTI is spent on first use.
type ShellPayload struct {
	jwt.Payload
	Scoped

	UserUUID    string   `json:"user_uuid"`
	ServerUUID  string   `json:"server_uuid"`
	Permissions []string `json:"permissions"`
	// SessionID is NextHost's name for the session. It is reported back with the session's
	// activity so NextHost can tie the close to whoever opened it.
	SessionID string `json:"session_id"`
}

// GetPayload returns the JWT payload.
func (p *ShellPayload) GetPayload() *jwt.Payload {
	return &p.Payload
}

// Authorize checks that the token may open a shell on the given server. It does not check the
// signature or the expiry (ParseToken does) and does not spend the token (ParseShellToken does).
func (p *ShellPayload) Authorize(serverUUID string) error {
	if p.Scope != string(Shell) {
		return ErrShellScope
	}
	if !slices.Contains(p.Permissions, PermissionShell) {
		return ErrShellPermission
	}
	if p.ServerUUID == "" || p.ServerUUID != serverUUID {
		return ErrShellServer
	}
	if p.JWTID == "" || isDenylisted(&p.Payload, p.ServerUUID, p.UserUUID) {
		return ErrShellDenylisted
	}
	if p.ExpirationTime == nil || p.ExpirationTime.Sub(p.IssuedAt.Time) > MaxShellTokenLifetime {
		return ErrShellLifetime
	}
	return nil
}

// ParseShellToken verifies a shell token for the given server and spends it, so it can never
// open a second session.
func ParseShellToken(token []byte, serverUUID string) (*ShellPayload, error) {
	var payload ShellPayload
	if err := ParseToken(token, &payload); err != nil {
		return nil, err
	}
	if err := payload.Authorize(serverUUID); err != nil {
		return nil, err
	}
	if !getTokenStore().IsValidToken("shell:" + payload.JWTID) {
		return nil, ErrShellReused
	}
	return &payload, nil
}
