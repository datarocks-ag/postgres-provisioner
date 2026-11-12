package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

// maxSecretFileSize caps how much is read from a *_FILE secret, guarding against
// pointing a variable at a large or endless file (e.g. /dev/zero) by mistake.
const maxSecretFileSize = 64 * 1024

// envVarNamePattern is the set of names accepted inside ${...}.
var envVarNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// LookupEnvOrFile resolves name from the environment, falling back to the contents of
// the file named by name_FILE (the Docker/Kubernetes secrets convention). Trailing
// CR/LF characters are trimmed from file contents.
//
// It reports ok=false when neither variable is set. Setting both is an error, because
// it is ambiguous which one should win. Errors never include the secret value.
func LookupEnvOrFile(name string) (value string, ok bool, err error) {
	fileVar := name + "_FILE"
	val, hasVal := os.LookupEnv(name)
	path, hasFile := os.LookupEnv(fileVar)

	switch {
	case hasVal && hasFile:
		return "", false, fmt.Errorf("both %s and %s are set", name, fileVar)
	case hasVal:
		return val, true, nil
	case hasFile:
		content, err := readSecretFile(path)
		if err != nil {
			return "", false, fmt.Errorf("reading %s %q: %w", fileVar, path, err)
		}
		return content, true, nil
	default:
		return "", false, nil
	}
}

func readSecretFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	// Read-only handle: a Close error cannot lose data, so it is safe to ignore.
	defer func() { _ = f.Close() }()

	data, err := io.ReadAll(io.LimitReader(f, maxSecretFileSize+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxSecretFileSize {
		return "", fmt.Errorf("file exceeds %d bytes", maxSecretFileSize)
	}
	return strings.TrimRight(string(data), "\r\n"), nil
}

// expander expands ${VAR} references and collects every resolution failure, so a
// misconfigured deployment sees all missing variables in a single run.
type expander struct {
	errs []error
}

// expand replaces ${VAR} and ${VAR:-default} references in s. path identifies the
// config field in error messages.
func (e *expander) expand(path, s string) string {
	out, _ := e.expandReferences(path, s)
	return out
}

// expandSecret expands a secret field and additionally rejects references that resolve
// to an empty string: an empty password means "no password", which would otherwise
// hide a missing secret behind a successful run.
func (e *expander) expandSecret(path, s string) string {
	expanded, hasReference := e.expandReferences(path, s)
	if expanded == "" && hasReference {
		e.errs = append(e.errs, fmt.Errorf("%s: environment variable reference resolved to an empty value", path))
	}
	return expanded
}

// expandReferences scans s left to right, resolving each ${...} reference, and reports
// whether s contained any reference. A reference ends at the first '}'; an
// unterminated "${" is kept as literal text.
func (e *expander) expandReferences(path, s string) (string, bool) {
	var out strings.Builder
	hasReference := false
	for {
		start := strings.Index(s, "${")
		if start < 0 {
			break
		}
		length := strings.IndexByte(s[start+2:], '}')
		if length < 0 {
			break
		}
		end := start + 2 + length // index of the closing '}'
		out.WriteString(s[:start])
		out.WriteString(e.resolve(path, s[start:end+1]))
		hasReference = true
		s = s[end+1:]
	}
	out.WriteString(s)
	return out.String(), hasReference
}

// resolve returns the value for a single ${...} reference, recording an error and
// returning the reference unchanged when it cannot be resolved.
func (e *expander) resolve(path, reference string) string {
	body := reference[2 : len(reference)-1]
	name, defaultVal, hasDefault := strings.Cut(body, ":-")
	if !envVarNamePattern.MatchString(name) {
		e.errs = append(e.errs, fmt.Errorf("%s: invalid environment variable reference %q", path, reference))
		return reference
	}

	val, ok, err := LookupEnvOrFile(name)
	if err != nil {
		e.errs = append(e.errs, fmt.Errorf("%s: %w", path, err))
		return reference
	}
	if ok {
		return val
	}
	if hasDefault {
		return defaultVal
	}
	e.errs = append(e.errs, fmt.Errorf("%s: environment variable %s is not set (set %s or %s_FILE)", path, name, name, name))
	return reference
}

func (e *expander) err() error {
	return errors.Join(e.errs...)
}
