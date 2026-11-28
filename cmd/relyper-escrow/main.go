// Command relyper-escrow is Relyper-owned: the operator's offline tool for
// key escrow (docs/CONCEPT-MULTITENANT.md, CUSTOMERS.md).
//
//	relyper-escrow keygen <private-key-file>   create a key pair; prints the public key
//	relyper-escrow open <private-key-file> <file.sealed>   print a sealed customer key
//
// Run it on an offline machine. The private key never belongs on the
// Relyper Backup server; the server only gets the public key
// (RELYPER_ESCROW_PUBLIC_KEY).
package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/garethgeorge/backrest/internal/relyper/portal"
	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/nacl/box"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "relyper-escrow:", err)
		os.Exit(1)
	}
}

func usage() error {
	return errors.New("usage: relyper-escrow keygen <private-key-file> | open <private-key-file> <file.sealed>")
}

func run(args []string) error {
	if len(args) < 2 {
		return usage()
	}
	switch args[0] {
	case "keygen":
		if len(args) != 2 {
			return usage()
		}
		if _, err := os.Stat(args[1]); err == nil {
			return fmt.Errorf("%s exists; refusing to overwrite a private key", args[1])
		}
		pub, priv, err := box.GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
		if err := os.WriteFile(args[1], []byte(base64.StdEncoding.EncodeToString(priv[:])+"\n"), 0o600); err != nil {
			return err
		}
		fmt.Println("Private key written to", args[1], "- keep it offline, never on the server.")
		fmt.Println("Set on the Relyper Backup server:")
		fmt.Println("RELYPER_ESCROW_PUBLIC_KEY=" + base64.StdEncoding.EncodeToString(pub[:]))
		return nil
	case "open":
		if len(args) != 3 {
			return usage()
		}
		priv, pub, err := readKey(args[1])
		if err != nil {
			return err
		}
		data, err := os.ReadFile(args[2])
		if err != nil {
			return err
		}
		rec, err := portal.Open(pub, priv, data)
		if err != nil {
			return err
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rec)
	}
	return usage()
}

func readKey(path string) (priv, pub *[32]byte, err error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(raw) != 32 {
		return nil, nil, errors.New("invalid private key file")
	}
	priv, pub = new([32]byte), new([32]byte)
	copy(priv[:], raw)
	p, err := curve25519.X25519(priv[:], curve25519.Basepoint)
	if err != nil {
		return nil, nil, err
	}
	copy(pub[:], p)
	return priv, pub, nil
}
