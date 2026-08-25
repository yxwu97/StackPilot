package cli

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"stackpilot/internal/domain"
	"stackpilot/internal/security"
)

func (run runner) secret(args []string) int {
	if len(args) == 0 {
		return run.secretUsage()
	}
	switch args[0] {
	case "set":
		return run.setSecret(args[1:])
	case "get-metadata":
		return run.readSecretMetadata(args[1:])
	case "delete":
		return run.deleteSecret(args[1:])
	default:
		return run.secretUsage()
	}
}

func (run runner) setSecret(args []string) int {
	flags, config, key, code := run.parseSecretCommand("secret set", args)
	if code != 0 {
		return code
	}
	_ = flags
	value, err := readSecretValue(run.stdin, run.stderr)
	if err != nil {
		return run.report(err)
	}
	defer erase(value)
	client, code := run.connect(config)
	if code != 0 {
		return code
	}
	defer client.Close()
	var metadata secretMetadataDTO
	input := struct {
		Value []byte `json:"value"`
	}{Value: value}
	err = client.JSON(run.ctx, http.MethodPut, secretPath(key), input, &metadata)
	if err != nil {
		return run.report(err)
	}
	return run.writeSecretMetadata(config.output, metadata)
}

func (run runner) readSecretMetadata(args []string) int {
	_, config, key, code := run.parseSecretCommand("secret get-metadata", args)
	if code != 0 {
		return code
	}
	client, code := run.connect(config)
	if code != 0 {
		return code
	}
	defer client.Close()
	var metadata secretMetadataDTO
	if err := client.JSON(run.ctx, http.MethodGet, secretPath(key), nil, &metadata); err != nil {
		return run.report(err)
	}
	return run.writeSecretMetadata(config.output, metadata)
}

func (run runner) deleteSecret(args []string) int {
	_, config, key, code := run.parseSecretCommand("secret delete", args)
	if code != 0 {
		return code
	}
	client, code := run.connect(config)
	if code != 0 {
		return code
	}
	defer client.Close()
	var metadata secretMetadataDTO
	if err := client.JSON(run.ctx, http.MethodDelete, secretPath(key), nil, &metadata); err != nil {
		return run.report(err)
	}
	return run.writeSecretMetadata(config.output, metadata)
}

func (run runner) parseSecretCommand(name string, args []string) (*flag.FlagSet, *commandConfig, security.SecretKey, int) {
	flags, config := newCommandFlags(name, run.stderr)
	if err := flags.Parse(reorderFlagArgs(args, nil)); err != nil || flags.NArg() != 2 {
		return flags, config, security.SecretKey{}, 2
	}
	systemID, err := domain.ParseSystemID(flags.Arg(0))
	if err != nil {
		return flags, config, security.SecretKey{}, 2
	}
	key := security.SecretKey{SystemID: systemID, Name: flags.Arg(1)}
	if security.ValidateSecretKey(key) != nil {
		return flags, config, security.SecretKey{}, 2
	}
	return flags, config, key, 0
}

func (run runner) writeSecretMetadata(output string, metadata secretMetadataDTO) int {
	return run.write(output, metadata, func() {
		fmt.Fprintf(run.stdout, "%s/%s\t%s\t%d\t%s\n",
			metadata.SystemID, metadata.Name, metadata.Provider, metadata.Version, metadata.UpdatedAt)
	})
}

func (run runner) secretUsage() int {
	fprintln(run.stderr, "Usage: stackpilot secret set|get-metadata|delete [flags] <system> <name>")
	return 2
}

func secretPath(key security.SecretKey) string {
	return "/api/v1/secrets/" + url.PathEscape(key.SystemID.String()) + "/" + url.PathEscape(key.Name)
}

func readSecretValue(input io.Reader, prompt io.Writer) ([]byte, error) {
	if value, handled, err := readHiddenSecret(input, prompt); handled {
		return value, err
	}
	payload, err := io.ReadAll(io.LimitReader(input, security.MaximumSecretValueSize+3))
	if err != nil {
		return nil, fmt.Errorf("read Secret from stdin: %w", err)
	}
	payload = trimInputNewline(payload)
	if len(payload) == 0 || len(payload) > security.MaximumSecretValueSize {
		erase(payload)
		return nil, commandErrorf("Secret input must contain 1 through %d bytes", security.MaximumSecretValueSize)
	}
	return payload, nil
}

func trimInputNewline(value []byte) []byte {
	if len(value) > 0 && value[len(value)-1] == '\n' {
		value = value[:len(value)-1]
		if len(value) > 0 && value[len(value)-1] == '\r' {
			value = value[:len(value)-1]
		}
	}
	return value
}
