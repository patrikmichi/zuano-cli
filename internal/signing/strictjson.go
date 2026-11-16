package signing

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// DecodeStrict reads one JSON document into maps, slices and scalars, with
// numbers kept as json.Number. A repeated object key at any depth is refused,
// because a parser that keeps the last one would verify bytes it did not show.
func DecodeStrict(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	v, err := strictValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("unexpected content after the JSON document")
	}
	return v, nil
}

func strictValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return tok, nil
	}
	switch delim {
	case '{':
		obj := map[string]any{}
		for dec.More() {
			keyTok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			key, _ := keyTok.(string)
			if _, dup := obj[key]; dup {
				return nil, fmt.Errorf("the key %q appears twice", key)
			}
			if obj[key], err = strictValue(dec); err != nil {
				return nil, err
			}
		}
		_, err := dec.Token()
		return obj, err
	case '[':
		list := []any{}
		for dec.More() {
			v, err := strictValue(dec)
			if err != nil {
				return nil, err
			}
			list = append(list, v)
		}
		_, err := dec.Token()
		return list, err
	}
	return nil, errors.New("unexpected JSON delimiter")
}
