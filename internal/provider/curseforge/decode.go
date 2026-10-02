package curseforge

import (
	"encoding/json"
	"fmt"
	"strings"
)

// VersionID is one entry of CurseForge's sortableGameVersions list.
//
// That list holds identifiers — game version ids and loader category ids mixed
// together — and not names. CurseForge has shipped it as a JSON array of
// numbers on some endpoints and as an array of strings on others, and a
// decoder that accepts only one of them fails the whole response: one such
// file anywhere in a listing takes every sibling file down with it, and the
// project then reads as publishing nothing. Decoding both shapes to the same
// textual id keeps one unreadable file from hiding a hundred readable ones.
//
// The ids themselves stay meaningful here, so nothing is widened to
// accommodate the alternative shape: File.GameVersions keeps its int ids and
// ModFile.GameVersions keeps its names.
type VersionID string

// UnmarshalJSON accepts a number or a string and yields the id as text.
func (v *VersionID) UnmarshalJSON(b []byte) error {
	raw := strings.TrimSpace(string(b))
	switch {
	case raw == "" || raw == "null":
		// A missing entry says nothing about the file, and inventing an id
		// would put "0" in a list a caller may use to query the API.
		*v = ""
		return nil
	case raw[0] == '"':
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*v = VersionID(s)
		return nil
	case raw[0] == '-' || (raw[0] >= '0' && raw[0] <= '9'):
		var n json.Number
		if err := json.Unmarshal(b, &n); err != nil {
			return err
		}
		// The literal text is kept rather than a re-formatted number, so an id
		// survives exactly as the server wrote it.
		*v = VersionID(n.String())
		return nil
	default:
		return fmt.Errorf("curseforge: game version id %s is neither a number nor a string", raw)
	}
}
