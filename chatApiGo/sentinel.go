package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

type ChatRequirements struct {
	Token          string
	ProofToken     string
	TurnstileToken string
	SOToken        string
}

type prepareResponse struct {
	PrepareToken string `json:"prepare_token"`
	ProofOfWork  *struct {
		Required   bool   `json:"required"`
		Seed       string `json:"seed"`
		Difficulty string `json:"difficulty"`
	} `json:"proofofwork"`
	Turnstile *struct {
		Required bool   `json:"required"`
		DX       string `json:"dx"`
	} `json:"turnstile"`
	Arkose *struct {
		Required bool `json:"required"`
	} `json:"arkose"`
}

type finalizeResponse struct {
	Token   string `json:"token"`
	SOToken string `json:"so_token"`
}

func (c *Client) GetChatRequirements() (*ChatRequirements, error) {
	if err := c.ensureToken(); err != nil {
		return nil, fmt.Errorf("refresh token before sentinel: %w", err)
	}

	base := "/backend-api/sentinel/chat-requirements"
	if tok := c.session.Token(); tok == "" {
		base = "/backend-anon/sentinel/chat-requirements"
	}

	pToken := BuildLegacyRequirementsToken(c.userAgent, c.powScriptSources, c.powDataBuild)

	preparePath := base + "/prepare"
	prepareBody := map[string]string{"p": pToken}
	bodyJSON, _ := json.Marshal(prepareBody)

	req, err := c.buildReq("POST", "https://chatgpt.com"+preparePath,
		bytes.NewReader(bodyJSON),
		c.headers(preparePath, map[string]string{
			"Content-Type": "application/json",
		}))
	if err != nil {
		return nil, fmt.Errorf("build prepare req: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("prepare request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("prepare failed: HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var prepareResp prepareResponse
	if err := json.NewDecoder(resp.Body).Decode(&prepareResp); err != nil {
		return nil, fmt.Errorf("parse prepare response: %w", err)
	}

	if prepareResp.Arkose != nil && prepareResp.Arkose.Required {
		return nil, fmt.Errorf("chat requirements requires arkose token, which is not implemented")
	}

	var proofToken, turnstileToken string

	if prepareResp.ProofOfWork != nil && prepareResp.ProofOfWork.Required {
		proofToken, err = BuildProofToken(
			prepareResp.ProofOfWork.Seed,
			prepareResp.ProofOfWork.Difficulty,
			c.userAgent,
			c.powScriptSources,
			c.powDataBuild,
		)
		if err != nil {
			return nil, fmt.Errorf("solve proof of work: %w", err)
		}
	}

	if prepareResp.Turnstile != nil && prepareResp.Turnstile.Required && prepareResp.Turnstile.DX != "" {
		vm := NewTurnstileVM()
		turnstileToken, err = vm.Solve(prepareResp.Turnstile.DX, pToken)
		if err != nil {
			return nil, fmt.Errorf("solve turnstile: %w", err)
		}
	}

	finalizePath := base + "/finalize"
	finalizeBody := map[string]string{
		"prepare_token":   prepareResp.PrepareToken,
		"proof_token":     proofToken,
		"turnstile_token": turnstileToken,
	}
	finalizeJSON, _ := json.Marshal(finalizeBody)

	req2, err := c.buildReq("POST", "https://chatgpt.com"+finalizePath,
		bytes.NewReader(finalizeJSON),
		c.headers(finalizePath, map[string]string{
			"Content-Type": "application/json",
		}))
	if err != nil {
		return nil, fmt.Errorf("build finalize req: %w", err)
	}

	resp2, err := c.httpClient.Do(req2)
	if err != nil {
		return nil, fmt.Errorf("finalize request: %w", err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != 200 {
		fBody, _ := io.ReadAll(resp2.Body)
		return nil, fmt.Errorf("finalize failed: HTTP %d: %s", resp2.StatusCode, string(fBody))
	}

	var finalizeResp finalizeResponse
	if err := json.NewDecoder(resp2.Body).Decode(&finalizeResp); err != nil {
		return nil, fmt.Errorf("parse finalize response: %w", err)
	}

	if finalizeResp.Token == "" {
		return nil, fmt.Errorf("missing chat requirements token in response")
	}

	return &ChatRequirements{
		Token:          finalizeResp.Token,
		ProofToken:     proofToken,
		TurnstileToken: turnstileToken,
		SOToken:        finalizeResp.SOToken,
	}, nil
}
