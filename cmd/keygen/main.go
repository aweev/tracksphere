// Command keygen generates and manages encryption keys for TrackSphere.
// Usage:
//
//   keygen                    # Generate a new 32-byte secret key (base64)
//   keygen rotate             # Print rotation instructions + new key
//   keygen list               # List keys from config (if multi-key configured)
package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/tracksphere/tracksphere/internal/config"
)

func main() {
	if len(os.Args) < 2 {
		generateKey()
		return
	}

	switch os.Args[1] {
	case "generate", "gen":
		generateKey()
	case "rotate":
		rotateKey()
	case "list":
		listKeys()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", os.Args[1])
		fmt.Fprintln(os.Stderr, "Usage: keygen [generate|rotate|list]")
		os.Exit(1)
	}
}

func generateKey() {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(base64.StdEncoding.EncodeToString(buf))
}

func rotateKey() {
	fmt.Println("# TrackSphere Key Rotation")
	fmt.Println("# Generated at:", time.Now().UTC().Format(time.RFC3339))
	fmt.Println()

	// Generate new key
	newKey := make([]byte, 32)
	if _, err := rand.Read(newKey); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	newKeyB64 := base64.StdEncoding.EncodeToString(newKey)

	fmt.Println("## New Key (add to TRACKSPHERE_SECRET_KEYS)")
	fmt.Println(newKeyB64)
	fmt.Println()

	fmt.Println("## Rotation Procedure")
	fmt.Println("1. Add the new key to TRACKSPHERE_SECRET_KEYS (comma-separated):")
	fmt.Printf("   TRACKSPHERE_SECRET_KEYS=\"%s,%s\"\n", getCurrentKey(), newKeyB64)
	fmt.Println("2. Deploy with both keys active (old + new)")
	fmt.Println("3. Wait for all sessions to rotate (max session TTL, default 168h)")
	fmt.Println("4. Remove the old key from TRACKSPHERE_SECRET_KEYS")
	fmt.Println("5. Redeploy with only the new key")
	fmt.Println()

	fmt.Println("## Multi-Key Config Format")
	fmt.Println("# TRACKSPHERE_SECRET_KEYS=\"key1,key2,key3\"")
	fmt.Println("# - First key is used for NEW encryption (signing, sealing)")
	fmt.Println("# - All keys are tried for DECRYPTION (verification, unsealing)")
	fmt.Println("# - This enables zero-downtime rotation")
	fmt.Println()

	// Print example docker-compose snippet
	fmt.Println("## Example docker-compose.yml update")
	fmt.Println("```yaml")
	fmt.Println("services:")
	fmt.Println("  api:")
	fmt.Printf("    environment:\n      TRACKSPHERE_SECRET_KEYS: \"%s,%s\"\n", getCurrentKey(), newKeyB64)
	fmt.Println("```")
}

func listKeys() {
	// Try to load from environment
	keysEnv := os.Getenv("TRACKSPHERE_SECRET_KEYS")
	if keysEnv == "" {
		keysEnv = os.Getenv("TRACKSPHERE_SECRET_KEY")
	}
	if keysEnv == "" {
		fmt.Println("No keys found in environment")
		return
	}

	keys := strings.Split(keysEnv, ",")
	fmt.Println("## Active Keys")
	for i, k := range keys {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		// Decode to show metadata
		decoded, err := base64.StdEncoding.DecodeString(k)
		if err != nil {
			fmt.Printf("%d. %s... (invalid base64)\n", i+1, k[:min(8, len(k))])
			continue
		}
		fmt.Printf("%d. %s... (%d bytes)\n", i+1, k[:min(8, len(k))], len(decoded))
		if i == 0 {
			fmt.Println("   ^ PRIMARY (used for new encryption)")
		}
	}

	// Validate config
	cfg, err := config.Load()
	if err != nil {
		fmt.Printf("\nConfig validation failed: %v\n", err)
		return
	}
	fmt.Printf("\nConfig validation: OK (env=%s)\n", cfg.Env)
	if cfg.Env == "production" {
		if len(cfg.SecretKey) < 32 {
			fmt.Println("WARNING: Primary key < 32 bytes in production")
		}
		for i, k := range cfg.CarrierSecrets {
			if len(k) < 16 {
				fmt.Printf("WARNING: Carrier '%s' webhook secret < 16 bytes in production\n", i)
			}
		}
	}
}

func getCurrentKey() string {
	key := os.Getenv("TRACKSPHERE_SECRET_KEY")
	if key == "" {
		return "<current-key>"
	}
	if len(key) > 8 {
		return key[:8] + "..."
	}
	return key
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// KeyConfig represents the multi-key configuration structure
type KeyConfig struct {
	Keys      []string `json:"keys"`
	Primary   int      `json:"primary"`
	Generated string   `json:"generated"`
	Note      string   `json:"note,omitempty"`
}

func printKeyConfigJSON(keys []string) {
	cfg := KeyConfig{
		Keys:      keys,
		Primary:   0,
		Generated: time.Now().UTC().Format(time.RFC3339),
		Note:      "Rotate by shifting primary index and adding new keys",
	}
	data, _ := json.MarshalIndent(cfg, "", "  ")
	fmt.Println(string(data))
}