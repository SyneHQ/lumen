package config

import (
	"context"
	"log"
	"os"

	infisical "github.com/infisical/go-sdk"
)

// FetchInfisicalSecrets initializes the official Infisical Go SDK
// and returns secrets directly in memory without mutating process environment variables.
func FetchInfisicalSecrets() map[string]string {
	clientID := os.Getenv("INFISICAL_CLIENT_ID")
	secret := os.Getenv("INFISICAL_CLIENT_SECRET")
	token := os.Getenv("INFISICAL_TOKEN")

	// If no Infisical credentials are provided, skip Infisical initialization.
	if clientID == "" && secret == "" && token == "" {
		return nil
	}

	siteURL := os.Getenv("INFISICAL_API_URL")
	if siteURL == "" {
		siteURL = "https://app.infisical.com"
	}

	env := os.Getenv("INFISICAL_ENV")
	if env == "" {
		env = "prod"
	}

	projectID := os.Getenv("INFISICAL_PROJECT_ID")

	log.Printf("[Infisical] 🔑 Initializing Infisical client for %s (env: %s)...", siteURL, env)

	client := infisical.NewInfisicalClient(context.Background(), infisical.Config{
		SiteUrl:          siteURL,
		AutoTokenRefresh: true,
	})

	if token != "" {
		client.Auth().SetAccessToken(token)
	} else if clientID != "" && secret != "" {
		_, err := client.Auth().UniversalAuthLogin(clientID, secret)
		if err != nil {
			log.Printf("[Infisical] ❌ Universal Auth login failed: %v", err)
			return nil
		}
	}

	// Fetch secrets payload directly from Infisical ListSecrets
	res, err := client.Secrets().ListSecrets(infisical.ListSecretsOptions{
		ProjectID:   projectID,
		Environment: env,
	})
	if err != nil {
		log.Printf("[Infisical] ❌ Failed to load secrets from Infisical: %v", err)
		return nil
	}

	secretsMap := make(map[string]string)
	for _, sec := range res.Secrets {
		if sec.SecretKey != "" && sec.SecretValue != "" {
			secretsMap[sec.SecretKey] = sec.SecretValue
		}
	}

	log.Printf("[Infisical] 🎉 Successfully loaded %d secrets from Infisical payload directly into memory.", len(secretsMap))
	return secretsMap
}
