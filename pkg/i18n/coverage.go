package i18n

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	corei18n "github.com/iyear/tdl/core/i18n"
)

const commandDirectory = "cmd"

type coverageResourceMessage struct {
	Zero  string `json:"zero"`
	One   string `json:"one"`
	Two   string `json:"two"`
	Few   string `json:"few"`
	Many  string `json:"many"`
	Other string `json:"other"`
}

var printfPlaceholder = regexp.MustCompile(`%(?:\[[0-9]+\])?[-+#0 ]*(?:[0-9]+|\*)?(?:\.(?:[0-9]+|\*))?(?:\[[0-9]+\])?[A-Za-z%]`)

// UnlocalizedOutput identifies literal English text sent directly to a
// terminal-writing call. Dynamic progress payloads and output wrapped in
// console.Translate are left alone.
type UnlocalizedOutput struct {
	File string
	Line int
	Text string
}

// FindUnlocalizedOutput is a migration guard for common terminal output APIs.
// Stable machine output should be generated as data and is not matched unless
// a literal is passed directly to a known terminal writer.
func FindUnlocalizedOutput(root string) ([]UnlocalizedOutput, error) {
	var findings []UnlocalizedOutput
	fset := token.NewFileSet()
	root = filepath.Clean(root)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".git" || entry.Name() == "vendor" || entry.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		switch strings.Split(filepath.ToSlash(relative), "/")[0] {
		case "app", commandDirectory, "pkg", "extension", "core", "internal":
		default:
			return nil
		}
		return parseOutputFile(path, fset, &findings)
	})
	return findings, err
}

func localizedExpression(node ast.Node) bool {
	localized := false
	ast.Inspect(node, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if ok && selector.Sel.Name == "Translate" {
			localized = true
			return false
		}
		return !localized
	})
	return localized
}

func outputFunction(call *ast.CallExpr, file string) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	switch selector.Sel.Name {
	case "Print", "Println", "Printf", "Fprint", "Fprintln", "Fprintf":
	case "Green", "Cyan", "Red", "Blue", "Yellow", "White", "Magenta", "Black",
		"GreenString", "CyanString", "RedString", "BlueString", "YellowString", "WhiteString", "MagentaString", "BlackString":
	default:
		return false
	}
	if pkg, ok := selector.X.(*ast.Ident); ok && (pkg.Name == "fmt" || pkg.Name == "color") {
		return true
	}
	receiver, ok := selector.X.(*ast.Ident)
	return ok && strings.Contains(filepath.ToSlash(file), "/cmd/") && receiver.Name == commandDirectory
}

func outputText(call *ast.CallExpr) (string, bool) {
	if len(call.Args) == 0 {
		return "", false
	}
	start := 0
	selector := call.Fun.(*ast.SelectorExpr)
	if strings.HasPrefix(selector.Sel.Name, "Fprint") {
		start = 1
	}
	for _, arg := range call.Args[start:] {
		if localizedExpression(arg) {
			continue
		}
		literal, ok := arg.(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			continue
		}
		text, err := strconv.Unquote(literal.Value)
		if err != nil {
			continue
		}
		text = printfPlaceholder.ReplaceAllString(text, "")
		if strings.TrimSpace(text) == "" || !strings.ContainsFunc(text, unicode.IsLetter) {
			continue
		}
		return text, true
	}
	return "", false
}

// parseOutputFile is kept separate to make source scanning deterministic and
// easy to exercise with focused tests.
func parseOutputFile(path string, fset *token.FileSet, findings *[]UnlocalizedOutput) error {
	file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return err
	}
	if ast.IsGenerated(file) {
		return nil
	}
	ast.Inspect(file, func(node ast.Node) bool {
		if function, ok := node.(*ast.FuncDecl); ok && function.Doc != nil && strings.Contains(function.Doc.Text(), "i18n:ignore") {
			return false
		}
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if selector, ok := call.Fun.(*ast.SelectorExpr); ok {
			if receiver, ok := selector.X.(*ast.Ident); ok && receiver.Name == "diagnostic" && selector.Sel.Name == "Describe" {
				return false
			}
			if receiver, ok := selector.X.(*ast.Ident); ok && (receiver.Name == "fmt" && selector.Sel.Name == "Errorf" || receiver.Name == "errors" && (selector.Sel.Name == "New" || selector.Sel.Name == "Errorf" || selector.Sel.Name == "Wrap" || selector.Sel.Name == "Wrapf")) {
				canonical := filepath.ToSlash(path)
				// Translation infrastructure must be able to explain a broken
				// catalog before any translator can be constructed.
				if strings.Contains(canonical, "/i18n/") || strings.Contains(canonical, "/diagnostic/") {
					return true
				}
				index := 0
				if strings.HasPrefix(selector.Sel.Name, "Wrap") {
					index = 1
				}
				if len(call.Args) > index {
					if literal, ok := call.Args[index].(*ast.BasicLit); ok && literal.Kind == token.STRING {
						position := fset.Position(call.Pos())
						ignored := false
						for _, comments := range file.Comments {
							if fset.Position(comments.End()).Line >= position.Line-1 && fset.Position(comments.Pos()).Line <= position.Line && strings.Contains(comments.Text(), "i18n:ignore") {
								ignored = true
							}
						}
						if !ignored {
							text, _ := strconv.Unquote(literal.Value)
							*findings = append(*findings, UnlocalizedOutput{File: canonical, Line: position.Line, Text: text})
						}
					}
				}
			}
		}
		if !outputFunction(call, path) {
			return true
		}
		position := fset.Position(call.Pos())
		for _, comments := range file.Comments {
			if fset.Position(comments.End()).Line >= position.Line-1 && fset.Position(comments.Pos()).Line <= position.Line && strings.Contains(comments.Text(), "i18n:ignore") {
				return true
			}
		}
		if text, ok := outputText(call); ok {
			position := fset.Position(call.Pos())
			*findings = append(*findings, UnlocalizedOutput{File: filepath.ToSlash(path), Line: position.Line, Text: text})
		}
		return true
	})
	return nil
}

// ValidateMessageSources checks every statically declared Message descriptor
// against both resource catalogs, including its named argument set.
func ValidateMessageSources(root string, resourceSets ...fs.FS) error {
	english := make(map[string]string)
	chinese := make(map[string]string)
	for _, resources := range resourceSets {
		if err := fs.WalkDir(resources, ".", func(file string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() || !strings.EqualFold(path.Ext(file), ".json") {
				return err
			}
			parts := strings.Split(strings.TrimSuffix(path.Base(file), path.Ext(file)), ".")
			language := strings.ToLower(parts[len(parts)-1])
			if language != "en" && language != "zh" {
				return nil
			}
			data, err := fs.ReadFile(resources, file)
			if err != nil {
				return err
			}
			var resource map[string]coverageResourceMessage
			if err := json.Unmarshal(data, &resource); err != nil {
				return fmt.Errorf("parse translation resource %q: %w", file, err)
			}
			catalog := english
			if language == "zh" {
				catalog = chinese
			}
			for id, message := range resource {
				catalog[id] = strings.Join([]string{message.Zero, message.One, message.Two, message.Few, message.Many, message.Other}, "\n")
			}
			return nil
		}); err != nil {
			return err
		}
	}

	root = filepath.Clean(root)
	fset := token.NewFileSet()
	return filepath.WalkDir(root, func(file string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == ".git" || entry.Name() == "vendor" || entry.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(file) != ".go" || strings.HasSuffix(file, "_test.go") {
			return nil
		}
		relative, err := filepath.Rel(root, file)
		if err != nil {
			return err
		}
		switch strings.Split(filepath.ToSlash(relative), "/")[0] {
		case "app", commandDirectory, "pkg", "extension", "core", "internal":
		default:
			return nil
		}
		source, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			return err
		}
		var checkErr error
		ast.Inspect(source, func(node ast.Node) bool {
			if checkErr != nil {
				return false
			}
			if field, ok := node.(*ast.Field); ok && field.Tag != nil {
				value, _ := strconv.Unquote(field.Tag.Value)
				if id := reflect.StructTag(value).Get("comment_id"); id != "" {
					checkErr = validateCatalogMessage(fset.Position(field.Pos()), id, nil, "", english, chinese)
				}
			}
			if literal, ok := node.(*ast.CompositeLit); ok && isMessageLiteral(literal.Type) {
				id, args, defaultText, static := messageLiteralFields(literal)
				if static {
					checkErr = validateCatalogMessage(fset.Position(literal.Pos()), id, args, defaultText, english, chinese)
				}
				return checkErr == nil
			}
			if call, ok := node.(*ast.CallExpr); ok {
				id, args, static := diagnosticCallFields(call, relative)
				if static {
					checkErr = validateCatalogMessage(fset.Position(call.Pos()), id, args, "", english, chinese)
					return checkErr == nil
				}
			}
			return true
		})
		return checkErr
	})
}

func validateCatalogMessage(position token.Position, id string, args []string, defaultText string, english, chinese map[string]string) error {
	englishText, englishOK := english[id]
	chineseText, chineseOK := chinese[id]
	if !englishOK || !chineseOK {
		return fmt.Errorf("%s: message ID %q is missing from a language catalog", position, id)
	}
	if !sameStringSet(templateParameters(englishText), args) {
		return fmt.Errorf("%s: message ID %q declares args %v, but English resources use %v", position, id, args, templateParameters(englishText))
	}
	if !sameStringSet(templateParameters(chineseText), args) {
		return fmt.Errorf("%s: message ID %q declares args %v, but Chinese resources use %v", position, id, args, templateParameters(chineseText))
	}
	if defaultText != "" && !sameStringSet(templateParameters(defaultText), args) {
		return fmt.Errorf("%s: message ID %q default text uses args %v, descriptor declares %v", position, id, templateParameters(defaultText), args)
	}
	return nil
}

func templateParameters(message string) []string {
	values, _ := corei18n.TemplateParameters(message)
	return values
}

func sameStringSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	left = append([]string(nil), left...)
	right = append([]string(nil), right...)
	sort.Strings(left)
	sort.Strings(right)
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func isMessageLiteral(value ast.Expr) bool {
	switch value := value.(type) {
	case *ast.Ident:
		return value.Name == "Message"
	case *ast.SelectorExpr:
		return value.Sel.Name == "Message"
	default:
		return false
	}
}

func messageLiteralFields(literal *ast.CompositeLit) (id string, args []string, defaultText string, static bool) {
	for _, element := range literal.Elts {
		field, ok := element.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := field.Key.(*ast.Ident)
		if !ok {
			continue
		}
		switch key.Name {
		case "ID":
			value, ok := field.Value.(*ast.BasicLit)
			if ok && value.Kind == token.STRING {
				id, _ = strconv.Unquote(value.Value)
				static = id != ""
			}
		case "Args":
			if values, ok := field.Value.(*ast.CompositeLit); ok {
				for _, element := range values.Elts {
					if pair, ok := element.(*ast.KeyValueExpr); ok {
						if arg, ok := pair.Key.(*ast.BasicLit); ok && arg.Kind == token.STRING {
							value, _ := strconv.Unquote(arg.Value)
							args = append(args, value)
						}
					}
				}
			}
		case "Default":
			if value, ok := field.Value.(*ast.BasicLit); ok && value.Kind == token.STRING {
				defaultText, _ = strconv.Unquote(value.Value)
			}
		}
	}
	sort.Strings(args)
	return id, args, defaultText, static
}

func diagnosticCallFields(call *ast.CallExpr, file string) (id string, args []string, static bool) {
	name := ""
	switch function := call.Fun.(type) {
	case *ast.SelectorExpr:
		packageName, ok := function.X.(*ast.Ident)
		if !ok || packageName.Name != "diagnostic" {
			return "", nil, false
		}
		name = function.Sel.Name
	case *ast.Ident:
		path := filepath.ToSlash(file)
		if !strings.HasPrefix(path, "core/diagnostic/") && !strings.Contains(path, "/core/diagnostic/") {
			return "", nil, false
		}
		name = function.Name
	default:
		return "", nil, false
	}
	if name != "New" && name != "Wrap" || len(call.Args) < 2 {
		return "", nil, false
	}
	code, ok := call.Args[0].(*ast.BasicLit)
	if !ok || code.Kind != token.STRING {
		return "", nil, false
	}
	id, _ = strconv.Unquote(code.Value)
	if id == "" {
		return "", nil, false
	}
	if nilArg, ok := call.Args[1].(*ast.Ident); ok && nilArg.Name == "nil" {
		return id, nil, true
	}
	values, ok := call.Args[1].(*ast.CompositeLit)
	if !ok {
		return "", nil, false
	}
	for _, element := range values.Elts {
		pair, ok := element.(*ast.KeyValueExpr)
		if !ok {
			return "", nil, false
		}
		key, ok := pair.Key.(*ast.BasicLit)
		if !ok || key.Kind != token.STRING {
			return "", nil, false
		}
		value, err := strconv.Unquote(key.Value)
		if err != nil {
			return "", nil, false
		}
		args = append(args, value)
	}
	sort.Strings(args)
	return id, args, true
}
