package main

import (
	"context"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/oracle/oci-go-sdk/v65/common/auth"
	"github.com/oracle/oci-go-sdk/v65/secrets"
	"github.com/oracle/oci-go-sdk/v65/vault"
	"github.com/skip2/go-qrcode"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

//go:embed index.html
var indexHTML embed.FS

type User struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

var (
	clientset  *kubernetes.Clientset
	namespace  = env("NAMESPACE", "xray")
	secretName = env("SECRET_NAME", "xray-users")
	deployName = env("DEPLOY_NAME", "xray")
	vlessHost  = env("HOST", "hide.nikolaev.id")
	vlessPort  = env("PORT", "443")

	vaultSecretID = os.Getenv("OCI_VAULT_SECRET_ID")
	vaultClient   *vault.VaultsClient
	secretsClient *secrets.SecretsClient
)

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	log.Printf("starting xray-admin")
	log.Printf("config: namespace=%s secret=%s deploy=%s host=%s port=%s",
		namespace, secretName, deployName, vlessHost, vlessPort)
	log.Printf("config: OCI_VAULT_SECRET_ID=%q", vaultSecretID)

	config, err := rest.InClusterConfig()
	if err != nil {
		log.Fatalf("in-cluster config: %v", err)
	}
	clientset, err = kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatalf("kubernetes client: %v", err)
	}
	log.Println("kubernetes client ready")

	initVaultClients()

	ctx := context.Background()
	users, err := getUsers(ctx)
	if err != nil {
		log.Printf("initial user load failed: %v", err)
	} else {
		log.Printf("loaded %d users from k8s secret", len(users))
	}
	if err != nil || len(users) == 0 {
		if restoreFromVault(ctx) {
			log.Println("restored users from OCI Vault")
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", serveIndex)
	mux.HandleFunc("GET /api/users", listUsers)
	mux.HandleFunc("POST /api/users", addUser)
	mux.HandleFunc("DELETE /api/users/{name}", deleteUser)
	mux.HandleFunc("GET /api/share/{name}", shareUser)

	addr := ":8080"
	log.Printf("listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, logRequests(mux))) // nosemgrep: go.lang.security.audit.net.use-tls.use-tls -- TLS terminates at Cloudflare Tunnel
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
	})
}

func initVaultClients() {
	if vaultSecretID == "" {
		log.Println("vault: OCI_VAULT_SECRET_ID is empty, backup disabled")
		return
	}

	log.Println("vault: obtaining instance principal credentials...")
	provider, err := auth.InstancePrincipalConfigurationProvider()
	if err != nil {
		log.Printf("vault: instance principal auth failed: %v", err)
		return
	}
	log.Println("vault: instance principal OK")

	vc, err := vault.NewVaultsClientWithConfigurationProvider(provider)
	if err != nil {
		log.Printf("vault: vaults client init failed: %v", err)
		return
	}
	vaultClient = &vc
	log.Println("vault: vaults client ready")

	sc, err := secrets.NewSecretsClientWithConfigurationProvider(provider)
	if err != nil {
		log.Printf("vault: secrets client init failed: %v", err)
		return
	}
	secretsClient = &sc
	log.Println("vault: secrets client ready")

	log.Printf("vault: backup enabled (secret: %s)", vaultSecretID)
}

func syncToVault(ctx context.Context, users []User) {
	if vaultClient == nil {
		return
	}

	data, err := json.Marshal(users)
	if err != nil {
		log.Printf("vault sync: marshal failed: %v", err)
		return
	}

	encoded := base64.StdEncoding.EncodeToString(data)
	_, err = vaultClient.UpdateSecret(ctx, vault.UpdateSecretRequest{
		SecretId: &vaultSecretID,
		UpdateSecretDetails: vault.UpdateSecretDetails{
			SecretContent: vault.Base64SecretContentDetails{
				Content: &encoded,
			},
		},
	})
	if err != nil {
		log.Printf("vault sync failed: %v", err)
		return
	}
	log.Printf("vault sync: backed up %d users", len(users))
}

func restoreFromVault(ctx context.Context) bool {
	if secretsClient == nil {
		return false
	}

	resp, err := secretsClient.GetSecretBundle(ctx, secrets.GetSecretBundleRequest{
		SecretId: &vaultSecretID,
	})
	if err != nil {
		log.Printf("vault restore: fetch failed: %v", err)
		return false
	}

	content, ok := resp.SecretBundleContent.(secrets.Base64SecretBundleContentDetails)
	if !ok {
		log.Printf("vault restore: unexpected content type")
		return false
	}
	if content.Content == nil {
		return false
	}

	raw, err := base64.StdEncoding.DecodeString(*content.Content)
	if err != nil {
		log.Printf("vault restore: base64 decode failed: %v", err)
		return false
	}

	var users []User
	if err := json.Unmarshal(raw, &users); err != nil {
		log.Printf("vault restore: json parse failed: %v", err)
		return false
	}
	if len(users) == 0 {
		return false
	}

	if err := saveUsers(ctx, users); err != nil {
		log.Printf("vault restore: save to k8s failed: %v", err)
		return false
	}

	log.Printf("vault restore: restored %d users", len(users))
	return true
}

func serveIndex(w http.ResponseWriter, r *http.Request) {
	data, _ := indexHTML.ReadFile("index.html")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data) // nosemgrep: go.lang.security.audit.xss.no-direct-write-to-responsewriter.no-direct-write-to-responsewriter -- embedded static HTML, no user input
}

func getUsers(ctx context.Context) ([]User, error) {
	secret, err := clientset.CoreV1().Secrets(namespace).Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	raw, ok := secret.Data["users.json"]
	if !ok {
		return []User{}, nil
	}
	var users []User
	if err := json.Unmarshal(raw, &users); err != nil {
		return nil, fmt.Errorf("parse users.json: %w", err)
	}
	return users, nil
}

func saveUsers(ctx context.Context, users []User) error {
	data, err := json.MarshalIndent(users, "", "  ")
	if err != nil {
		return err
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	patch := fmt.Sprintf(`{"data":{"users.json":"%s"}}`, encoded)
	_, err = clientset.CoreV1().Secrets(namespace).Patch(
		ctx, secretName, types.MergePatchType, []byte(patch), metav1.PatchOptions{},
	)
	return err
}

func restartDeployment(ctx context.Context) error {
	patch := fmt.Sprintf(
		`{"spec":{"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":"%s"}}}}}`,
		time.Now().Format(time.RFC3339),
	)
	_, err := clientset.AppsV1().Deployments(namespace).Patch(
		ctx, deployName, types.StrategicMergePatchType, []byte(patch), metav1.PatchOptions{},
	)
	return err
}

func listUsers(w http.ResponseWriter, r *http.Request) {
	users, err := getUsers(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(users)
}

func addUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	users, err := getUsers(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	for _, u := range users {
		if u.Name == req.Name {
			http.Error(w, "name already exists", http.StatusConflict)
			return
		}
	}

	newUser := User{ID: uuid.NewString(), Name: req.Name}
	users = append(users, newUser)
	log.Printf("addUser: %q (total: %d)", req.Name, len(users))

	if err := saveUsers(ctx, users); err != nil {
		log.Printf("addUser: save failed: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	go syncToVault(context.Background(), users)
	if err := restartDeployment(ctx); err != nil {
		log.Printf("addUser: restart failed: %v", err)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(newUser)
}

func deleteUser(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	ctx := r.Context()
	users, err := getUsers(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	found := false
	filtered := make([]User, 0, len(users))
	for _, u := range users {
		if u.Name == name {
			found = true
			continue
		}
		filtered = append(filtered, u)
	}
	if !found {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}

	log.Printf("deleteUser: %q (remaining: %d)", name, len(filtered))
	if err := saveUsers(ctx, filtered); err != nil {
		log.Printf("deleteUser: save failed: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	go syncToVault(context.Background(), filtered)
	if err := restartDeployment(ctx); err != nil {
		log.Printf("deleteUser: restart failed: %v", err)
	}

	w.WriteHeader(http.StatusNoContent)
}

func shareUser(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	users, err := getUsers(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var target *User
	for _, u := range users {
		if u.Name == name {
			target = &u
			break
		}
	}
	if target == nil {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}

	uri := fmt.Sprintf(
		"vless://%s@%s:%s?encryption=none&security=tls&sni=%s&type=ws&host=%s&path=%%2Fws#OCI-%s",
		target.ID, vlessHost, vlessPort, vlessHost, vlessHost, target.Name,
	)

	png, err := qrcode.Encode(uri, qrcode.Medium, 512)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"uri": uri,
		"qr":  base64.StdEncoding.EncodeToString(png),
	})
}
