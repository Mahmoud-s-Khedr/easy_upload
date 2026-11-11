package service

import (
	"crypto/sha256"
	"encoding"
	"encoding/hex"
	"fmt"
	"hash"
)

func newHashState() ([]byte, error) {
	marshaler, ok := sha256.New().(encoding.BinaryMarshaler)
	if !ok {
		return nil, fmt.Errorf("sha256 hasher does not support binary marshal")
	}
	return marshaler.MarshalBinary()
}

func hashFromState(state []byte) (hash.Hash, error) {
	hasher := sha256.New()
	if len(state) == 0 {
		return hasher, nil
	}
	unmarshaler, ok := hasher.(encoding.BinaryUnmarshaler)
	if !ok {
		return nil, fmt.Errorf("sha256 hasher does not support binary unmarshal")
	}
	if err := unmarshaler.UnmarshalBinary(state); err != nil {
		return nil, err
	}
	return hasher, nil
}

func marshalHashState(hasher hash.Hash) ([]byte, error) {
	marshaler, ok := hasher.(encoding.BinaryMarshaler)
	if !ok {
		return nil, fmt.Errorf("sha256 hasher does not support binary marshal")
	}
	return marshaler.MarshalBinary()
}

func digestFromState(state []byte) (string, error) {
	hasher, err := hashFromState(state)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}
