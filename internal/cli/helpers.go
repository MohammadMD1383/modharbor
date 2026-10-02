package cli

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/MohammadMD1383/modharbor/internal/app"
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

// requireInstanceArg resolves an instance reference from args, erroring with a
// helpful message when neither the argument nor the flag names one.
func requireInstanceArg(args []string) (string, error) {
	ref := pickInstanceArg(args)
	if ref == "" {
		return "", errNoInstance
	}
	return ref, nil
}

// resolveInstance turns a command's arguments into the instance it should act
// on, and is the single path every instance-taking command resolves through so
// the missing-instance message never varies between them.
//
// An empty reference is not yet a failure: the configured defaultInstance is
// about to be applied, and failing first would break every user who set one.
// Only once there is no default either does errNoInstance surface — which is
// why this cannot live inside requireInstanceArg, which sees no config.
//
// Commands that parse the instance out of a mixed argument list (`add sodium
// 26.3-fabric-mod`) pass the reference their own parsing settled on as the
// sole element, which is exactly what pickInstanceArg returns for it.
//
// The error is returned rather than wrapped in fail so that Execute still
// recognises it and adds the "try: modharbor instances" hint.
func resolveInstance(a *app.App, args []string) (*instance.Info, error) {
	ref, err := requireInstanceArg(args)
	if err != nil && a.Config.DefaultInstance == "" {
		return nil, err
	}
	return a.ResolveInstance(ref)
}

// errNoInstance is returned when no instance could be determined.
var errNoInstance = &missingInstanceError{}

type missingInstanceError struct{}

func (e *missingInstanceError) Error() string {
	return "no instance specified; pass one as an argument or set a default with `modharbor config set defaultInstance <id>`"
}
