package cli

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/MohammadMD1383/modharbor/internal/app"
	"github.com/MohammadMD1383/modharbor/internal/hashutil"
	"github.com/MohammadMD1383/modharbor/internal/instance"
	"github.com/MohammadMD1383/modharbor/internal/modmeta"
	"github.com/MohammadMD1383/modharbor/internal/provider/modrinth"
	"github.com/MohammadMD1383/modharbor/internal/store"
	"github.com/MohammadMD1383/modharbor/internal/ui"
	"github.com/spf13/cobra"
)

func newLinkCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "link <mod> <project> [instance]",
		Short: "Pin a mod to a specific Modrinth project",
		Long: strings.TrimSpace(`
Teach modharbor that a jar is a particular Modrinth project.

Useful when automatic identification gets it wrong, or for mods that are not
published anywhere but that you want mapped to an upstream project so updates
can be tracked.

  modharbor link moretools AANobbMI
  modharbor link moretools-1.0.0.jar sodium

Manual links are authoritative: they are never overwritten by automatic
identification, and they survive cache expiry.
`),
		Example: strings.TrimSpace(`
  modharbor link more-tools sodium
  modharbor link ipn libipn 26.3-fabric-mod
`),
		Args: cobra.RangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := bootstrap()
			if err != nil {
				return fail("%v", err)
			}
			// `link <mod> <project>` uses the default instance;
			// `link <instance> <mod> <project>` names one explicitly.
			var instRef, modRef, projectRef string
			if len(args) == 3 {
				instRef, modRef, projectRef = args[0], args[1], args[2]
			} else {
				instRef, modRef, projectRef = flagInstance, args[0], args[1]
			}
			inst, err := resolveInstance(a, []string{instRef})
			if err != nil {
				return err
			}

			jar, sha1, err := findJar(inst, modRef)
			if err != nil {
				return fail("%v", err)
			}

			proj, err := a.MR().Project(cmd.Context(), projectKey(projectRef))
			if err != nil {
				return fail("resolving %q: %v", projectRef, err)
			}

			st, err := a.State()
			if err != nil {
				return fail("%v", err)
			}

			res := store.Resolution{
				SHA1:       sha1,
				Provider:   "modrinth",
				ProjectID:  proj.ID,
				Slug:       proj.Slug,
				Title:      proj.Title,
				Method:     "manual",
				Confidence: 1,
				SourceFile: jar,
				InstanceID: inst.ID,
			}
			// Pin the newest compatible version too, so list and doctor show
			// something sensible straight away.
			if v := newestForInstance(a, inst, proj.ID); v != nil {
				res.VersionID = v.ID
				res.VersionNumber = v.VersionNumber
				if f, ok := v.PrimaryFile(); ok {
					res.Filename = f.Filename
				}
			}
			st.PutManual(res)
			if err := st.Save(); err != nil {
				return fail("%v", err)
			}

			if flagJSON {
				return printJSON(res)
			}
			ui.Success("%s is now pinned to %s", ui.Bold(jar), ui.OK(proj.Title))
			ui.Note("sha1 %s", ui.Faint(shortSHA(sha1)))
			ui.Blank()
			return nil
		},
	}
	return cmd
}

func newUnlinkCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "unlink <mod> [instance]",
		Aliases: []string{"unpin"},
		Short:   "Remove a manual link so automatic identification resumes",
		Args:    cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := bootstrap()
			if err != nil {
				return fail("%v", err)
			}
			instRef, modRef := flagInstance, args[0]
			if len(args) == 2 {
				instRef, modRef = args[0], args[1]
			}
			inst, err := resolveInstance(a, []string{instRef})
			if err != nil {
				return err
			}
			_, sha1, err := findJar(inst, modRef)
			if err != nil {
				return fail("%v", err)
			}
			st, err := a.State()
			if err != nil {
				return fail("%v", err)
			}
			if prev, ok := st.Lookup(sha1); !ok || prev.Method != "manual" {
				ui.Warn("%s was not manually linked", modRef)
				return nil
			}
			st.Forget(sha1)
			if err := st.Save(); err != nil {
				return fail("%v", err)
			}
			ui.Success("unlinked %s", ui.Bold(shortSHA(sha1)))
			ui.Blank()
			return nil
		},
	}
}

// findJar resolves a user-supplied mod reference to a jar in an instance,
// returning its file name and SHA-1.
func findJar(inst *instance.Info, ref string) (name, sha1 string, err error) {
	files, err := instance.ModFiles(inst.ModsDirOrDefault())
	if err != nil {
		return "", "", err
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", "", fmt.Errorf("no mod given")
	}

	// Exact file name first.
	for _, f := range files {
		if strings.EqualFold(f, ref) || strings.EqualFold(baseName(f), ref) {
			s, err := hashutil.SHA1File(f)
			if err != nil {
				return "", "", err
			}
			return baseName(f), s, nil
		}
	}
	// Hash, so a copied digest works too.
	if len(ref) >= 32 && isHex(ref) {
		for _, f := range files {
			s, err := hashutil.SHA1File(f)
			if err != nil {
				continue
			}
			if strings.EqualFold(s, ref) {
				return baseName(f), s, nil
			}
		}
	}
	// Otherwise fall back to a normalised comparison, so "more-tools"
	// matches "moretools-1.0.0.jar" and "ipn" matches "libIPN-fabric...".
	var matches []string
	normRef := modmeta.NormalizeName(ref)
	for _, f := range files {
		name := baseName(f)
		if strings.Contains(strings.ToLower(name), strings.ToLower(ref)) ||
			strings.Contains(modmeta.NormalizeName(name), normRef) {
			matches = append(matches, f)
		}
	}
	if len(matches) == 1 {
		s, err := hashutil.SHA1File(matches[0])
		if err != nil {
			return "", "", err
		}
		return baseName(matches[0]), s, nil
	}
	if len(matches) > 1 {
		sort.Strings(matches)
		var names []string
		for _, m := range matches {
			names = append(names, baseName(m))
		}
		return "", "", fmt.Errorf("%q matches %d jars: %s", ref, len(matches), strings.Join(names, ", "))
	}
	return "", "", fmt.Errorf("no jar matching %q in %s", ref, inst.ModsDirOrDefault())
}

func baseName(p string) string {
	if i := strings.LastIndexAny(p, "/\\"); i >= 0 {
		return p[i+1:]
	}
	return p
}

func isHex(s string) bool {
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

// newestForInstance returns the newest version of a project usable by an
// instance.
func newestForInstance(a *app.App, inst *instance.Info, projectID string) *modrinth.Version {
	loader := loaderName(inst.Type)
	if loader == "" {
		loader = "fabric"
	}
	opts := modrinth.VersionListOptions{Loaders: []string{loader}}
	if inst.MCVersion != "" {
		opts.GameVersions = []string{inst.MCVersion}
	}
	vers, err := a.MR().Versions(context.Background(), projectID, opts)
	if err != nil {
		return nil
	}
	filtered := modrinth.FilterByChannel(vers, modrinth.ChannelRelease)
	if len(filtered) == 0 {
		return nil
	}
	return &filtered[0]
}
