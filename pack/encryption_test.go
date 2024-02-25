package pack

import (
	"bytes"
	"encoding/hex"
	"testing"
)

func TestEncrypt(t *testing.T) {
	txt := []byte("HELLO WORLD THIS IS BEDROCKPACK 9418894178")
	key := []byte("0123Z5678K0123u567890123Z56789P1")

	encrypted, err := encryptCfb(txt, key)
	if err != nil {
		t.Fatal(err)
	}

	if hex.EncodeToString(encrypted) != "173918d75ea78e660b4f8927e11ad475941c55ccb0bb0fbd39e1e4f5d9233e86281677cc2e11d199ab19" {
		t.Fatal("mismatch")
	}
}

func TestDecrypt(t *testing.T) {
	encrypted, err := hex.DecodeString("173918d75ea78e660b4f8927e11ad475941c55ccb0bb0fbd39e1e4f5d9233e86281677cc2e11d199ab19")
	if err != nil {
		t.Fatal(err)
	}

	key := []byte("0123Z5678K0123u567890123Z56789P1")
	decrypted, err := decryptCfb(encrypted, key)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(decrypted, []byte("HELLO WORLD THIS IS BEDROCKPACK 9418894178")) {
		t.Fatal("mismatch")
	}
}

func TestEncryptDecryptCfb(t *testing.T) {
	txt := []byte("HELLO WORLD THIS IS BEDROCKPACK 9418894178")
	key := []byte("0123Z5678K0123u567890123Z56789P1")

	encrypted, err := encryptCfb(txt, key)
	if err != nil {
		t.Fatal(err)
	}

	decrypted, err := decryptCfb(encrypted, key)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(txt, decrypted) {
		t.Fatal("mismatch")
	}
}
