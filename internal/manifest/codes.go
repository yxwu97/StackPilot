package manifest

import "errors"

const (
	CodeFileTooLarge         = "MANIFEST_FILE_TOO_LARGE"
	CodeNotRegularFile       = "MANIFEST_NOT_REGULAR_FILE"
	CodeYAMLInvalid          = "MANIFEST_YAML_INVALID"
	CodeDuplicateKey         = "MANIFEST_DUPLICATE_KEY"
	CodeUnknownField         = "MANIFEST_UNKNOWN_FIELD"
	CodeMultipleDocuments    = "MANIFEST_MULTIPLE_DOCUMENTS"
	CodeSchemaInvalid        = "MANIFEST_SCHEMA_INVALID"
	CodeSemanticInvalid      = "MANIFEST_SEMANTIC_INVALID"
	CodePathOutsideWorkspace = "MANIFEST_PATH_OUTSIDE_WORKSPACE"
	CodeCycleDetected        = "MANIFEST_CYCLE_DETECTED"
	CodeReferenceNotFound    = "MANIFEST_REFERENCE_NOT_FOUND"
	CodeTemplateInvalid      = "MANIFEST_TEMPLATE_INVALID"
	CodeDurationInvalid      = "MANIFEST_DURATION_INVALID"
	CodePortRangeInvalid     = "MANIFEST_PORT_RANGE_INVALID"
	CodeHealthTargetUnsafe   = "MANIFEST_HEALTH_TARGET_UNSAFE"
	CodeFeatureNotEnabled    = "FEATURE_NOT_ENABLED"
)

// ErrorCode returns the stable public category for a recognized manifest error.
func ErrorCode(err error) (string, bool) {
	categories := []struct {
		err  error
		code string
	}{
		{ErrFileTooLarge, CodeFileTooLarge}, {ErrNotRegularFile, CodeNotRegularFile},
		{ErrMalformedYAML, CodeYAMLInvalid}, {ErrDuplicateKey, CodeDuplicateKey},
		{ErrUnknownField, CodeUnknownField}, {ErrMultipleDocuments, CodeMultipleDocuments},
		{ErrSchemaInvalid, CodeSchemaInvalid}, {ErrPathOutsideWorkspace, CodePathOutsideWorkspace},
		{ErrDependencyCycle, CodeCycleDetected}, {ErrReferenceNotFound, CodeReferenceNotFound},
		{ErrTemplateInvalid, CodeTemplateInvalid}, {ErrDurationInvalid, CodeDurationInvalid},
		{ErrPortRangeInvalid, CodePortRangeInvalid}, {ErrHealthTargetUnsafe, CodeHealthTargetUnsafe},
		{ErrFeatureNotEnabled, CodeFeatureNotEnabled}, {ErrSemanticInvalid, CodeSemanticInvalid},
	}
	for _, category := range categories {
		if errors.Is(err, category.err) {
			return category.code, true
		}
	}
	return "", false
}
