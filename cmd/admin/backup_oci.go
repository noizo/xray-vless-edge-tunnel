package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"

	"github.com/oracle/oci-go-sdk/v65/common/auth"
	"github.com/oracle/oci-go-sdk/v65/secrets"
	"github.com/oracle/oci-go-sdk/v65/vault"
)

type ociBackend struct {
	secretID string
	writer   *vault.VaultsClient
	reader   *secrets.SecretsClient
}

func newOCIBackend(secretID string) *ociBackend {
	log.Println("backup/oci: obtaining instance principal credentials...")
	provider, err := auth.InstancePrincipalConfigurationProvider()
	if err != nil {
		log.Printf("backup/oci: instance principal auth failed: %v", err)
		return nil
	}
	log.Println("backup/oci: instance principal OK")

	vc, err := vault.NewVaultsClientWithConfigurationProvider(provider)
	if err != nil {
		log.Printf("backup/oci: vaults client init failed: %v", err)
		return nil
	}

	sc, err := secrets.NewSecretsClientWithConfigurationProvider(provider)
	if err != nil {
		log.Printf("backup/oci: secrets client init failed: %v", err)
		return nil
	}

	log.Printf("backup/oci: enabled (secret: %s)", secretID)
	return &ociBackend{secretID: secretID, writer: &vc, reader: &sc}
}

func (b *ociBackend) Save(ctx context.Context, data []byte) error {
	encoded := base64.StdEncoding.EncodeToString(data)
	_, err := b.writer.UpdateSecret(ctx, vault.UpdateSecretRequest{
		SecretId: &b.secretID,
		UpdateSecretDetails: vault.UpdateSecretDetails{
			SecretContent: vault.Base64SecretContentDetails{
				Content: &encoded,
			},
		},
	})
	if err != nil {
		return fmt.Errorf("oci vault update: %w", err)
	}
	return nil
}

func (b *ociBackend) Load(ctx context.Context) ([]byte, error) {
	resp, err := b.reader.GetSecretBundle(ctx, secrets.GetSecretBundleRequest{
		SecretId: &b.secretID,
	})
	if err != nil {
		return nil, fmt.Errorf("oci vault fetch: %w", err)
	}

	content, ok := resp.SecretBundleContent.(secrets.Base64SecretBundleContentDetails)
	if !ok {
		return nil, fmt.Errorf("oci vault: unexpected content type")
	}
	if content.Content == nil {
		return nil, nil
	}

	raw, err := base64.StdEncoding.DecodeString(*content.Content)
	if err != nil {
		return nil, fmt.Errorf("oci vault base64 decode: %w", err)
	}
	return raw, nil
}
