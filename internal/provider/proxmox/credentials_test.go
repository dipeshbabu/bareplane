package proxmox

import (
	"strings"
	"testing"
)

func TestCredentialsFromEnv(t *testing.T) {
	values := map[string]string{
		EnvTokenID:     "root@pam!bareplane",
		EnvTokenSecret: "secret-value",
	}
	credentials, err := CredentialsFromEnv(func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	})
	if err != nil {
		t.Fatalf("CredentialsFromEnv() error = %v", err)
	}
	if credentials.TokenID != values[EnvTokenID] || credentials.TokenSecret != values[EnvTokenSecret] {
		t.Fatalf("unexpected credentials: %#v", credentials)
	}
}

func TestCredentialsFromEnvReportsMissingVariableWithoutSecrets(t *testing.T) {
	_, err := CredentialsFromEnv(func(key string) (string, bool) {
		if key == EnvTokenID {
			return "root@pam!bareplane", true
		}
		return "", false
	})
	if err == nil || !strings.Contains(err.Error(), EnvTokenSecret) {
		t.Fatalf("expected missing variable error, got %v", err)
	}
	if strings.Contains(err.Error(), "root@pam!bareplane") {
		t.Fatalf("credential value leaked in error: %v", err)
	}
}
