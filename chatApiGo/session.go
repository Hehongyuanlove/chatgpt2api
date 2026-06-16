package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

type User struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
	IDP   string `json:"idp"`
	IAT   int64  `json:"iat"`
	MFA   bool   `json:"mfa"`
}

type Account struct {
	ID                                                   string `json:"id"`
	PlanType                                             string `json:"planType"`
	Structure                                            string `json:"structure"`
	OrganizationID                                       string `json:"organizationId"`
	IsConversationClassifierEnabledForWorkspace          bool   `json:"isConversationClassifierEnabledForWorkspace"`
	IsFinservEnabledWorkspace                            bool   `json:"isFinservEnabledWorkspace"`
	IsFedrampCompliantWorkspace                          bool   `json:"isFedrampCompliantWorkspace"`
	IsDelinquent                                         bool   `json:"isDelinquent"`
	GracePeriodID                                        string `json:"gracePeriodId"`
	ResidencyRegion                                      string `json:"residencyRegion"`
	ComputeResidency                                     string `json:"computeResidency"`
}

type Session struct {
	AccessToken     string                 `json:"accessToken"`
	AccessTokenAlt  string                 `json:"access_token"`
	SessionToken    string                 `json:"sessionToken"`
	AuthProvider    string                 `json:"authProvider"`
	Expires         string                 `json:"expires"`
	User            *User                  `json:"user"`
	Account         *Account               `json:"account"`
	RUMViewTags     map[string]interface{} `json:"rumViewTags"`
	sessionPath     string
	sessionRaw      map[string]interface{}

	GeneratedUserAgent    string
	GeneratedDeviceID     string
	GeneratedSessionID    string
	GeneratedSecChUA      string
	GeneratedSecChUAMobile string
	GeneratedSecChUAPlatform string
}

const refreshThreshold = 24 * time.Hour

func (s *Session) Token() string {
	if s.AccessToken != "" {
		return s.AccessToken
	}
	return s.AccessTokenAlt
}

func decodeJWT(token string) map[string]interface{} {
	claims := map[string]interface{}{}
	if token == "" {
		return claims
	}
	parts := splitN(token, ".", 3)
	if len(parts) < 2 {
		return claims
	}
	payload := parts[1]
	switch len(payload) % 4 {
	case 2:
		payload += "=="
	case 3:
		payload += "="
	}
	decoded, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		return claims
	}
	json.Unmarshal(decoded, &claims)
	return claims
}

func splitN(s, sep string, n int) []string {
	out := make([]string, 0, n)
	start := 0
	for i := 0; i < n-1 && start < len(s); i++ {
		idx := indexOf(s[start:], sep)
		if idx < 0 {
			break
		}
		out = append(out, s[start:start+idx])
		start = start + idx + len(sep)
	}
	out = append(out, s[start:])
	return out
}

func indexOf(s, sep string) int {
	for i := 0; i <= len(s)-len(sep); i++ {
		if s[i:i+len(sep)] == sep {
			return i
		}
	}
	return -1
}

func jwtExpiresIn(token string) time.Duration {
	claims := decodeJWT(token)
	exp, _ := claims["exp"].(float64)
	if exp == 0 {
		return 0
	}
	remaining := time.Unix(int64(exp), 0).Sub(time.Now())
	if remaining < 0 {
		return 0
	}
	return remaining
}

func (s *Session) NeedsRefresh() bool {
	return jwtExpiresIn(s.Token()) <= refreshThreshold
}

func (s *Session) TokenClientID() string {
	claims := decodeJWT(s.Token())
	cid, _ := claims["client_id"].(string)
	return cid
}

func (s *Session) SetToken(newToken string) {
	s.AccessToken = newToken
	if s.sessionRaw != nil {
		s.sessionRaw["accessToken"] = newToken
	}
}

func (s *Session) SetSessionToken(newToken string) {
	s.SessionToken = newToken
	if s.sessionRaw != nil {
		s.sessionRaw["sessionToken"] = newToken
	}
}

func (s *Session) Save() error {
	if s.sessionPath == "" {
		return nil
	}
	data, err := json.MarshalIndent(s.sessionRaw, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal session: %w", err)
	}
	if err := os.WriteFile(s.sessionPath, data, 0644); err != nil {
		return fmt.Errorf("write session file: %w", err)
	}
	return nil
}

func LoadSession(path string) (*Session, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read session file: %w", err)
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse session JSON: %w", err)
	}

	var sess Session
	if err := json.Unmarshal(data, &sess); err != nil {
		return nil, fmt.Errorf("unmarshal session: %w", err)
	}

	sess.sessionPath = path
	sess.sessionRaw = raw

	sess.GeneratedUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/143.0.0.0 Safari/537.36 Edg/143.0.0.0"
	sess.GeneratedSecChUA = `"Microsoft Edge";v="143", "Chromium";v="143", "Not A(Brand";v="24"`
	sess.GeneratedSecChUAMobile = "?0"
	sess.GeneratedSecChUAPlatform = `"Windows"`

	return &sess, nil
}
