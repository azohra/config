package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// decodeExactJSON is the one strict decode Config performs on a tracked
// artifact: no unknown fields, and nothing after the document. Anything less
// accepts a file Config did not write as one it did.
func decodeExactJSON(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("JSON contains trailing data")
	}
	return nil
}

// contentDigest identifies the exact bytes of a file Config owns. A hook body,
// a baseline, and a restore record are all the same fact, so it is spelled
// once, and validContentDigest is the only reader of that spelling.
func contentDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return digestPrefix + hex.EncodeToString(sum[:])
}

const digestPrefix = "sha256:"

func validContentDigest(value string) bool {
	digest, found := strings.CutPrefix(value, digestPrefix)
	if !found || len(digest) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(digest)
	return err == nil
}
