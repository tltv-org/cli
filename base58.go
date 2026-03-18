package main

import (
	"fmt"
	"math/big"
)

// Base58 alphabet (Bitcoin variant) — no 0, O, I, l
const b58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

// b58Index maps byte -> index in alphabet, or -1 if invalid.
var b58Index [256]int

func init() {
	for i := range b58Index {
		b58Index[i] = -1
	}
	for i, c := range b58Alphabet {
		b58Index[c] = i
	}
}

// b58Encode encodes arbitrary bytes to a base58 string.
func b58Encode(data []byte) string {
	if len(data) == 0 {
		return ""
	}

	n := new(big.Int).SetBytes(data)
	mod := new(big.Int)
	zero := new(big.Int)
	base := big.NewInt(58)

	var result []byte
	for n.Cmp(zero) > 0 {
		n.DivMod(n, base, mod)
		result = append(result, b58Alphabet[mod.Int64()])
	}

	// Leading zero bytes become '1' in base58
	for _, b := range data {
		if b != 0 {
			break
		}
		result = append(result, b58Alphabet[0])
	}

	// Reverse
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}

	return string(result)
}

// b58Decode decodes a base58 string to bytes.
func b58Decode(s string) ([]byte, error) {
	if len(s) == 0 {
		return nil, fmt.Errorf("empty base58 string")
	}

	n := new(big.Int)
	base := big.NewInt(58)

	for i := 0; i < len(s); i++ {
		idx := b58Index[s[i]]
		if idx < 0 {
			return nil, fmt.Errorf("invalid base58 character: %c (position %d)", s[i], i)
		}
		n.Mul(n, base)
		n.Add(n, big.NewInt(int64(idx)))
	}

	// Count leading '1' characters (they represent 0x00 bytes)
	var leadingZeros int
	for i := 0; i < len(s); i++ {
		if s[i] != '1' {
			break
		}
		leadingZeros++
	}

	b := n.Bytes()
	result := make([]byte, leadingZeros+len(b))
	copy(result[leadingZeros:], b)

	return result, nil
}
