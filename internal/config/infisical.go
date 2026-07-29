package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// InfisicalClient handles fetching runtime secrets from Infisical API.
type InfisicalClient struct {
	apiURL     string
	clientID   string
	secret     string
	token      string
	projectID  string
	env        string
	httpClient *http.Client
}

type infisicalAuthResponse struct {
	AccessToken string `json:"accessToken"`
}

type infisicalSecret struct {
	SecretKey   string `json:"secretKey"`
	SecretValue string `json:"secretValue"`
}

type infisicalSecretsResponse struct {
	Secrets []infisicalSecret `json:"secrets"`
}

// LoadInfisicalSecrets checks if Infisical environment variables are set and populates process env.
func LoadInfisicalSecrets() {
	clientID := os.Getenv("INFISICAL_CLIENT_ID")
	secret := os.Getenv("INFISICAL_CLIENT_SECRET")
	token := os.Getenv("INFISICAL_TOKEN")

	// If no Infisical auth credentials are provided, skip Infisical secret loading.
	if clientID == "" && secret == "" && token == "" {
		return
	}

	apiURL := os.Getenv("INFISICAL_API_URL")
	if apiURL == "" {
		apiURL = "https://app.infisical.com"
	}
	apiURL = strings.TrimSuffix(apiURL, "/")

	env := os.Getenv("INFISICAL_ENV")
	if env == "" {
		env = "prod"
	}

	projectID := os.Getenv("INFISICAL_PROJECT_ID")

	client := &InfisicalClient{
		apiURL:     apiURL,
		clientID:   clientID,
		secret:     secret,
		token:      token,
		projectID:  projectID,
		env:        env,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}

	log.Printf("[Infisical] Auth credentials detected. Fetching secrets from %s (env: %s)...", apiURL, env)

	secrets, err := client.FetchSecrets()
	if err != nil {
		log.Printf("[Infisical] Warning: Failed to fetch secrets from Infisical: %v. Falling back to local env vars.", err)
		return
	}

	loadedCount := 0
	for key, val := range secrets {
		// Populate process environment if not already explicitly overridden
		if val != "" {
			_ = os.Setenv(key, val)
			loadedCount++
		}
	}

	log.Printf("[Infisical] Successfully loaded %d secrets from Infisical.", loadedCount)
}

// FetchSecrets retrieves raw secrets from Infisical API using Universal Auth or Service Token.
func (c *InfisicalClient) FetchSecrets() (map[string]string, error) {
	authToken := c.token

	// If Universal Auth client ID & secret are provided, acquire access token first
	if authToken == "" && c.clientID != "" && c.secret != "" {
		tok, err := c.authenticateUniversalAuth()
		if err != nil {
			return nil, fmt.Errorf("universal auth failed: %w", err)
		}
		authToken = tok
	}

	if authToken == "" {
		return nil, fmt.Errorf("missing Infisical authorization token")
	}

	// Fetch raw secrets endpoint
	reqURL := fmt.Sprintf("%s/api/v3/secrets/raw?environment=%s", c.apiURL, c.env)
	if c.projectID != "" {
		reqURL += fmt.Sprintf("&workspaceId=%s", c.projectID)
	}

	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+authToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("infisical API returned status %d: %s", resp.StatusCode, string(body))
	}

	var secResp infisicalSecretsResponse
	if err := json.NewDecoder(resp.Body).Decode(&secResp); err != nil {
		return nil, fmt.Errorf("failed to decode secrets json: %w", err)
	}

	result := make(map[string]string)
	for _, sec := range secResp.Secrets {
		result[sec.SecretKey] = sec.SecretValue
	}

	return result, nil
}

// authenticateUniversalAuth exchanges Client ID and Client Secret for a Bearer token.
func (c *InfisicalClient) authenticateUniversalAuth() (string, error) {
	loginURL := fmt.Sprintf("%s/api/v1/auth/universal-auth/login", c.apiURL)

	payload := map[string]string{
		"clientId":     c.clientID,
		"clientSecret": c.secret,
	}

	jsonBytes, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest("POST", loginURL, bytes.NewBuffer(jsonBytes))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("universal auth endpoint status %d: %s", resp.StatusCode, string(body))
	}

	var authResp infisicalAuthResponse
	if err := json.NewDecoder(resp.Body).Decode(&authResp); err != nil {
		return "", err
	}

	return authResp.AccessToken, nil
}
