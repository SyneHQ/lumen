package config

import (
	"context"
	"log"
	"os"

	infisical "github.com/infisical/go-sdk"
)

// LoadInfisicalSecrets initializes the official Infisical Go SDK
// and reads project secrets directly from the returned Secrets struct payload.
func LoadInfisicalSecrets() {
	clientID := os.Getenv("INFISICAL_CLIENT_ID")
	secret := os.Getenv("INFISICAL_CLIENT_SECRET")
	token := os.Getenv("INFISICAL_TOKEN")

	// If no Infisical credentials are provided, skip Infisical initialization.
	if clientID == "" && secret == "" && token == "" {
		return
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
			return
		}
	}

	// Fetch secrets payload directly from Infisical ListSecrets
	res, err := client.Secrets().ListSecrets(infisical.ListSecretsOptions{
		ProjectID:   projectID,
		Environment: env,
	})
	if err != nil {
		log.Printf("[Infisical] ❌ Failed to load secrets from Infisical: %v", err)
		return
	}

	// Read directly from res.Secrets struct slice
	loadedCount := 0
	for _, sec := range res.Secrets {
		if sec.SecretKey != "" && sec.SecretValue != "" {
			_ = os.Setenv(sec.SecretKey, sec.SecretValue)
			loadedCount++
		}
	}

	log.Printf("[Infisical] 🎉 Successfully loaded %d secrets from Infisical payload into process env.", loadedCount)
}
