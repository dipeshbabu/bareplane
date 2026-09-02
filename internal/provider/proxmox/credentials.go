package proxmox

import (
	"fmt"
	"os"
	"strings"
)

const (
	EnvTokenID     = "BAREPLANE_PROXMOX_TOKEN_ID"
	EnvTokenSecret = "BAREPLANE_PROXMOX_TOKEN_SECRET"
)

type Credentials struct {
	TokenID     string
	TokenSecret string
}

type LookupEnvFunc func(string) (string, bool)

func CredentialsFromEnv(lookup LookupEnvFunc) (Credentials, error) {
	if lookup == nil {
		lookup = os.LookupEnv
	}

	tokenID, ok := lookup(EnvTokenID)
	if !ok || strings.TrimSpace(tokenID) == "" {
		return Credentials{}, fmt.Errorf("required environment variable %s is not set", EnvTokenID)
	}
	tokenSecret, ok := lookup(EnvTokenSecret)
	if !ok || strings.TrimSpace(tokenSecret) == "" {
		return Credentials{}, fmt.Errorf("required environment variable %s is not set", EnvTokenSecret)
	}

	return Credentials{
		TokenID:     strings.TrimSpace(tokenID),
		TokenSecret: strings.TrimSpace(tokenSecret),
	}, nil
}

func (c Credentials) validate() error {
	if strings.TrimSpace(c.TokenID) == "" {
		return fmt.Errorf("token ID is required")
	}
	if strings.TrimSpace(c.TokenSecret) == "" {
		return fmt.Errorf("token secret is required")
	}
	return nil
}
