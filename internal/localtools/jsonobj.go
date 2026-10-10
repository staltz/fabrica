package localtools

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// object is a JSON object that remembers the order of its keys.
//
// Tool config files are yours, not Fabrica's. When Fabrica changes its own
// entries, everything else in the file should come out exactly as it went
// in, in the same order, so a `git diff` of your dotfiles only shows
// Fabrica's lines. Go's maps don't keep order, hence this little type.
type object struct {
	keys []string
	vals map[string]json.RawMessage
}

func newObject() *object { return &object{vals: map[string]json.RawMessage{}} }

// parseObject parses a JSON object. Empty input is an empty object.
func parseObject(data []byte) (*object, error) {
	o := newObject()
	if len(bytes.TrimSpace(data)) == 0 {
		return o, nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, errors.New("expected a JSON object")
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := tok.(string)
		if !ok {
			return nil, errors.New("expected a key")
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		o.set(key, raw)
	}
	if _, err := dec.Token(); err != nil { // the closing brace
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("unexpected data after the JSON object")
	}
	return o, nil
}

func (o *object) get(key string) (json.RawMessage, bool) {
	v, ok := o.vals[key]
	return v, ok
}

func (o *object) set(key string, v json.RawMessage) {
	if _, ok := o.vals[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.vals[key] = v
}

func (o *object) setValue(key string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	o.set(key, data)
	return nil
}

func (o *object) delete(key string) {
	if _, ok := o.vals[key]; !ok {
		return
	}
	delete(o.vals, key)
	for i, k := range o.keys {
		if k == key {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
}

func (o *object) len() int { return len(o.keys) }

// child returns a nested object, or an empty one when the key is missing.
func (o *object) child(key string) (*object, error) {
	raw, ok := o.get(key)
	if !ok || string(bytes.TrimSpace(raw)) == "null" {
		return newObject(), nil
	}
	c, err := parseObject(raw)
	if err != nil {
		return nil, fmt.Errorf("%q: %w", key, err)
	}
	return c, nil
}

// MarshalJSON writes the object compactly, keys in order.
func (o *object) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		buf.Write(kb)
		buf.WriteByte(':')
		buf.Write(o.vals[k])
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// pretty renders the object with two-space indentation and a final
// newline, the way most tools write their config files.
func (o *object) pretty() ([]byte, error) {
	compact, err := o.MarshalJSON()
	if err != nil {
		return nil, err
	}
	var flat bytes.Buffer
	if err := json.Compact(&flat, compact); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, flat.Bytes(), "", "  "); err != nil {
		return nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}

// rawArray parses a JSON array into its raw elements. Missing or null is
// an empty array.
func rawArray(raw json.RawMessage) ([]json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return nil, nil
	}
	var out []json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}
