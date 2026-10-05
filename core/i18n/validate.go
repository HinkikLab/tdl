package i18n

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
	"text/template"
	"text/template/parse"
)

type resourceMessage struct {
	Zero  string `json:"zero"`
	One   string `json:"one"`
	Two   string `json:"two"`
	Few   string `json:"few"`
	Many  string `json:"many"`
	Other string `json:"other"`
}

func (m resourceMessage) forms() []string {
	return []string{m.Zero, m.One, m.Two, m.Few, m.Many, m.Other}
}

// ValidateResources checks message identity, template syntax, and the union of
// named parameters across all plural forms in the two shipped languages.
func ValidateResources(resourceSets ...fs.FS) error {
	translations := map[string]map[string]resourceMessage{"en": {}, "zh": {}}
	for _, resources := range resourceSets {
		err := fs.WalkDir(resources, ".", func(file string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || !strings.EqualFold(path.Ext(file), ".json") {
				return nil
			}
			parts := strings.Split(strings.TrimSuffix(path.Base(file), path.Ext(file)), ".")
			if len(parts) < 2 {
				return fmt.Errorf("translation filename %q must end with .en.json or .zh.json", file)
			}
			lang := strings.ToLower(parts[len(parts)-1])
			if lang != "en" && lang != "zh" {
				return nil
			}
			data, err := fs.ReadFile(resources, file)
			if err != nil {
				return err
			}
			dec := json.NewDecoder(bytes.NewReader(data))
			start, err := dec.Token()
			if err != nil || start != json.Delim('{') {
				return fmt.Errorf("translation resource %q must be a JSON object", file)
			}
			for dec.More() {
				key, err := dec.Token()
				if err != nil {
					return err
				}
				id := key.(string)
				if id == "" {
					return fmt.Errorf("empty translation ID in %q", file)
				}
				if _, ok := translations[lang][id]; ok {
					return fmt.Errorf("duplicate %s translation ID %q", lang, id)
				}
				var message resourceMessage
				if err := dec.Decode(&message); err != nil {
					return fmt.Errorf("parse translation resource %q: %w", file, err)
				}
				if message.Other == "" {
					return fmt.Errorf("translation %q has an empty other form", id)
				}
				for _, form := range message.forms() {
					if _, err := TemplateParameters(form); err != nil {
						return fmt.Errorf("translation %q has an invalid template: %w", id, err)
					}
				}
				translations[lang][id] = message
			}
			if _, err := dec.Token(); err != nil {
				return err
			}
			if _, err := dec.Token(); err != io.EOF {
				return fmt.Errorf("translation resource %q contains trailing data", file)
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	for id := range translations["en"] {
		if _, ok := translations["zh"][id]; !ok {
			return fmt.Errorf("missing Simplified Chinese translation for %q", id)
		}
	}
	for id := range translations["zh"] {
		if _, ok := translations["en"][id]; !ok {
			return fmt.Errorf("missing English translation for %q", id)
		}
	}
	for id, en := range translations["en"] {
		zh := translations["zh"][id]
		left, right := formParameters(en), formParameters(zh)
		if strings.Join(left, "\x00") != strings.Join(right, "\x00") {
			return fmt.Errorf("translation %q has inconsistent named parameters: en=%v zh=%v", id, left, right)
		}
	}
	return nil
}

func formParameters(m resourceMessage) []string {
	set := map[string]bool{}
	for _, form := range m.forms() {
		p, _ := TemplateParameters(form)
		for _, name := range p {
			set[name] = true
		}
	}
	result := make([]string, 0, len(set))
	for name := range set {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

// TemplateParameters parses templates rather than guessing parameters with a
// regular expression. Repeated fields, conditionals, and pipelines are covered.
func TemplateParameters(message string) ([]string, error) {
	tmpl, err := template.New("message").Parse(message)
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	var walk func(parse.Node)
	walk = func(n parse.Node) {
		if n == nil {
			return
		}
		switch n := n.(type) {
		case *parse.ListNode:
			if n != nil {
				for _, v := range n.Nodes {
					walk(v)
				}
			}
		case *parse.ActionNode:
			walk(n.Pipe)
		case *parse.PipeNode:
			for _, c := range n.Cmds {
				walk(c)
			}
		case *parse.CommandNode:
			for _, a := range n.Args {
				walk(a)
			}
		case *parse.FieldNode:
			if len(n.Ident) > 0 {
				set[n.Ident[0]] = true
			}
		case *parse.IfNode:
			walk(n.Pipe)
			walk(n.List)
			walk(n.ElseList)
		case *parse.RangeNode:
			walk(n.Pipe)
			walk(n.List)
			walk(n.ElseList)
		case *parse.WithNode:
			walk(n.Pipe)
			walk(n.List)
			walk(n.ElseList)
		case *parse.ChainNode:
			walk(n.Node)
		case *parse.TemplateNode:
			walk(n.Pipe)
		}
	}
	for _, tree := range tmpl.Templates() {
		walk(tree.Root)
	}
	result := make([]string, 0, len(set))
	for name := range set {
		result = append(result, name)
	}
	sort.Strings(result)
	return result, nil
}
