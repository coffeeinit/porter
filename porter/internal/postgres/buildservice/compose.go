package buildservice

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"
)

// ComposeBuild is a parsed, path-confined build target. The validator accepts
// only one service with a build context and Dockerfile; it rejects execution,
// mounts, privileges, dependencies, networks, secrets, and all other Compose
// behavior. A guest-side runtime must call this validator on the checked-out
// Compose file before starting its guest-local build engine.
type ComposeBuild struct {
	PrimaryService string
	Context        string
	Dockerfile     string
}

func ValidateComposeSubset(data []byte, primaryService string) (ComposeBuild, error) {
	if len(data) == 0 || len(data) > 1<<20 || !servicePattern.MatchString(primaryService) {
		return ComposeBuild{}, fmt.Errorf("%w: Compose document or primary service is invalid", ErrInvalidRequest)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return ComposeBuild{}, fmt.Errorf("%w: invalid Compose YAML", ErrInvalidRequest)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		return ComposeBuild{}, fmt.Errorf("%w: multiple Compose YAML documents are not supported", ErrInvalidRequest)
	}
	if containsYAMLAlias(&document) {
		return ComposeBuild{}, fmt.Errorf("%w: YAML aliases and anchors are not supported", ErrInvalidRequest)
	}
	if document.Kind != yaml.DocumentNode || len(document.Content) != 1 {
		return ComposeBuild{}, fmt.Errorf("%w: Compose YAML document root is invalid", ErrInvalidRequest)
	}
	root, err := yamlMapping(document.Content[0])
	if err != nil {
		return ComposeBuild{}, err
	}
	for key, value := range root {
		switch key {
		case "version":
			if value.Kind != yaml.ScalarNode || value.Tag != "!!str" {
				return ComposeBuild{}, fmt.Errorf("%w: Compose version must be a string", ErrInvalidRequest)
			}
		case "services":
		default:
			return ComposeBuild{}, fmt.Errorf("%w: unsupported Compose top-level key %q", ErrInvalidRequest, key)
		}
	}
	servicesNode, ok := root["services"]
	if !ok {
		return ComposeBuild{}, fmt.Errorf("%w: Compose services are required", ErrInvalidRequest)
	}
	services, err := yamlMapping(servicesNode)
	if err != nil {
		return ComposeBuild{}, err
	}
	if len(services) != 1 {
		return ComposeBuild{}, fmt.Errorf("%w: only one Compose service is supported", ErrInvalidRequest)
	}
	serviceNode, ok := services[primaryService]
	if !ok {
		return ComposeBuild{}, fmt.Errorf("%w: selected primary service is not the sole Compose service", ErrInvalidRequest)
	}
	service, err := yamlMapping(serviceNode)
	if err != nil {
		return ComposeBuild{}, err
	}
	for key := range service {
		if key != "build" && key != "image" {
			return ComposeBuild{}, fmt.Errorf("%w: unsupported Compose service key %q", ErrInvalidRequest, key)
		}
	}
	if image, ok := service["image"]; ok && (image.Kind != yaml.ScalarNode || image.Tag != "!!str" || strings.TrimSpace(image.Value) == "") {
		return ComposeBuild{}, fmt.Errorf("%w: Compose image must be a non-empty string", ErrInvalidRequest)
	}
	buildNode, ok := service["build"]
	if !ok {
		return ComposeBuild{}, fmt.Errorf("%w: selected Compose service must define build", ErrInvalidRequest)
	}
	contextPath := ""
	dockerfile := "Dockerfile"
	if buildNode.Kind == yaml.ScalarNode && buildNode.Tag == "!!str" {
		contextPath = buildNode.Value
	} else {
		build, err := yamlMapping(buildNode)
		if err != nil {
			return ComposeBuild{}, err
		}
		for key := range build {
			if key != "context" && key != "dockerfile" {
				return ComposeBuild{}, fmt.Errorf("%w: unsupported Compose build key %q", ErrInvalidRequest, key)
			}
		}
		if node, ok := build["context"]; ok && node.Kind == yaml.ScalarNode && node.Tag == "!!str" {
			contextPath = node.Value
		} else {
			return ComposeBuild{}, fmt.Errorf("%w: Compose build.context must be a string", ErrInvalidRequest)
		}
		if node, ok := build["dockerfile"]; ok && node.Kind == yaml.ScalarNode && node.Tag == "!!str" {
			dockerfile = node.Value
		} else if _, ok := build["dockerfile"]; ok {
			return ComposeBuild{}, fmt.Errorf("%w: Compose build.dockerfile must be a string", ErrInvalidRequest)
		}
	}
	if !validContextPath(contextPath) || !validBuildPath(dockerfile) {
		return ComposeBuild{}, fmt.Errorf("%w: Compose context and Dockerfile must be safe relative paths", ErrInvalidRequest)
	}
	return ComposeBuild{PrimaryService: primaryService, Context: contextPath, Dockerfile: dockerfile}, nil
}

func validContextPath(raw string) bool {
	raw = strings.TrimSpace(raw)
	return raw == "." || validBuildPath(raw)
}

func yamlMapping(node *yaml.Node) (map[string]*yaml.Node, error) {
	if node == nil || node.Kind != yaml.MappingNode || len(node.Content)%2 != 0 {
		return nil, fmt.Errorf("%w: expected a YAML mapping", ErrInvalidRequest)
	}
	result := make(map[string]*yaml.Node, len(node.Content)/2)
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i]
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || key.Value == "" {
			return nil, fmt.Errorf("%w: YAML mapping keys must be non-empty strings", ErrInvalidRequest)
		}
		if _, exists := result[key.Value]; exists {
			return nil, fmt.Errorf("%w: duplicate YAML mapping key %q", ErrInvalidRequest, key.Value)
		}
		result[key.Value] = node.Content[i+1]
	}
	return result, nil
}

func containsYAMLAlias(node *yaml.Node) bool {
	if node == nil || node.Kind == yaml.AliasNode || node.Anchor != "" {
		return node != nil
	}
	for _, child := range node.Content {
		if containsYAMLAlias(child) {
			return true
		}
	}
	return false
}
