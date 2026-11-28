// Package ftpdiag supports FTP / FTPS repositories, which restic reaches
// through rclone: rclone's password obscuring, TLS certificate inspection and
// pinning, and a step-by-step connection test that runs the real rclone
// binary.
//
// relyper: Relyper Backup addition, not part of upstream Backrest.
package ftpdiag

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
)

// cryptKey is rclone's fixed key for obscured config values
// (github.com/rclone/rclone/fs/config/obscure). Obscuring is not encryption:
// anyone with rclone can reveal the value. It only keeps the password from
// being readable at a glance, which is what rclone requires for *_PASS.
var cryptKey = []byte{
	0x9c, 0x93, 0x5b, 0x48, 0x73, 0x0a, 0x55, 0x4d,
	0x6b, 0xfd, 0x7c, 0x63, 0xc8, 0x86, 0xa9, 0x2b,
	0xd3, 0x90, 0x19, 0x8e, 0xb8, 0x12, 0x8a, 0xfb,
	0xf4, 0xde, 0x16, 0x2b, 0x8b, 0x95, 0xf6, 0x38,
}

// Obscure returns x in the form `rclone obscure` produces.
func Obscure(x string) (string, error) {
	plaintext := []byte(x)
	out := make([]byte, aes.BlockSize+len(plaintext))
	iv := out[:aes.BlockSize]
	if _, err := rand.Read(iv); err != nil {
		return "", err
	}
	if err := crypt(out[aes.BlockSize:], plaintext, iv); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(out), nil
}

// Reveal is the inverse of Obscure.
func Reveal(x string) (string, error) {
	in, err := base64.RawURLEncoding.DecodeString(x)
	if err != nil {
		return "", errors.New("obscured password is not valid base64")
	}
	if len(in) < aes.BlockSize {
		return "", errors.New("obscured password is too short")
	}
	buf := in[aes.BlockSize:]
	if err := crypt(buf, buf, in[:aes.BlockSize]); err != nil {
		return "", err
	}
	return string(buf), nil
}

func crypt(out, in, iv []byte) error {
	block, err := aes.NewCipher(cryptKey)
	if err != nil {
		return err
	}
	cipher.NewCTR(block, iv).XORKeyStream(out, in)
	return nil
}
