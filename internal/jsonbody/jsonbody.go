// Package jsonbody builds a JSON object from key=value arguments.
//
//	key=value     string
//	key:=<json>   raw JSON (number, bool, null, array, object)
//	a.b=value     nested object {"a":{"b":"value"}} unless Flat is set
//
// Insertion order is kept.
package jsonbody

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

type node struct {
	keys []string
	kids map[string]*node
	raw  json.RawMessage // leaf value, set when kids is nil
}

func newNode() *node { return &node{kids: map[string]*node{}} }

func (n *node) child(key string) *node {
	c, ok := n.kids[key]
	if !ok {
		c = newNode()
		n.kids[key] = c
		n.keys = append(n.keys, key)
	}
	return c
}

func (n *node) marshal(buf *bytes.Buffer) {
	if n.raw != nil {
		buf.Write(n.raw)
		return
	}
	buf.WriteByte('{')
	for i, k := range n.keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		buf.Write(kb)
		buf.WriteByte(':')
		n.kids[k].marshal(buf)
	}
	buf.WriteByte('}')
}

// Resolver rewrites a string value before it is encoded (value references).
type Resolver func(string) (string, error)

// Build returns the JSON object for args, indented unless compact.
func Build(args []string, flat, compact bool) (string, error) {
	return BuildWith(args, flat, compact, nil)
}

// BuildWith is Build with a Resolver applied to every key=value string value
// (key:=json values are raw JSON and are never resolved).
func BuildWith(args []string, flat, compact bool, resolve Resolver) (string, error) {
	root := newNode()
	for _, arg := range args {
		key, raw, err := parseArg(arg, resolve)
		if err != nil {
			return "", err
		}
		path := []string{key}
		if !flat {
			path = strings.Split(key, ".")
		}
		cur := root
		for _, p := range path {
			if cur.kids == nil { // a leaf set earlier becomes a parent
				cur.kids = map[string]*node{}
			}
			cur.raw = nil
			cur = cur.child(p)
		}
		cur.kids, cur.keys, cur.raw = nil, nil, raw
	}

	var buf bytes.Buffer
	root.marshal(&buf)
	if compact {
		return buf.String(), nil
	}
	var out bytes.Buffer
	if err := json.Indent(&out, buf.Bytes(), "", "  "); err != nil {
		return "", err
	}
	return out.String(), nil
}

func parseArg(arg string, resolve Resolver) (key string, raw json.RawMessage, err error) {
	if k, v, ok := strings.Cut(arg, ":="); ok && !strings.Contains(k, "=") {
		if !json.Valid([]byte(v)) {
			return "", nil, fmt.Errorf("%s: %q is not valid JSON", k, v)
		}
		return k, json.RawMessage(v), nil
	}
	k, v, ok := strings.Cut(arg, "=")
	if !ok || k == "" {
		return "", nil, fmt.Errorf("bad argument %q: want key=value or key:=json", arg)
	}
	if resolve != nil {
		if v, err = resolve(v); err != nil {
			return "", nil, fmt.Errorf("%s: %w", k, err)
		}
	}
	b, _ := json.Marshal(v)
	return k, b, nil
}
