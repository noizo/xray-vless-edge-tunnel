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
	vlessHost  = env("HOST", "hide.nikolaev.id")
	vlessPort  = env("PORT", "443")
	basePath   string

	backend SecretBackend
)

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// normalizeBasePath returns a non-empty path starting with "/" and no trailing slash.
func normalizeBasePath(p string) string {
	p = strings.TrimSpace(p)
	p = strings.TrimSuffix(p, "/")
	if p == "" || p == "/" {
		return "/admin"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return p
}

func main() {
	basePath = normalizeBasePath(env("BASE_PATH", "/admin"))
	log.Printf("starting xray-admin")
	log.Printf("config: namespace=%s secret=%s host=%s port=%s basePath=%s",
		namespace, secretName, vlessHost, vlessPort, basePath)

	config, err := rest.InClusterConfig()
	if err != nil {
		log.Fatalf("in-cluster config: %v", err)
	}
	clientset, err = kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatalf("kubernetes client: %v", err)
	}
	log.Println("kubernetes client ready")

	backend = initBackend()

	ctx := context.Background()
	users, err := getUsers(ctx)
	if err != nil {
		log.Printf("initial user load failed: %v", err)
	} else {
		log.Printf("loaded %d users from k8s secret", len(users))
	}
	if (err != nil || len(users) == 0) && backend != nil {
		if restored := restoreFromBackend(ctx); restored {
			log.Println("restored users from backup")
		}
	}

	mux := http.NewServeMux()
	p := basePath
	mux.HandleFunc("GET "+p+"/", serveIndex)
	mux.HandleFunc("GET "+p+"/api/users", listUsers)
	mux.HandleFunc("POST "+p+"/api/users", addUser)
	mux.HandleFunc("DELETE "+p+"/api/users/{name}", deleteUser)
	mux.HandleFunc("GET "+p+"/api/share/{name}", shareUser)

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

func restoreFromBackend(ctx context.Context) bool {
	raw, err := backend.Load(ctx)
	if err != nil {
		log.Printf("backup restore: load failed: %v", err)
		return false
	}
	if raw == nil {
		return false
	}

	var users []User
	if err := json.Unmarshal(raw, &users); err != nil {
		log.Printf("backup restore: json parse failed: %v", err)
		return false
	}
	if len(users) == 0 {
		return false
	}

	if err := saveUsers(ctx, users); err != nil {
		log.Printf("backup restore: save to k8s failed: %v", err)
		return false
	}

	log.Printf("backup restore: restored %d users", len(users))
	return true
}

func syncToBackend(users []User) {
	if backend == nil {
		return
	}
	data, err := json.Marshal(users)
	if err != nil {
		log.Printf("backup sync: marshal failed: %v", err)
		return
	}
	if err := backend.Save(context.Background(), data); err != nil {
		log.Printf("backup sync: %v", err)
		return
	}
	log.Printf("backup sync: backed up %d users", len(users))
}

func serveIndex(w http.ResponseWriter, r *http.Request) {
	data, _ := indexHTML.ReadFile("index.html")
	html := strings.Replace(string(data), "__BASE_HREF__", basePath+"/", 1)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(html)) // nosemgrep: go.lang.security.audit.xss.no-direct-write-to-responsewriter.no-direct-write-to-responsewriter -- embedded static HTML, no user input
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
	go syncToBackend(users)

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
	go syncToBackend(filtered)

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
