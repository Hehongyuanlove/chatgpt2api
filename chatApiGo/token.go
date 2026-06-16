package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/url"
	"strings"
	"time"

	http "github.com/bogdanfinn/fhttp"
)

type tokenRefreshResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
}

func (c *Client) RefreshToken() error {
	sess := c.session
	if sess.SessionToken == "" {
		return fmt.Errorf("no sessionToken available for refresh")
	}

	clientID := sess.TokenClientID()
	if clientID == "" {
		clientID = "app_2SKx67EdpoN0G6j64rFvigXD"
	}

	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {sess.SessionToken},
		"client_id":     {clientID},
	}
	body := strings.NewReader(form.Encode())

	req, err := http.NewRequest("POST", "https://auth.openai.com/oauth/token", body)
	if err != nil {
		return fmt.Errorf("create refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("refresh request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return fmt.Errorf("refresh failed: HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var tr tokenRefreshResponse
	if err := json.Unmarshal(respBody, &tr); err != nil {
		return fmt.Errorf("parse refresh response: %w", err)
	}
	if tr.AccessToken == "" {
		return fmt.Errorf("refresh response missing access_token")
	}

	sess.SetToken(tr.AccessToken)
	if tr.RefreshToken != "" {
		sess.SetSessionToken(tr.RefreshToken)
	}
	if tr.IDToken != "" {
		if sess.sessionRaw != nil {
			sess.sessionRaw["idToken"] = tr.IDToken
		}
	}

	if err := sess.Save(); err != nil {
		return fmt.Errorf("save session after refresh: %w", err)
	}

	return nil
}

func (c *Client) ensureToken() error {
	if !c.session.NeedsRefresh() {
		return nil
	}
	if c.session.SessionToken == "" {
		return fmt.Errorf("token expired and no sessionToken for refresh")
	}

	log.Println("Token expires soon, refreshing...")
	if err := c.RefreshToken(); err != nil {
		return err
	}
	log.Println("Token refreshed and saved to session file")
	time.Sleep(2 * time.Second)
	return nil
}

