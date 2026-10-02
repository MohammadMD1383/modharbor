package cli

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/MohammadMD1383/modharbor/internal/instance"
)

// pickInstanceArg returns the instance reference from a positional argument,
// falling back to the global --instance flag and then the config default.
func pickInstanceArg(args []string) string {
	if len(args) > 0 && args[0] != "" {
		return args[0]
	}
	return flagInstance
}

// loaderName maps an instance's loader type to a Modrinth loader tag.
func loaderName(t instance.Type) string {
	switch t {
	case instance.TypeFabric:
		return "fabric"
	case instance.TypeQuilt:
		return "quilt"
	case instance.TypeForge:
		return "forge"
	case instance.TypeNeoForge:
		return "neoforge"
	default:
		return ""
	}
}

// loaderLabel renders a loader type for display.
func loaderLabel(t instance.Type) string {
	if t == instance.TypeUnknown || t == instance.TypeVanilla {
		return "vanilla"
	}
	return strings.ToLower(string(t))
}

// filepathBase is a tiny indirection that keeps call sites terse.
func filepathBase(p string) string { return filepath.Base(p) }

// statFile returns file info, or nil when unavailable.
func statFile(p string) (os.FileInfo, error) { return os.Stat(p) }

// requireInstanceArg resolves an instance from args, erroring with a helpful
// message when the argument is missing.
func requireInstanceArg(args []string) (string, error) {
	ref := pickInstanceArg(args)
	if ref == "" {
		return "", errNoInstance
	}
	return ref, nil
}

// errNoInstance is returned when no instance could be determined.
var errNoInstance = &missingInstanceError{}

type missingInstanceError struct{}

func (e *missingInstanceError) Error() string {
	return "no instance specified; pass one as an argument or set a default with `modharbor config set defaultInstance <id>`"
}
