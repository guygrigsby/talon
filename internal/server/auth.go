package server

import (
	"crypto/subtle"
	"fmt"
)

type AuthMode string

const (
	AuthNone          AuthMode = "none"
	AuthToken         AuthMode = "token"
	AuthPassword      AuthMode = "password"
	AuthTrustedProxy  AuthMode = "trusted-proxy"
)

type AuthConfig struct {
	Mode  AuthMode
	Token string
}

type AuthResult struct {
	Role   string
	Scopes []string
}

func (a AuthConfig) Authorize(p *ConnectParams) (AuthResult, *FrameError) {
	role := p.Role
	if role == "" {
		role = "operator"
	}
	scopes := p.Scopes
	if len(scopes) == 0 {
		scopes = []string{"operator.read", "operator.write"}
	}

	switch a.Mode {
	case "", AuthNone:
		return AuthResult{Role: role, Scopes: scopes}, nil
	case AuthToken:
		if a.Token == "" {
			return AuthResult{}, &FrameError{Code: ErrCodeInternal, Message: "server has auth=token but no token configured"}
		}
		got := ""
		if p.Auth != nil {
			got = p.Auth.Token
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(a.Token)) != 1 {
			return AuthResult{}, &FrameError{Code: ErrCodeUnauthorized, Message: "invalid or missing auth token"}
		}
		return AuthResult{Role: role, Scopes: scopes}, nil
	case AuthPassword, AuthTrustedProxy:
		return AuthResult{}, &FrameError{Code: ErrCodeInternal, Message: fmt.Sprintf("auth mode %q not yet supported", a.Mode)}
	default:
		return AuthResult{}, &FrameError{Code: ErrCodeInternal, Message: fmt.Sprintf("unknown auth mode %q", a.Mode)}
	}
}
