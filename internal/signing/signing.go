// Package signing provides HMAC-SHA256 signing and verification of the
// client-carried context blob. The server is stateless; integrity comes
// from the signature, not from server-side storage.
package signing

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sort"
)

// Sign returns a URL-safe base64 HMAC-SHA256 of the canonical JSON encoding
// of visible, using secret as the key.
func Sign(secret []byte, visible any) (string, error) {
	canon, err := canonicalJSON(visible)
	if err != nil {
		return "", err
	}
	m := hmac.New(sha256.New, secret)
	m.Write(canon)
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil)), nil
}

// Verify recomputes the signature over visible and compares it to sig in
// constant time. Returns nil on match.
func Verify(secret []byte, visible any, sig string) error {
	if sig == "" {
		return errors.New("empty signature")
	}
	want, err := Sign(secret, visible)
	if err != nil {
		return err
	}
	if !hmac.Equal([]byte(want), []byte(sig)) {
		return errors.New("signature mismatch")
	}
	return nil
}

// canonicalJSON encodes v with sorted map keys and no extraneous whitespace
// so that semantically identical inputs always produce identical bytes.
// Without this, Go's default map ordering would make signatures non-deterministic.
func canonicalJSON(v any) ([]byte, error) {
	normalized, err := normalize(v)
	if err != nil {
		return nil, err
	}
	return json.Marshal(normalized)
}

func normalize(v any) (any, error) {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make(orderedMap, 0, len(keys))
		for _, k := range keys {
			nv, err := normalize(x[k])
			if err != nil {
				return nil, err
			}
			out = append(out, kv{Key: k, Val: nv})
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			n, err := normalize(item)
			if err != nil {
				return nil, err
			}
			out[i] = n
		}
		return out, nil
	default:
		// Round-trip through JSON so that decoded numbers etc. stay consistent.
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		var back any
		if err := json.Unmarshal(b, &back); err != nil {
			return nil, err
		}
		return back, nil
	}
}

type kv struct {
	Key string
	Val any
}

type orderedMap []kv

func (o orderedMap) MarshalJSON() ([]byte, error) {
	buf := []byte{'{'}
	for i, pair := range o {
		if i > 0 {
			buf = append(buf, ',')
		}
		k, err := json.Marshal(pair.Key)
		if err != nil {
			return nil, err
		}
		buf = append(buf, k...)
		buf = append(buf, ':')
		v, err := json.Marshal(pair.Val)
		if err != nil {
			return nil, err
		}
		buf = append(buf, v...)
	}
	buf = append(buf, '}')
	return buf, nil
}
