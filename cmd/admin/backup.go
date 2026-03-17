package main

import (
	"context"
	"log"
	"os"
)

// SecretBackend abstracts user data backup/restore.
// Implementations are auto-detected from environment variables.
type SecretBackend interface {
	Save(ctx context.Context, data []byte) error
	Load(ctx context.Context) ([]byte, error)
}

func initBackend() SecretBackend {
	if id := os.Getenv("OCI_VAULT_SECRET_ID"); id != "" {
		if b := newOCIBackend(id); b != nil {
			return b
		}
	}
	log.Println("backup: no provider configured, disabled")
	return nil
}
