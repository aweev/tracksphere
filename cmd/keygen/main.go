// Command keygen prints a fresh 32-byte secret key (base64) for
// TRACKSPHERE_SECRET_KEY. Usage: keygen
package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
)

func main() {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(base64.StdEncoding.EncodeToString(buf))
}