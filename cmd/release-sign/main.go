package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func main() {
	cmd := ""
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}

	switch cmd {
	case "keygen":
		runKeygen()
	case "manifest":
		runManifest()
	case "sign":
		runSign()
	case "verify":
		runVerify()
	case "pubkey":
		runPubkey()
	default:
		fmt.Fprintf(os.Stderr, `Usage: release-sign <command>

Commands:
  keygen    Generate a new Ed25519 release-signing key pair
  manifest  Generate SHA256SUMS.txt for release archives in a directory
  sign      Sign a manifest file with a private key
  verify    Verify a manifest signature with a public key
  pubkey    Derive and write the public key from a private key

Examples:
  release-sign keygen -out keys/release
  release-sign manifest -dir dist -out dist/SHA256SUMS.txt
  release-sign sign -in dist/SHA256SUMS.txt -privkey keys/release/private.key -out dist/SHA256SUMS.txt.sig
  release-sign verify -in dist/SHA256SUMS.txt -sig dist/SHA256SUMS.txt.sig -pubkey keys/release/public.key
`)
		os.Exit(1)
	}
}

func runKeygen() {
	fs := flag.NewFlagSet("keygen", flag.ExitOnError)
	outDir := fs.String("out", "keys/release", "Output directory for key files")
	fs.Parse(os.Args[2:])

	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		failf("failed to generate key pair: %v", err)
	}
	if err := os.MkdirAll(*outDir, 0700); err != nil {
		failf("failed to create output directory: %v", err)
	}

	privPath := filepath.Join(*outDir, "private.key")
	pubPath := filepath.Join(*outDir, "public.key")
	if err := os.WriteFile(privPath, []byte(hex.EncodeToString(priv)), 0600); err != nil {
		failf("failed to write private key: %v", err)
	}
	if err := os.WriteFile(pubPath, []byte(hex.EncodeToString(pub)), 0644); err != nil {
		failf("failed to write public key: %v", err)
	}

	fmt.Printf("Release signing key pair generated:\n")
	fmt.Printf("  Private key: %s (KEEP THIS SECRET)\n", privPath)
	fmt.Printf("  Public key:  %s\n", pubPath)
}

func runManifest() {
	fs := flag.NewFlagSet("manifest", flag.ExitOnError)
	dir := fs.String("dir", "dist", "Directory containing release archives")
	out := fs.String("out", "dist/SHA256SUMS.txt", "Output manifest path")
	fs.Parse(os.Args[2:])

	lines, err := buildManifest(*dir)
	if err != nil {
		failf("failed to build manifest: %v", err)
	}
	if len(lines) == 0 {
		failf("no release archives found in %s", *dir)
	}
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(*out, []byte(body), 0644); err != nil {
		failf("failed to write manifest: %v", err)
	}
	fmt.Printf("Manifest written: %s (%d files)\n", *out, len(lines))
}

func runSign() {
	fs := flag.NewFlagSet("sign", flag.ExitOnError)
	in := fs.String("in", "", "Input file to sign")
	privKeyPath := fs.String("privkey", "", "Path to private key")
	out := fs.String("out", "", "Output signature path")
	fs.Parse(os.Args[2:])

	if strings.TrimSpace(*in) == "" || strings.TrimSpace(*privKeyPath) == "" {
		failf("sign requires -in and -privkey")
	}
	outPath := *out
	if strings.TrimSpace(outPath) == "" {
		outPath = *in + ".sig"
	}

	privKey := mustReadPrivateKey(*privKeyPath)
	data, err := os.ReadFile(*in)
	if err != nil {
		failf("failed to read input file: %v", err)
	}
	sig := ed25519.Sign(privKey, data)
	if err := os.WriteFile(outPath, []byte(hex.EncodeToString(sig)), 0644); err != nil {
		failf("failed to write signature: %v", err)
	}
	fmt.Printf("Signature written: %s\n", outPath)
}

func runVerify() {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	in := fs.String("in", "", "Input file to verify")
	sigPath := fs.String("sig", "", "Path to detached signature")
	pubKeyPath := fs.String("pubkey", "", "Path to public key")
	fs.Parse(os.Args[2:])

	if strings.TrimSpace(*in) == "" || strings.TrimSpace(*sigPath) == "" || strings.TrimSpace(*pubKeyPath) == "" {
		failf("verify requires -in, -sig, and -pubkey")
	}

	data, err := os.ReadFile(*in)
	if err != nil {
		failf("failed to read input file: %v", err)
	}
	sigHex, err := os.ReadFile(*sigPath)
	if err != nil {
		failf("failed to read signature: %v", err)
	}
	sig, err := hex.DecodeString(strings.TrimSpace(string(sigHex)))
	if err != nil {
		failf("invalid signature format: %v", err)
	}
	pubKey := mustReadPublicKey(*pubKeyPath)
	if !ed25519.Verify(pubKey, data, sig) {
		failf("signature verification failed")
	}
	fmt.Println("Signature: VALID")
}

func runPubkey() {
	fs := flag.NewFlagSet("pubkey", flag.ExitOnError)
	privKeyPath := fs.String("privkey", "", "Path to private key")
	out := fs.String("out", "", "Output public key path")
	fs.Parse(os.Args[2:])

	if strings.TrimSpace(*privKeyPath) == "" || strings.TrimSpace(*out) == "" {
		failf("pubkey requires -privkey and -out")
	}
	privKey := mustReadPrivateKey(*privKeyPath)
	pubKey := privKey.Public().(ed25519.PublicKey)
	if err := os.WriteFile(*out, []byte(hex.EncodeToString(pubKey)), 0644); err != nil {
		failf("failed to write public key: %v", err)
	}
	fmt.Printf("Public key written: %s\n", *out)
}

func buildManifest(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var files []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if name == "SHA256SUMS.txt" || name == "SHA256SUMS.txt.sig" || name == "SHA256SUMS.public.key" {
			continue
		}
		if strings.HasSuffix(name, ".tar.gz") || strings.HasSuffix(name, ".zip") {
			files = append(files, name)
		}
	}
	sort.Strings(files)

	lines := make([]string, 0, len(files))
	for _, name := range files {
		hash, err := hashFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		lines = append(lines, fmt.Sprintf("%s  %s", hash, name))
	}
	return lines, nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func mustReadPrivateKey(path string) ed25519.PrivateKey {
	raw, err := os.ReadFile(path)
	if err != nil {
		failf("failed to read private key: %v", err)
	}
	decoded, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		failf("invalid private key format: %v", err)
	}
	if len(decoded) != ed25519.PrivateKeySize {
		failf("invalid private key size")
	}
	return ed25519.PrivateKey(decoded)
}

func mustReadPublicKey(path string) ed25519.PublicKey {
	raw, err := os.ReadFile(path)
	if err != nil {
		failf("failed to read public key: %v", err)
	}
	decoded, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		failf("invalid public key format: %v", err)
	}
	if len(decoded) != ed25519.PublicKeySize {
		failf("invalid public key size")
	}
	return ed25519.PublicKey(decoded)
}

func failf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
