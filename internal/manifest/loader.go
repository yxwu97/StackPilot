package manifest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v5"
	"gopkg.in/yaml.v3"

	"stackpilot/schemas"
)

const (
	// MaxFileSize is the fixed upper bound for a system manifest.
	MaxFileSize = 1 << 20
	schemaURL   = "https://stackpilot.dev/schemas/system-v1alpha1.schema.json"
)

// Loader performs bounded YAML parsing and structural Schema validation.
type Loader struct {
	schema *jsonschema.Schema
}

// NewLoader compiles the embedded v1alpha1 Schema.
func NewLoader() (*Loader, error) {
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(schemaURL, bytes.NewReader(schemas.SystemV1Alpha1())); err != nil {
		return nil, fmt.Errorf("add manifest schema: %w", err)
	}
	compiled, err := compiler.Compile(schemaURL)
	if err != nil {
		return nil, fmt.Errorf("compile manifest schema: %w", err)
	}
	return &Loader{schema: compiled}, nil
}

// Load reads one regular manifest file within the fixed size bound.
func (l *Loader) Load(ctx context.Context, path string) (document *Document, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open manifest: %w", err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close manifest: %w", closeErr))
		}
	}()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat manifest: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, ErrNotRegularFile
	}
	if info.Size() > MaxFileSize {
		return nil, ErrFileTooLarge
	}
	contents, err := io.ReadAll(io.LimitReader(file, MaxFileSize+1))
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	if len(contents) > MaxFileSize {
		return nil, ErrFileTooLarge
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return l.Parse(contents)
}

// Parse validates a single in-memory YAML document.
func (l *Loader) Parse(contents []byte) (*Document, error) {
	if len(contents) > MaxFileSize {
		return nil, ErrFileTooLarge
	}
	root, err := decodeSingleDocument(contents)
	if err != nil {
		return nil, err
	}
	if err := validateNode(root.Content[0], reflect.TypeOf(Manifest{}), "$"); err != nil {
		return nil, err
	}
	var typed Manifest
	if err := root.Content[0].Decode(&typed); err != nil {
		return nil, newValidationError("$", "", ErrMalformedYAML)
	}
	normalized, value, err := normalizeAsJSON(root.Content[0])
	if err != nil {
		return nil, err
	}
	if err := l.schema.Validate(value); err != nil {
		return nil, newValidationError("$", "", ErrSchemaInvalid)
	}
	return &Document{Manifest: typed, JSON: normalized}, nil
}

func decodeSingleDocument(contents []byte) (*yaml.Node, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(contents))
	var root yaml.Node
	if err := decoder.Decode(&root); err != nil {
		return nil, newValidationError("$", "", ErrMalformedYAML)
	}
	if len(root.Content) != 1 {
		return nil, newValidationError("$", "", ErrMalformedYAML)
	}
	var extra yaml.Node
	err := decoder.Decode(&extra)
	if err == nil {
		return nil, newValidationError("$", "", ErrMultipleDocuments)
	}
	if !errors.Is(err, io.EOF) {
		return nil, newValidationError("$", "", ErrMalformedYAML)
	}
	return &root, nil
}

func normalizeAsJSON(node *yaml.Node) ([]byte, any, error) {
	var value any
	if err := node.Decode(&value); err != nil {
		return nil, nil, newValidationError("$", "", ErrMalformedYAML)
	}
	contents, err := json.Marshal(value)
	if err != nil {
		return nil, nil, newValidationError("$", "", ErrMalformedYAML)
	}
	var normalized any
	if err := json.Unmarshal(contents, &normalized); err != nil {
		return nil, nil, fmt.Errorf("decode normalized manifest JSON: %w", err)
	}
	return contents, normalized, nil
}

func validateNode(node *yaml.Node, expected reflect.Type, path string) error {
	for expected.Kind() == reflect.Pointer {
		expected = expected.Elem()
	}
	if node.Kind == yaml.AliasNode {
		return validateNode(node.Alias, expected, path)
	}
	switch expected.Kind() {
	case reflect.Struct:
		return validateStructNode(node, expected, path)
	case reflect.Map:
		return validateMapNode(node, expected.Elem(), path)
	case reflect.Slice:
		return validateSequenceNode(node, expected.Elem(), path)
	default:
		return nil
	}
}

func validateStructNode(node *yaml.Node, expected reflect.Type, path string) error {
	if node.Kind != yaml.MappingNode {
		return nil
	}
	fields := yamlFields(expected)
	seen := make(map[string]struct{}, len(node.Content)/2)
	for index := 0; index < len(node.Content); index += 2 {
		key, value := node.Content[index], node.Content[index+1]
		if _, ok := seen[key.Value]; ok {
			return newValidationError(path, key.Value, ErrDuplicateKey)
		}
		seen[key.Value] = struct{}{}
		fieldType, ok := fields[key.Value]
		if !ok {
			return newValidationError(path, key.Value, ErrUnknownField)
		}
		if err := validateNode(value, fieldType, childPath(path, key.Value)); err != nil {
			return err
		}
	}
	return nil
}

func validateMapNode(node *yaml.Node, element reflect.Type, path string) error {
	if node.Kind != yaml.MappingNode {
		return nil
	}
	seen := make(map[string]struct{}, len(node.Content)/2)
	for index := 0; index < len(node.Content); index += 2 {
		key, value := node.Content[index], node.Content[index+1]
		if _, ok := seen[key.Value]; ok {
			return newValidationError(path, key.Value, ErrDuplicateKey)
		}
		seen[key.Value] = struct{}{}
		if err := validateNode(value, element, childPath(path, key.Value)); err != nil {
			return err
		}
	}
	return nil
}

func validateSequenceNode(node *yaml.Node, element reflect.Type, path string) error {
	if node.Kind != yaml.SequenceNode {
		return nil
	}
	for index, item := range node.Content {
		if err := validateNode(item, element, fmt.Sprintf("%s[%d]", path, index)); err != nil {
			return err
		}
	}
	return nil
}

func yamlFields(structType reflect.Type) map[string]reflect.Type {
	result := make(map[string]reflect.Type, structType.NumField())
	for index := 0; index < structType.NumField(); index++ {
		field := structType.Field(index)
		name := strings.Split(field.Tag.Get("yaml"), ",")[0]
		if name != "" && name != "-" {
			result[name] = field.Type
		}
	}
	return result
}

func childPath(parent, child string) string { return parent + "." + child }
