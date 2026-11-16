package evidence

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

const maxJSONDepth = 64

// parseJSON decodes one JSON document into maps, slices and scalars, keeping
// numbers as written. It refuses a repeated object key and anything after the
// document: a repeated key would let two readers see different documents under
// one signature.
func parseJSON(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	v, err := readValue(dec, 0)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("there is data after the JSON document")
	}
	return v, nil
}

func readValue(dec *json.Decoder, depth int) (any, error) {
	if depth > maxJSONDepth {
		return nil, errors.New("the JSON is nested too deeply")
	}
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return tok, nil
	}
	if delim == '[' {
		list := []any{}
		for dec.More() {
			v, err := readValue(dec, depth+1)
			if err != nil {
				return nil, err
			}
			list = append(list, v)
		}
		_, err := dec.Token()
		return list, err
	}
	obj := map[string]any{}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, _ := keyTok.(string)
		if _, dup := obj[key]; dup {
			return nil, errors.New("an object key is repeated")
		}
		if obj[key], err = readValue(dec, depth+1); err != nil {
			return nil, err
		}
	}
	_, err = dec.Token()
	return obj, err
}
