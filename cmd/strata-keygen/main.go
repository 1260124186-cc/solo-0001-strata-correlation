// Command strata-keygen generates the Ed25519 signing key pair used to issue
// verifiable comparison credentials. The private key is written to a local
// PEM file (mode 0600); it is never submitted through the service API.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("strata-keygen", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	privatePath := flags.String("out", "", "私钥 PEM 输出路径（必填）")
	publicPath := flags.String("public-out", "", "可选：同时写出公钥 PEM，供离线核验分发")
	force := flags.Bool("force", false, "允许覆盖已存在的输出文件")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if len(flags.Args()) > 0 {
		return fmt.Errorf("不支持位置参数")
	}
	if *privatePath == "" {
		return fmt.Errorf("必须通过 -out 指定私钥输出路径")
	}
	if _, err := os.Stat(*privatePath); err == nil && !*force {
		return fmt.Errorf("私钥文件已存在：%s（确认后使用 -force 覆盖）", *privatePath)
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		return err
	}
	privatePEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})
	if err := writeKeyFile(*privatePath, privatePEM, *force); err != nil {
		return err
	}
	publicDER, err := x509.MarshalPKIXPublicKey(public)
	if err != nil {
		return err
	}
	publicPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER})
	if *publicPath != "" {
		if err := writeKeyFile(*publicPath, publicPEM, *force); err != nil {
			return err
		}
	}
	fmt.Printf("STRATA_KEY_PRIVATE=%s\n", filepath.Clean(*privatePath))
	if *publicPath != "" {
		fmt.Printf("STRATA_KEY_PUBLIC=%s\n", filepath.Clean(*publicPath))
	}
	fmt.Printf("STRATA_PUBLIC_KEY_PEM_BEGIN\n%sSTRATA_PUBLIC_KEY_PEM_END\n", string(publicPEM))
	return nil
}

func writeKeyFile(path string, contents []byte, force bool) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(absolute, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err = f.Write(contents); err != nil {
		return err
	}
	return f.Sync()
}
