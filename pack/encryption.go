package pack

import (
	"crypto/aes"
	"math/rand"
)

func decryptCfb(data []byte, key []byte) ([]byte, error) {
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	shiftRegister := append(key[:16], data...) // prefill with iv + cipherdata
	_tmp := make([]byte, 16)
	off := 0
	for off < len(data) {
		b.Encrypt(_tmp, shiftRegister)
		data[off] ^= _tmp[0]
		shiftRegister = shiftRegister[1:]
		off++
	}
	return data, nil
}

func encryptCfb(data []byte, key []byte) ([]byte, error) {
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	shiftRegister := make([]byte, 16)
	copy(shiftRegister, key[:16]) // prefill with iv
	_tmp := make([]byte, 16)
	off := 0
	for off < len(data) {
		b.Encrypt(_tmp, shiftRegister)
		data[off] ^= _tmp[0]
		shiftRegister = append(shiftRegister[1:], data[off])
		off++
	}
	return data, nil
}

func GenerateKey() []byte {
	const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	const length = 32
	result := make([]byte, length)
	for i := 0; i < length; i++ {
		result[i] = chars[rand.Intn(len(chars))]
	}

	return result
}

func GenerateKeyFromSeed(seed []byte) []byte {
	const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	const length = 32
	result := make([]byte, length)
	for i := 0; i < length; i++ {
		result[i] = chars[int(seed[i%len(seed)])%len(chars)]
	}

	return result
}
