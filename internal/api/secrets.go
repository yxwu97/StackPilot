package api

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"stackpilot/internal/domain"
	"stackpilot/internal/security"
)

type secretSetRequest struct {
	Value []byte `json:"value"`
}

type secretMetadataDTO struct {
	ID        string `json:"id"`
	SystemID  string `json:"systemId"`
	Name      string `json:"name"`
	Provider  string `json:"provider"`
	Version   int64  `json:"version"`
	UpdatedAt string `json:"updatedAt"`
}

func registerSecretRoutes(router chi.Router, provider security.SecretProvider, auth Authenticator, audit security.AuditStore, logger *slog.Logger) {
	path := "/secrets/{systemID}/{secretName}"
	router.Get(path, secretMetadataHandler(provider))
	router.With(auditMutation(audit, logger, "secret.set", "secret", ""), browserMutationGuard(auth)).Put(path, secretSetHandler(provider))
	router.With(auditMutation(audit, logger, "secret.delete", "secret", ""), browserMutationGuard(auth)).Delete(path, secretDeleteHandler(provider))
}

func secretSetHandler(provider security.SecretProvider) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		key, ok := parseSecretKey(request)
		if !ok {
			writeRegisteredError(response, request, ErrorRequestValidationFailed)
			return
		}
		var input secretSetRequest
		if err := decodeJSONRequest(response, request, &input); err != nil {
			writeRegisteredError(response, request, ErrorRequestValidationFailed)
			return
		}
		defer clearBytes(input.Value)
		metadata, err := provider.Set(request.Context(), key, input.Value)
		if err != nil {
			writeBoundaryError(response, request, err)
			return
		}
		writeJSON(response, http.StatusOK, mapSecretMetadata(metadata))
	}
}

func secretMetadataHandler(provider security.SecretProvider) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		key, ok := parseSecretKey(request)
		if !ok {
			writeRegisteredError(response, request, ErrorRequestValidationFailed)
			return
		}
		metadata, found, err := provider.Metadata(request.Context(), key)
		if err != nil {
			writeBoundaryError(response, request, err)
			return
		}
		if !found {
			writeRegisteredError(response, request, ErrorSecretNotFound)
			return
		}
		writeJSON(response, http.StatusOK, mapSecretMetadata(metadata))
	}
}

func secretDeleteHandler(provider security.SecretProvider) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		key, ok := parseSecretKey(request)
		if !ok {
			writeRegisteredError(response, request, ErrorRequestValidationFailed)
			return
		}
		metadata, found, err := provider.Metadata(request.Context(), key)
		if err != nil {
			writeBoundaryError(response, request, err)
			return
		}
		if !found {
			writeRegisteredError(response, request, ErrorSecretNotFound)
			return
		}
		if err := provider.Delete(request.Context(), key); err != nil {
			writeBoundaryError(response, request, err)
			return
		}
		writeJSON(response, http.StatusOK, mapSecretMetadata(metadata))
	}
}

func parseSecretKey(request *http.Request) (security.SecretKey, bool) {
	systemID, err := domain.ParseSystemID(chi.URLParam(request, "systemID"))
	if err != nil {
		return security.SecretKey{}, false
	}
	key := security.SecretKey{SystemID: systemID, Name: chi.URLParam(request, "secretName")}
	return key, security.ValidateSecretKey(key) == nil
}

func mapSecretMetadata(metadata security.SecretMetadata) secretMetadataDTO {
	id := metadata.Key.SystemID.String() + "/" + metadata.Key.Name
	return secretMetadataDTO{ID: id, SystemID: metadata.Key.SystemID.String(), Name: metadata.Key.Name,
		Provider: metadata.Provider, Version: metadata.Version, UpdatedAt: formatAPITime(metadata.UpdatedAt)}
}

func clearBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
