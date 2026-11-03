package install

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
)

// ordered is a JSON object that keeps its key order, so editing the user's
// settings.json does not reshuffle it.
type ordered struct {
	keys []string
	vals map[string]json.RawMessage
}

func parseOrdered(b []byte) (*ordered, error) {
	o := &ordered{vals: map[string]json.RawMessage{}}
	if len(bytes.TrimSpace(b)) == 0 {
		return o, nil
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	t, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := t.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("expected a JSON object")
	}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, err
		}
		k, _ := kt.(string)
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		if _, dup := o.vals[k]; !dup {
			o.keys = append(o.keys, k)
		}
		o.vals[k] = v
	}
	return o, nil
}

func (o *ordered) get(k string) json.RawMessage { return o.vals[k] }

func (o *ordered) set(k string, v json.RawMessage) {
	if _, ok := o.vals[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.vals[k] = v
}

func (o *ordered) del(k string) {
	if _, ok := o.vals[k]; !ok {
		return
	}
	delete(o.vals, k)
	for i, x := range o.keys {
		if x == k {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
}

func (o *ordered) marshalCompact() json.RawMessage {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		b.Write(kb)
		b.WriteByte(':')
		b.Write(o.vals[k])
	}
	b.WriteByte('}')
	return b.Bytes()
}

func (o *ordered) marshal() []byte {
	var out bytes.Buffer
	if err := json.Indent(&out, o.marshalCompact(), "", "  "); err != nil {
		return append(o.marshalCompact(), '\n')
	}
	out.WriteByte('\n')
	return out.Bytes()
}

func xmlEscape(w io.Writer, s string) error { return xml.EscapeText(w, []byte(s)) }
