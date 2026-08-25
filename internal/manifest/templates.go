package manifest

import (
	"strconv"
	"strings"

	"stackpilot/internal/domain"
)

const secretTemplatePrefix = "${secret."

func validateServiceTemplates(service Service, ports map[string]Port, path string) error {
	for index, argument := range service.Arguments {
		if err := validateTemplateValue(argument, ports, false, indexedPath(path+".arguments", index)); err != nil {
			return err
		}
	}
	for name, value := range service.Environment {
		if err := validateTemplateValue(value, ports, true, path+".environment."+name); err != nil {
			return err
		}
	}
	if service.Readiness != nil {
		if err := validateTemplateValue(service.Readiness.URL, ports, false, path+".readiness.url"); err != nil {
			return err
		}
		if portTemplate, ok := service.Readiness.Port.(string); ok {
			if err := validateTemplateValue(portTemplate, ports, false, path+".readiness.port"); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateTemplateValue(value string, ports map[string]Port, allowSecret bool, path string) error {
	if strings.Contains(value, "$(") || strings.Contains(value, "`") {
		return newValidationError(path, "", ErrTemplateInvalid)
	}
	remaining := value
	for {
		start := strings.Index(remaining, "${")
		if start < 0 {
			return nil
		}
		endOffset := strings.IndexByte(remaining[start+2:], '}')
		if endOffset < 0 {
			return newValidationError(path, "", ErrTemplateInvalid)
		}
		end := start + 2 + endOffset
		token := remaining[start+2 : end]
		if strings.HasPrefix(token, "secret.") && value != "${"+token+"}" {
			return newValidationError(path, token, ErrTemplateInvalid)
		}
		if err := validateTemplateToken(token, ports, allowSecret, path); err != nil {
			return err
		}
		remaining = remaining[end+1:]
	}
}

func validateTemplateToken(token string, ports map[string]Port, allowSecret bool, path string) error {
	switch token {
	case "workspace.root", "instance.id", "system.id":
		return nil
	}
	if name, ok := strings.CutPrefix(token, "ports."); ok && name != "" {
		if _, exists := ports[name]; !exists {
			return newValidationError(path, name, ErrReferenceNotFound)
		}
		return nil
	}
	if name, ok := strings.CutPrefix(token, "secret."); ok && name != "" {
		if !allowSecret {
			return newValidationError(path, name, ErrTemplateInvalid)
		}
		if _, err := domain.ParseServiceID(name); err != nil {
			return newValidationError(path, name, ErrTemplateInvalid)
		}
		return nil
	}
	return newValidationError(path, token, ErrTemplateInvalid)
}

// SecretReference returns the name from an exact environment Secret placeholder.
func SecretReference(value string) (string, bool) {
	if !strings.HasPrefix(value, secretTemplatePrefix) || !strings.HasSuffix(value, "}") {
		return "", false
	}
	name := strings.TrimSuffix(strings.TrimPrefix(value, secretTemplatePrefix), "}")
	if _, err := domain.ParseServiceID(name); err != nil {
		return "", false
	}
	return name, value == secretTemplatePrefix+name+"}"
}

func containsTemplateSyntax(value string) bool {
	return strings.Contains(value, "${") || strings.Contains(value, "$(") || strings.Contains(value, "`")
}

func indexedPath(path string, index int) string {
	return path + "[" + strconv.Itoa(index) + "]"
}
