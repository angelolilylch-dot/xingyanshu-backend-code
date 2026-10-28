package avatar

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"testing"
)

func TestEncryptTencentFaceIdentityAES256CBC(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	idCard, name, encryption, err := encryptTencentFaceIdentity(&privateKey.PublicKey, "350102198509064117", "测试用户")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if idCard == "350102198509064117" || name == "测试用户" || encryption == nil || encryption.Algorithm == nil || *encryption.Algorithm != "AES-256-CBC" {
		t.Fatalf("unexpected encrypted request: %#v", encryption)
	}
	if encryption.EncryptList == nil || len(encryption.EncryptList) != 2 || *encryption.EncryptList[0] != "IdCard" || *encryption.EncryptList[1] != "Name" {
		t.Fatalf("unexpected encrypt list: %#v", encryption.EncryptList)
	}
	for _, value := range []string{idCard, name, *encryption.CiphertextBlob, *encryption.Iv} {
		if _, err := base64.StdEncoding.DecodeString(value); err != nil {
			t.Fatalf("cipher field must be base64: %v", err)
		}
	}
}
